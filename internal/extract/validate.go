package extract

import (
	"math"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Sanity bounds (ADR-2): extracted values are rejected, never clamped and
// never fabricated. A rejection flags the whole candidate
// extraction_failed — degraded but visible.
const (
	minPrice          = 0.01
	maxPrice          = 10_000_000
	maxRating         = 5.0
	maxNameLen        = 500  // runes
	maxSellerLen      = 200  // runes
	maxURLLen         = 2048 // bytes
	maxDescriptionLen = 2000 // runes; the egress blurb caps lower
)

// currencies is the ISO 4217 allowlist (current alphabetic codes; XXX — "no
// currency" — deliberately excluded so meaningless data is rejected).
var currencies = toSet(`
AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BHD BIF BMD BND BOB
BOV BRL BSD BTN BWP BYN BZD CAD CDF CHE CHF CHW CLF CLP CNY COP COU CRC CUP
CVE CZK DJF DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP GEL GHS GIP GMD GNF GTQ
GYD HKD HNL HTG HUF IDR ILS INR IQD IRR ISK JMD JOD JPY KES KGS KHR KMF KPW
KRW KWD KYD KZT LAK LBP LKR LRD LSL LYD MAD MDL MGA MKD MMK MNT MOP MRU MUR
MVR MWK MXN MXV MYR MZN NAD NGN NIO NOK NPR NZD OMR PAB PEN PGK PHP PKR PLN
PYG QAR RON RSD RUB RWF SAR SBD SCR SDG SEK SGD SHP SLE SOS SRD SSP STN SVC
SYP SZL THB TJS TMT TND TOP TRY TTD TWD TZS UAH UGX USD USN UYI UYU UYW UZS
VED VES VND VUV WST XAF XAG XAU XBA XBB XBC XBD XCD XDR XOF XPD XPF XPT XSU
XTS XUA YER ZAR ZMW ZWG`)

// conditions is the canonical condition enum. Raw marketplace and
// schema.org vocabulary is mapped onto it by normalizeCondition; anything
// unmapped becomes "" (an absent optional field), never a stored value.
var conditions = toSet(`
new like_new refurbished used for_parts damaged`)

// availabilities is the canonical availability enum — a superset of the
// sources.MetaAvailability vocabulary plus the schema.org states that
// carry distinct meaning for matching.
var availabilities = toSet(`
in_stock out_of_stock pre_order backorder limited discontinued`)

func toSet(s string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, w := range strings.Fields(s) {
		out[w] = struct{}{}
	}
	return out
}

// priceValid reports whether a price pointer carries a sane value.
func priceValid(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) &&
		*p >= minPrice && *p <= maxPrice
}

func currencyOK(c string) bool {
	_, ok := currencies[c]
	return ok
}

// missingRequired reports whether the product lacks (or carries invalid)
// required fields: name + price + currency. It is the trigger for detail
// fetches and the LLM gate — cheaper than a full problems() pass.
func missingRequired(p *Product) bool {
	return strings.TrimSpace(p.Name) == "" || !priceValid(p.Price) || !currencyOK(p.Currency)
}

// problems lists every validation problem on a merged product; empty means
// the product may reach the jeff path. Missing required fields and invalid
// values both count — the caller maps them to the failure reason.
func (p *Product) problems() []string {
	var out []string
	out = p.requiredProblems(out)
	out = p.enumProblems(out)
	out = p.boundProblems(out)
	return out
}

// requiredProblems checks the required trio: name, price, currency.
func (p *Product) requiredProblems(out []string) []string {
	if s := strings.TrimSpace(p.Name); s == "" {
		out = append(out, "missing name")
	} else if utf8.RuneCountInString(s) > maxNameLen {
		out = append(out, "name too long")
	}
	if p.Price == nil {
		out = append(out, "missing price")
	} else if !priceValid(p.Price) {
		out = append(out, "price out of bounds")
	}
	if p.Price != nil && !currencyOK(p.Currency) {
		out = append(out, "missing or unknown currency")
	}
	return out
}

