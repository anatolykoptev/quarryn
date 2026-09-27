// Package trust is the seed trust provider: a domain → tier lookup whose
// verdicts annotate results. It mirrors northcinder's seed-trust
// discipline: only VERIFIABLE signals (the merchant domain the buyer will
// actually pay) classify a candidate — a seller-declared field (scraped
// "platform", self-reported rating) can never raise standing.
package trust

import (
	"net/url"
	"strings"
)

// Tier is the trust standing of a merchant domain. Order of precedence is
// deny > allow > platform > unknown — a deny hit can never be rescued by
// an allow entry.
type Tier string

const (
	// Flagged — explicit deny-list hit; the result stays visible but
	// marked so a consumer can drop it.
	Flagged Tier = "flagged"
	// Trusted — operator allow-list entry.
	Trusted Tier = "trusted"
	// Known — a recognized platform or major-retailer domain.
	Known Tier = "known"
	// Unknown — no signal either way; the honest default.
	Unknown Tier = "unknown"
)

// platformDomains are marketplace, hosted-storefront and major-retailer
// base domains recognized without operator input. Matching is by suffix:
// "www.ebay.com" and "deals.ebay.com" both count, "notebay.com" does not.
var platformDomains = []string{
	// marketplaces / aggregators
	"amazon.com", "amazon.co.uk", "amazon.de", "amazon.fr", "amazon.co.jp",
	"ebay.com", "ebay.co.uk", "etsy.com", "aliexpress.com", "alibaba.com",
	"rakuten.com", "mercari.com", "stockx.com", "goat.com", "g2a.com",
	"slickdeals.net",
	// hosted storefronts
	"myshopify.com", "square.site", "bigcartel.com", "shop.app",
	// major retailers
	"walmart.com", "target.com", "bestbuy.com", "costco.com",
	"newegg.com", "bhphotovideo.com", "woot.com", "wayfair.com",
	"homedepot.com", "lowes.com", "macys.com", "nordstrom.com",
	"zappos.com", "nike.com", "adidas.com", "apple.com", "samsung.com",
	"dell.com", "ikea.com",
}

// Provider resolves domains to tiers. allow/deny are operator-supplied
// base domains (env config); the platform table is built in.
type Provider struct {
	allow map[string]struct{}
	deny  map[string]struct{}
}

// New builds the provider from operator lists; nil lists are fine.
func New(allow, deny []string) *Provider {
	return &Provider{allow: domainSet(allow), deny: domainSet(deny)}
}

func domainSet(list []string) map[string]struct{} {
	m := make(map[string]struct{}, len(list))
	for _, d := range list {
		if d = normalizeDomain(d); d != "" {
			m[d] = struct{}{}
		}
	}
	return m
}

// normalizeDomain accepts "Example.COM/path" or "https://x.example.com"
// and returns the bare lowercase host — the env lists are forgiving.
func normalizeDomain(d string) string {
	d = strings.TrimSpace(strings.ToLower(d))
	if _, h, ok := strings.Cut(d, "://"); ok {
		d = h
	}
	if i := strings.IndexByte(d, '/'); i >= 0 {
		d = d[:i]
	}
	return strings.TrimPrefix(d, "www.")
}

// Classify resolves a bare domain ("www.ebay.com" or "ebay.com") to its
// tier. Deny beats allow, allow beats platform.
func (p *Provider) Classify(domain string) Tier {
	d := normalizeDomain(domain)
	if d == "" || p == nil {
		return Unknown
	}
	for base := range p.deny {
		if d == base || strings.HasSuffix(d, "."+base) {
			return Flagged
		}
	}
	for base := range p.allow {
		if d == base || strings.HasSuffix(d, "."+base) {
			return Trusted
		}
	}
	for _, base := range platformDomains {
		if d == base || strings.HasSuffix(d, "."+base) {
			return Known
		}
	}
	return Unknown
}

// ClassifyMerchant picks the domain the buyer actually pays: the resolved
// buy_url when present, otherwise the listing's own URL. Returns the tier
// for that merchant domain.
func (p *Provider) ClassifyMerchant(buyURL, productURL string) Tier {
	raw := buyURL
	if raw == "" {
		raw = productURL
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Unknown
	}
	return p.Classify(u.Hostname())
}
