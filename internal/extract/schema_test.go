package extract

import (
	"testing"
)

// amazonProductHTML mirrors an Amazon-style detail page: a single JSON-LD
// Product with a scalar Offer and nested seller/aggregateRating.
const amazonProductHTML = `<!doctype html><html><head><title>x</title>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product",
 "name":"Sony WH-1000XM5 Wireless Noise Canceling Headphones",
 "image":"https://m.media-amazon.com/images/I/51aXvjzcukL.jpg",
 "description":"Industry-leading noise canceling with 30-hour battery.",
 "sku":"B09XS7JWHH",
 "url":"https://www.amazon.com/dp/B09XS7JWHH",
 "brand":{"@type":"Brand","name":"Sony"},
 "aggregateRating":{"@type":"AggregateRating","ratingValue":4.7,"ratingCount":41203},
 "offers":{"@type":"Offer","priceCurrency":"USD","price":"278.00",
   "availability":"https://schema.org/InStock",
   "itemCondition":"https://schema.org/NewCondition",
   "seller":{"@type":"Organization","name":"Amazon.com"}}}
</script></head><body><h1>Sony WH-1000XM5</h1></body></html>`

// shopifyProductHTML mirrors a Shopify theme's JSON-LD: Product inside a
// @graph next to WebPage, offers as a LIST, price as a JSON number, and an
// ImageObject rather than a bare URL.
const shopifyProductHTML = `<!doctype html><html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@graph":[
 {"@type":"WebPage","@id":"https://shop.example.com/products/mug"},
 {"@type":"Product","name":"Minimalist Ceramic Mug",
  "description":{"@type":"Thing","name":"Hand-thrown stoneware, 350ml."},
  "image":{"@type":"ImageObject","url":"https://cdn.shop.example.com/mug.jpg"},
  "aggregateRating":{"@type":"AggregateRating","ratingValue":"4.9"},
  "offers":[{"@type":"Offer","price":24.5,"priceCurrency":"USD",
    "availability":"https://schema.org/OutOfStock",
    "itemCondition":"https://schema.org/UsedCondition"}]}]}
</script></head><body><h1>Mug</h1></body></html>`

// etsyItemListHTML mirrors an Etsy-style ItemList of Product cards — the
// candidate's own listing is one of several, so extraction must pick the
// Product whose url matches the page, not the first card.
const etsyItemListHTML = `<!doctype html><html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"ItemList","itemListElement":[
 {"@type":"ListItem","position":1,"item":{"@type":"Product",
   "name":"Handmade Silver Ring","url":"https://www.etsy.com/listing/123/ring",
   "offers":{"@type":"Offer","price":"45.00","priceCurrency":"USD",
     "availability":"https://schema.org/InStock"}}},
 {"@type":"ListItem","position":2,"item":{"@type":"Product",
   "name":"Vintage Travel Poster","url":"https://www.etsy.com/listing/456/poster",
   "offers":{"@type":"Offer","price":12.0,"priceCurrency":"EUR",
     "availability":"https://schema.org/OutOfStock",
     "seller":{"@type":"Organization","name":"PosterShop"}}}}]}
</script></head><body>listing</body></html>`

// microdataProductHTML exercises the itemprop path — meta/link/itemscope
// markup instead of JSON-LD.
const microdataProductHTML = `<!doctype html><html><body>
<div itemscope itemtype="https://schema.org/Product">
  <span itemprop="name">Walnut Desk Organizer</span>
  <img itemprop="image" src="https://cdn.shop.example.com/organizer.jpg">
  <div itemprop="offers" itemscope itemtype="https://schema.org/Offer">
    <meta itemprop="price" content="89.00">
    <meta itemprop="priceCurrency" content="USD">
    <link itemprop="availability" href="https://schema.org/LimitedAvailability">
    <link itemprop="itemCondition" href="https://schema.org/RefurbishedCondition">
  </div>
</div></body></html>`

// malformedSchemaHTML carries a JSON-LD block so broken that even fixjson
// cannot salvage it — the parse yields no items.
const malformedSchemaHTML = `<!doctype html><html><head>
<script type="application/ld+json">{"@type":"Product",,,"name":}</script>
</head><body><h1>no structured data</h1></body></html>`

