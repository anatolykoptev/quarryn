package sources

import (
	"net/url"
	"strings"
)

// TrackingParams is the shared affiliate/tracking denylist applied at the
// candidate boundary (funnel) and reused by extract's canonicalization and
// buy-URL cleaning. Every key is a parameter that carries no product
// identity — identity lives in the path (/dp/ASIN, /itm/ID,
// /products/handle, /f/<thread>). utm_* is matched by PREFIX, not
// membership.
//
// The set deliberately covers click IDs, session tokens, affiliate tags and
// campaign params observed across marketplaces and aggregators (slickdeals
// utm_*, Shopify global-catalog _gsid, CJ cjdata/cjevent, ebay mkcid/mkevt/
// mkrid, Amazon tag/ref).
var TrackingParams = map[string]struct{}{
	// click ids
	"fbclid": {}, "gclid": {}, "gbraid": {}, "wbraid": {}, "msclkid": {},
	"dclid": {}, "twclid": {}, "ttclid": {}, "igshid": {}, "irclickid": {},
	"clickid": {},
	// session tokens
	"sessionid": {}, "session_id": {}, "phpsessid": {}, "jsessionid": {},
	"_ga": {}, "_gac": {}, "_gl": {}, "_gsid": {},
	"mc_cid": {}, "mc_eid": {},
	// affiliate / campaign attribution
	"aff": {}, "affid": {}, "aff_id": {}, "affiliate": {}, "affiliate_id": {},
	"ascsubtag": {}, "campid": {}, "customid": {}, "siteid": {},
	"mkcid": {}, "mkevt": {}, "mkrid": {},
	"cjdata": {}, "cjevent": {}, "cjpub": {}, "cjrefer": {},
	"tag": {}, "ref": {}, "ref_": {},
}

// IsTrackingParam reports whether a query key is a tracker: denylist
// membership or the utm_ prefix.
func IsTrackingParam(key string) bool {
	lk := strings.ToLower(key)
	if strings.HasPrefix(lk, "utm_") {
		return true
	}
	_, ok := TrackingParams[lk]
	return ok
}

// CleanTrackingURL drops affiliate/tracking params from a URL while keeping
// every other byte (case, ordering of surviving params, fragment) intact.
// Unparseable URLs are returned unchanged — the funnel's SSRF guard decides
// their fate.
func CleanTrackingURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	q := u.Query()
	dirty := false
	for k := range q {
		if IsTrackingParam(k) {
			q.Del(k)
			dirty = true
		}
	}
	if !dirty {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}
