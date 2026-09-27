package extract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/anatolykoptev/quarryn/internal/money"
	"net/url"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-enriche/structured"
	"github.com/astappiev/microdata"
)

// errNoProduct is returned when the page carries no schema.org Product
// item — the trigger for the LLM fallback tier.
var errNoProduct = errors.New("schema: no Product item in structured data")

// maxRawLen caps the provenance blob stored in Product.Raw.
const maxRawLen = 8 * 1024

// productFromSchema parses JSON-LD and HTML microdata from a product page
// and maps the best-matching Product item onto a Product.
func productFromSchema(body []byte, pageURL string) (*Product, error) {
	d, err := structured.Parse(bytes.NewReader(body), "text/html", pageURL)
	if err != nil {
		return nil, fmt.Errorf("schema: parse: %w", err)
	}
	item := pickProductItem(d.Raw(), pageURL)
	if item == nil {
		return nil, errNoProduct
	}
	p := productFromItem(item, pageURL)
	if raw, merr := json.Marshal(item); merr == nil && len(raw) <= maxRawLen {
		p.Raw = raw
	}
	return p, nil
}

// pickProductItem selects the Product item for this page. The canonical
// accessor answers the common single-Product page directly (top-level or
// @graph); multi-product pages — ItemList cards, the SERP shape e.g. Etsy
// emits — are walked by productItems and matched on each product's own url.
func pickProductItem(md *microdata.Microdata, pageURL string) *microdata.Item {
	canonPage := CanonicalURL(pageURL)
	if it := md.GetFirstOfSchemaType("Product"); it != nil && productURLMatches(it, canonPage) {
		return it
	}
	items := productItems(md)
	for _, it := range items {
		if productURLMatches(it, canonPage) {
			return it
		}
	}
	if len(items) > 0 {
		return items[0]
	}
	return nil
}

// productURLMatches reports whether the item's url property resolves to the
// fetched page under canonicalization.
func productURLMatches(it *microdata.Item, canonPage string) bool {
	if canonPage == "" {
		return false
	}
	s := propStr(it, "url")
	return s != nil && CanonicalURL(*s) == canonPage
}

// productItems flattens Product items out of the three container shapes
// real pages use.
func productItems(md *microdata.Microdata) []*microdata.Item {
	var out []*microdata.Item
	for _, it := range md.Items {
		out = appendProductItems(out, it)
	}
	return out
}

func appendProductItems(out []*microdata.Item, it *microdata.Item) []*microdata.Item {
	if it.IsOfSchemaType("Product") || it.IsOfSchemaType("ProductGroup") {
		return append(out, it)
	}
	if g, ok := it.GetNested("@graph"); ok {
		for _, gi := range g.Items {
			out = appendProductItems(out, gi)
		}
	}
	if it.IsOfSchemaType("ItemList") {
		out = appendListProducts(out, it)
	}
	return out
}

// appendListProducts flattens Product items out of an ItemList's
// itemListElement — entries are either bare Products or ListItem wrappers
// holding the Product in their item prop.
func appendListProducts(out []*microdata.Item, list *microdata.Item) []*microdata.Item {
	els, ok := list.GetNested("itemListElement")
	if !ok {
		return out
	}
	for _, el := range els.Items {
		if el.IsOfSchemaType("Product") {
			out = append(out, el)
			continue
		}
		if inner, ok := el.GetNestedItem("item"); ok && inner.IsOfSchemaType("Product") {
			out = append(out, inner)
		}
	}
	return out
}

// productFromItem maps one schema.org Product item to a Product. Offer
// fields come from the offers list (Offer or AggregateOffer); every
// recognized value is normalized — enum mapping lives here, bounds checks
// live in problems().
func productFromItem(item *microdata.Item, pageURL string) *Product {
	p := &Product{
		URL:    pageURL,
		Source: domainOf(pageURL),
		Method: MethodSchema,
	}
	if s := propStr(item, "name"); s != nil {
		p.Name = *s
	}
	if s := propStr(item, "description"); s != nil {
		p.Description = *s
	}
	if s := propStr(item, "image"); s != nil {
		p.ImageURL = *s
	} else if img, ok := item.GetNestedItem("image"); ok {
		if s := propStr(img, "url"); s != nil {
			p.ImageURL = *s
		}
	}
	if ar, ok := item.GetNestedItem("aggregateRating"); ok {
		if s := propStr(ar, "ratingValue"); s != nil {
			if f, ferr := strconv.ParseFloat(strings.TrimSpace(*s), 64); ferr == nil {
				p.Rating = &f
			}
		}
	}
	if offers, ok := item.GetNested("offers"); ok {
		for _, off := range offers.Items {
			applyOffer(p, off)
		}
	}
	// ProductGroup (Google's variant shape on Shopify etc.): offers and
	// variant attributes live on hasVariant Products — earlier variants
	// win, matching the offer precedence above.
	if item.IsOfSchemaType("ProductGroup") {
		applyVariantOffers(p, item)
	}
	p.Currency = normalizeCurrency(p.Currency)
	p.Condition = normalizeCondition(p.Condition)
	p.Availability = normalizeAvailability(p.Availability)
	return p
}

// applyVariantOffers fills Product offer fields from a ProductGroup's
// hasVariant Products; earlier variants win on conflicts.
func applyVariantOffers(p *Product, group *microdata.Item) {
	vars, ok := group.GetNested("hasVariant")
	if !ok {
		return
	}
	for _, v := range vars.Items {
		if offers, ok := v.GetNested("offers"); ok {
			for _, off := range offers.Items {
				applyOffer(p, off)
			}
		}
	}
}

// applyOffer fills still-empty Product fields from one Offer or
// AggregateOffer item; earlier offers win on conflicts.
func applyOffer(p *Product, off *microdata.Item) {
	var priceStr *string
	if p.PriceMinor == nil {
		priceStr = propStr(off, "price", "lowPrice")
	}
	fillStr(&p.Currency, off, "priceCurrency")
	// Currency is filled first so the literal converts with the offer's
	// own exponent (JPY "1999" is 1999, not 19.99).
	if priceStr != nil {
		if m, ok := money.ToMinor(*priceStr, p.Currency); ok {
			p.PriceMinor = &m
		}
	}
	fillStr(&p.Availability, off, "availability")
	fillStr(&p.Condition, off, "itemCondition")
	if p.SellerName == "" {
		if seller, ok := off.GetNestedItem("seller"); ok {
			fillStr(&p.SellerName, seller, "name")
		}
	}
}

// fillStr sets *dst from the first usable property value when *dst is
// empty — a fill-missing primitive for offer/item field merges.
func fillStr(dst *string, it *microdata.Item, keys ...string) {
	if *dst != "" {
		return
	}
	if s := propStr(it, keys...); s != nil {
		*dst = *s
	}
}

// propStr returns the first usable string for any of the given property
// keys — the go-enriche propString pattern: microdata values arrive as
// string, float64 (JSON-LD numbers), bool or nested items.
func propStr(item *microdata.Item, keys ...string) *string {
	for _, key := range keys {
		val, ok := item.GetProperty(key)
		if !ok {
			continue
		}
		var s string
		switch v := val.(type) {
		case string:
			s = v
		case float64:
			s = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			s = strconv.FormatBool(v)
		case fmt.Stringer:
			s = v.String()
		default:
			continue // nested items and other shapes are not scalars
		}
		if s = strings.TrimSpace(s); s != "" {
			return &s
		}
	}
	return nil
}

// domainOf returns the lowercased product-page host without a www. prefix.
func domainOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}
