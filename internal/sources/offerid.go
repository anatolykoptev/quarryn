package sources

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/anatolykoptev/go-engine/sources"
)

// Stable offer identity (issue #56, stolen from northcinder's UCP codec):
// every offer carries a resolvable ID so a later call can re-fetch the
// SAME listing — required for watches and "is this still available"
// probes. A URL is not an identity: variants, redirects and tracker
// params all change it.
//
// Wire format: pipe-separated, first segment is the scheme:
//
//	ebay|<itemId>                — itemId is globally unique
//	etsy|<listingId>             — listing_id is globally unique
//	slickdeals|<threadId>        — /f/<id>-slug parsed at the adapter edge
//	shopify|<shop-domain>|<id>   — product ids are per-shop, seller scopes
//	url|<sha256-16>              — fallback: hash of the normalized URL
//
// The url| fallback is deliberately last: it is stable only per exact
// canonical URL and exists so offers without a native ID still get *a*
// stable handle, not a missing one.

// slickdealsThreadRe extracts the thread id from /f/<id>-slug URLs —
// the thread, not the URL, is the listing identity.
var slickdealsThreadRe = regexp.MustCompile(`(?:^|/)f/(\d+)`)

// OfferID derives the stable offer identity for a Result. Empty when the
// result carries neither a listing id nor a URL (a malformed result, not
// a pass-through).
func OfferID(r sources.Result) string {
	src := r.Metadata[MetaSource]
	if id := strings.TrimSpace(r.Metadata[MetaListingID]); id != "" {
		if src == "shopify" {
			// Shopify product ids are scoped to the shop — without the
			// seller domain "12345" on shop A collides with shop B.
			if shop := strings.TrimSpace(r.Metadata[MetaSeller]); shop != "" {
				return "shopify|" + normalizeHost(shop) + "|" + id
			}
		}
		if src != "" {
			return src + "|" + id
		}
		return "listing|" + id
	}
	if src == "slickdeals" {
		if m := slickdealsThreadRe.FindStringSubmatch(r.URL); len(m) == 2 {
			return "slickdeals|" + m[1]
		}
	}
	if h := urlHash(r.URL); h != "" {
		return "url|" + h
	}
	return ""
}

// ParseOfferID splits a wire-format ID into (adapter, identity parts).
// ok=false when the string is not an offer id (no pipe, empty head).
func ParseOfferID(id string) (adapter string, parts []string, ok bool) {
	segs := strings.Split(id, "|")
	if len(segs) < 2 || segs[0] == "" {
		return "", nil, false
	}
	for _, s := range segs[1:] {
		if s == "" {
			return "", nil, false
		}
	}
	return segs[0], segs[1:], true
}

// urlHash hashes the URL with the fragment dropped and query parameters
// sorted — the two sources of spurious URL instability after the funnel's
// tracking-param cleanup. Lowercased scheme+host so case variants share
// one identity.
func urlHash(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(u.Scheme + "://" + u.Host + u.EscapedPath())
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			b.WriteString("&" + k + "=" + v)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8]) // 16 hex chars — collision-safe at offer scale
}

// normalizeHost strips a scheme and path if the caller passed a URL-ish
// value instead of a bare host (shopify MetaSeller is already a domain,
// but a UCP seller may arrive as "https://shop.example").
func normalizeHost(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}
