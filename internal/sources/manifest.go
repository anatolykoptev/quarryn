package sources

import (
	"fmt"
	"net/url"
	"strings"
)

// Manifest is the adapter's declared contract (issue #55, stolen from
// northcinder's adapter manifest): which hosts its listing URLs may point
// at, and whether it may act inside the user's browser session.
//
// The funnel enforces AllowedHosts at the egress boundary — a result whose
// URL host is outside the manifest is dropped, so a buggy or compromised
// adapter cannot smuggle arbitrary hosts into the offer stream. The ID is
// the offer's provenance handle ("sourceStore"): it always equals the
// adapter Name(), asserted by the conformance test.
type Manifest struct {
	ID           string   // manifest id — must equal Adapter.Name()
	AllowedHosts []string // bare host | "*.domain.tld" | "*" (unrestricted)
	// UserSession marks adapters whose flow acts inside a user/browser
	// session — for us: ResolveOutbound sources that hop merchant trackers
	// through the wowa browser. Declared, not derived: the conformance test
	// pins it equal to SourceSpec.ResolveOutbound so the two can't drift.
	UserSession bool
}

// Validate checks the manifest's shape: non-empty id, at least one host
// pattern, every pattern well-formed. Called at adapter construction and
// by the conformance harness — an invalid manifest fails loudly, never
// silently unrestricted.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("manifest: empty id")
	}
	if len(m.AllowedHosts) == 0 {
		return fmt.Errorf("manifest %q: allowedHosts is empty", m.ID)
	}
	for _, p := range m.AllowedHosts {
		if err := validateHostPattern(p); err != nil {
			return fmt.Errorf("manifest %q: %w", m.ID, err)
		}
	}
	return nil
}

// Allows reports whether host is inside the manifest. host is a bare
// hostname (port already stripped). A malformed/empty pattern or host
// never matches.
func (m Manifest) Allows(host string) bool {
	host = normalizeManifestHost(host)
	if host == "" {
		return false
	}
	for _, p := range m.AllowedHosts {
		p = normalizeManifestHost(p)
		switch {
		case p == "*":
			return true
		case strings.HasPrefix(p, "*."):
			// "*.domain.tld" covers subdomains, not the apex — the apex
			// needs its own bare entry.
			apex := p[2:]
			if host != apex && strings.HasSuffix(host, "."+apex) {
				return true
			}
		case p == host:
			return true
		}
	}
	return false
}

// AllowsURL is the funnel-facing helper: parse the URL, pull its
// hostname, apply Allows. An unparseable or hostless URL never matches —
// except under a declared "*" manifest, where any parseable URL passes.
func (m Manifest) AllowsURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return m.Allows(u.Hostname())
}

// validateHostPattern accepts: "*" (declared unrestricted — e.g. shopify's
// arbitrary storefront domains), "*.domain.tld" wildcards scoped to at
// least two labels under the star ("*.com" is rejected), or a bare
// hostname containing a dot.
func validateHostPattern(p string) error {
	if p == "*" {
		return nil
	}
	if strings.HasPrefix(p, "*.") {
		if labels := strings.Count(p[2:], ".") + 1; labels >= 2 {
			return nil
		}
		return fmt.Errorf("wildcard %q too broad — needs >=2 labels under the star", p)
	}
	if strings.ContainsAny(p, "*/ ") || !strings.Contains(p, ".") {
		return fmt.Errorf("host pattern %q must be a bare host with a dot, a >=2-label wildcard, or *", p)
	}
	return nil
}

// normalizeManifestHost folds case, strips a trailing dot and any port.
func normalizeManifestHost(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	h = strings.TrimSuffix(h, ".")
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h
}
