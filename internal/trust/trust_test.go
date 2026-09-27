package trust

import "testing"

// TestClassifyPrecedence — deny beats allow beats platform; an operator
// deny must win even against a conflicting allow or a platform hit.
func TestClassifyPrecedence(t *testing.T) {
	p := New([]string{"ebay.com", "crutchfield.com"}, []string{"scam.example", "ebay.com"})
	if got := p.Classify("ebay.com"); got != Flagged {
		t.Fatalf("deny must beat allow: %v", got)
	}
	if got := p.Classify("deals.ebay.com"); got != Flagged {
		t.Fatalf("deny suffix must beat platform too: %v", got)
	}
	if got := p.Classify("shop.crutchfield.com"); got != Trusted {
		t.Fatalf("allow suffix: %v", got)
	}
	if got := p.Classify("www.bestbuy.com"); got != Known {
		t.Fatalf("platform www-suffix: %v", got)
	}
	if got := p.Classify("notebay.com"); got != Unknown {
		t.Fatalf("suffix must not leap a dot boundary: %v", got)
	}
	if got := p.Classify("random-shop.example"); got != Unknown {
		t.Fatalf("default tier: %v", got)
	}
}

// TestClassifyNormalizes — operator lists arrive as env strings and may
// carry scheme/case/www; classification must normalize before matching.
func TestClassifyNormalizes(t *testing.T) {
	p := New([]string{"  HTTPS://WWW.Crutchfield.com/x "}, nil)
	if got := p.Classify("crutchfield.com"); got != Trusted {
		t.Fatalf("env normalization: %v", got)
	}
}

// TestClassifyMerchantPicksBuyerDomain — trust answers "who will I pay":
// a slickdeals listing resolved to woot must classify as woot, not as the
// aggregator that listed it.
func TestClassifyMerchantPicksBuyerDomain(t *testing.T) {
	p := New(nil, nil)
	got := p.ClassifyMerchant("https://electronics.woot.com/offers/x", "https://slickdeals.net/f/123")
	if got != Known {
		t.Fatalf("buy_url host wins: %v", got)
	}
	// no buy_url → listing domain decides
	if got := p.ClassifyMerchant("", "https://random.example/p/1"); got != Unknown {
		t.Fatalf("fallback to listing host: %v", got)
	}
	if got := p.ClassifyMerchant("::not a url", "https://ebay.com/itm/1"); got != Unknown {
		t.Fatalf("unparseable → unknown, not the listing tier: %v", got)
	}
}

// TestNilProvider — an unconfigured provider must classify everything as
// unknown rather than panic (deps wiring is optional in tests/tools).
func TestNilProvider(t *testing.T) {
	var p *Provider
	if got := p.ClassifyMerchant("https://ebay.com/itm/1", ""); got != Unknown {
		t.Fatalf("nil provider must be inert: %v", got)
	}
}
