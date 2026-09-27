package extract

import (
	"net/url"
	"strings"

	"github.com/anatolykoptev/go-kit/cache"
	pssources "github.com/anatolykoptev/quarryn/internal/sources"
)

// CanonicalURL normalizes a candidate URL for cache keys: lowercase scheme
// and host, drop fragment/userinfo/default port/trailing slash, strip
// tracking and session params, sort the rest. Returns "" for URLs that are
// unparseable or non-HTTP(S) — callers skip caching for them (the funnel's
// SSRF guard already keeps such URLs out of the pipeline).
func CanonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimSuffix(u.Host, ".")
	u.Fragment = ""
	u.User = nil
	if (u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) ||
		(u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) {
		u.Host = u.Host[:strings.LastIndexByte(u.Host, ':')]
	}
	q := u.Query()
	for k := range q {
		if pssources.IsTrackingParam(k) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.ForceQuery = false
	if len(u.Path) > 1 {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	return u.String()
}

// extractorVersion namespaces every extraction-cache key. Bump it whenever
// parsing, normalization or validation semantics change so stale entries
// die with their TTL instead of being reinterpreted.
const extractorVersion = "v1"

// cacheKey builds the versioned extraction-cache key (ADR-7): "v1:" +
// FNV-128a of the canonical URL. The Redis L2 prefix ("quarryn:") is set
// at cache construction, so the full Redis key reads "quarryn:v1:<fnv>".
func cacheKey(rawURL string) string {
	canon := CanonicalURL(rawURL)
	if canon == "" {
		return ""
	}
	return extractorVersion + ":" + cache.Key(canon)
}