func TestSchemaAmazonStyle(t *testing.T) {
	p, err := productFromSchema([]byte(amazonProductHTML), "https://www.amazon.com/dp/B09XS7JWHH")
	if err != nil {
		t.Fatalf("productFromSchema: %v", err)
	}
	if p.Name != "Sony WH-1000XM5 Wireless Noise Canceling Headphones" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.Price == nil || *p.Price != 278.00 {
		t.Fatalf("price = %v", p.Price)
	}
	if p.Currency != "USD" || p.Availability != "in_stock" || p.Condition != "new" {
		t.Fatalf("offer fields = %+v", p)
	}
	if p.Rating == nil || *p.Rating != 4.7 {
		t.Fatalf("rating = %v", p.Rating)
	}
	if p.SellerName != "Amazon.com" {
		t.Fatalf("seller = %q", p.SellerName)
	}
	if p.Source != "amazon.com" || p.Method != MethodSchema {
		t.Fatalf("source/method = %q %q", p.Source, p.Method)
	}
	if len(p.Raw) == 0 {
		t.Fatal("raw provenance not recorded")
	}
	if probs := p.problems(); len(probs) > 0 {
		t.Fatalf("valid fixture rejected: %v", probs)
	}
}

func TestSchemaShopifyGraph(t *testing.T) {
	// @graph container + offers list + numeric price + ImageObject.
	p, err := productFromSchema([]byte(shopifyProductHTML), "https://shop.example.com/products/mug")
	if err != nil {
		t.Fatalf("productFromSchema: %v", err)
	}
	if p.Name != "Minimalist Ceramic Mug" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.Price == nil || *p.Price != 24.5 {
		t.Fatalf("price = %v", p.Price)
	}
	if p.Availability != "out_of_stock" || p.Condition != "used" {
		t.Fatalf("avail/cond = %q %q", p.Availability, p.Condition)
	}
	if p.ImageURL != "https://cdn.shop.example.com/mug.jpg" {
		t.Fatalf("image = %q", p.ImageURL)
	}
	if p.Rating == nil || *p.Rating != 4.9 {
		t.Fatalf("rating = %v", p.Rating)
	}
}

func TestSchemaEtsyItemListPicksMatchingURL(t *testing.T) {
	// The page URL matches list element 2 — extraction must not return the
	// first card.
	p, err := productFromSchema([]byte(etsyItemListHTML), "https://www.etsy.com/listing/456/poster")
	if err != nil {
		t.Fatalf("productFromSchema: %v", err)
	}
	if p.Name != "Vintage Travel Poster" {
		t.Fatalf("picked wrong product: %q", p.Name)
	}
	if p.Price == nil || *p.Price != 12.0 || p.Currency != "EUR" {
		t.Fatalf("price/currency = %v %q", p.Price, p.Currency)
	}
	if p.Availability != "out_of_stock" || p.SellerName != "PosterShop" {
		t.Fatalf("avail/seller = %q %q", p.Availability, p.SellerName)
	}
}

func TestSchemaMicrodata(t *testing.T) {
	p, err := productFromSchema([]byte(microdataProductHTML), "https://shop.example.com/p/organizer")
	if err != nil {
		t.Fatalf("productFromSchema: %v", err)
	}
	if p.Name != "Walnut Desk Organizer" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.Price == nil || *p.Price != 89.0 {
		t.Fatalf("price = %v", p.Price)
	}
	if p.Availability != "limited" || p.Condition != "refurbished" {
		t.Fatalf("avail/cond = %q %q", p.Availability, p.Condition)
	}
}

func TestSchemaNoProduct(t *testing.T) {
	if _, err := productFromSchema([]byte(malformedSchemaHTML), "https://x.example.com/p/1"); err == nil {
		t.Fatal("malformed schema must not yield a product")
	}
	if _, err := productFromSchema([]byte(`<html><body>plain</body></html>`), "https://x.example.com/p/1"); err != errNoProduct {
		t.Fatalf("expected errNoProduct, got %v", err)
	}
}

func TestParsePrice(t *testing.T) {
	cases := map[string]float64{
		"278.00":     278.0,
		"$1,299.00":  1299.0,
		"1.299,00 €": 1299.0,
		"1 299,00":   1299.0,
		"12,50":      12.5,
		"USD 9.99":   9.99,
	}
	for in, want := range cases {
		got, ok := parsePrice(in)
		if !ok || got != want {
			t.Errorf("parsePrice(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "no price", "abc"} {
		if _, ok := parsePrice(bad); ok {
			t.Errorf("parsePrice(%q) unexpectedly succeeded", bad)
		}
	}
}