// enumProblems checks the enum-governed optional fields — a present value
// must be a canonical member (normalizers emit only enum values or "").
func (p *Product) enumProblems(out []string) []string {
	if p.Condition != "" {
		if _, ok := conditions[p.Condition]; !ok {
			out = append(out, "unknown condition")
		}
	}
	if p.Availability != "" {
		if _, ok := availabilities[p.Availability]; !ok {
			out = append(out, "unknown availability")
		}
	}
	if p.Rating != nil && (math.IsNaN(*p.Rating) || *p.Rating < 0 || *p.Rating > maxRating) {
		out = append(out, "rating out of bounds")
	}
	return out
}

// boundProblems checks URL shape and per-field length caps.
func (p *Product) boundProblems(out []string) []string {
	if u, err := url.Parse(p.URL); err != nil || u.Host == "" ||
		(u.Scheme != "http" && u.Scheme != "https") || len(p.URL) > maxURLLen {
		out = append(out, "bad url")
	}
	if utf8.RuneCountInString(p.SellerName) > maxSellerLen {
		out = append(out, "seller too long")
	}
	if len(p.ImageURL) > maxURLLen {
		out = append(out, "image url too long")
	}
	if utf8.RuneCountInString(p.Description) > maxDescriptionLen {
		out = append(out, "description too long")
	}
	return out
}

// normalizeCurrency uppercases ISO codes and maps common symbols. Anything
// else returns unchanged for problems() to reject.
func normalizeCurrency(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch s {
	case "$", "US$", "USD$":
		return "USD"
	case "€":
		return "EUR"
	case "£":
		return "GBP"
	}
	return s
}

// normalizeCondition maps marketplace and schema.org condition vocabulary
// onto the conditions enum ("NewCondition" → "new", eBay "For parts or not
// working" → "for_parts"). Unrecognized values → "": condition is optional,
// so an unmappable raw value degrades to absent rather than poisoning the
// record.
func normalizeCondition(raw string) string {
	s := schemaTail(raw)
	s = strings.TrimSuffix(s, "condition") // schema.org *Condition enums
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "new", "brand_new":
		return "new"
	case "new_other", "like_new", "open_box", "mint", "excellent":
		return "like_new"
	case "refurbished", "manufacturer_refurbished", "seller_refurbished",
		"certified_refurbished", "renewed", "remanufactured":
		return "refurbished"
	case "used", "pre_owned", "preowned", "very_good", "good", "acceptable",
		"vintage":
		return "used"
	case "for_parts", "for_parts_or_not_working", "not_working":
		return "for_parts"
	case "damaged", "defect", "defective":
		return "damaged"
	}
	// Fuzzy tails for eBay-style free text ("new other (see details)").
	switch {
	case strings.HasPrefix(s, "for_parts") || strings.Contains(s, "not_working"):
		return "for_parts"
	case strings.Contains(s, "refurb"), strings.Contains(s, "renewed"):
		return "refurbished"
	case strings.Contains(s, "damag"), strings.Contains(s, "defect"):
		return "damaged"
	case strings.HasPrefix(s, "new"):
		return "new"
	case strings.HasPrefix(s, "used"):
		return "used"
	}
	return ""
}

// normalizeAvailability maps schema.org availability URLs and adapter
// vocabulary onto the availabilities enum. Unrecognized → "".
func normalizeAvailability(raw string) string {
	s := schemaTail(raw)
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "instock", "in_stock", "instoreonly", "in_store_only",
		"onlineonly", "online_only", "available", "active":
		return "in_stock"
	case "outofstock", "out_of_stock", "soldout", "sold_out",
		"unavailable", "inactive", "oos":
		return "out_of_stock"
	case "preorder", "pre_order", "presale", "pre_sale",
		"madetoorder", "made_to_order":
		return "pre_order"
	case "backorder", "back_order", "backordered":
		return "backorder"
	case "limitedavailability", "limited_availability", "limited", "low_stock":
		return "limited"
	case "discontinued", "endofline", "end_of_line":
		return "discontinued"
	}
	return ""
}

// schemaTail trims a schema.org enumeration URL to its final path segment
// ("https://schema.org/InStock" → "instock"); plain strings pass through.
func schemaTail(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
