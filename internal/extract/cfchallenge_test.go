package extract

import (
	"errors"
	"testing"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

// The solver layer reports a bot wall as a clearance timeout, not a
// challenge flag — live signature from prod on ebay/woot (issue #112):
// "proxy pool error: solver failed: timeout waiting for cf_clearance".
// Misreading it as a plain fetch failure skips the render/interact tiers
// that exist precisely to clear such walls.
func TestCFChallengeSolverTimeout(t *testing.T) {
	err := &wowa.RemoteError{Endpoint: "/api/v1/fetch", StatusCode: 502,
		Message: "proxy pool error: solver failed: timeout waiting for cf_clearance"}
	if !isCFChallengeError(err) {
		t.Fatalf("solver clearance timeout not recognized as a challenge: %v", err)
	}
}

func TestCFChallengeExistingSignatures(t *testing.T) {
	for _, msg := range []string{
		"cloudflare managed_challenge_200",
		"cf_detected on upstream body",
		"remote error (http 502): cf_challenge",
	} {
		if !isCFChallengeError(errors.New(msg)) {
			t.Fatalf("signature %q not recognized", msg)
		}
	}
	// A plain upstream 500 or DNS failure is NOT a challenge — escalating
	// it would burn a render slot for nothing.
	if isCFChallengeError(errors.New("remote error (http 500): upstream reset")) {
		t.Fatal("plain upstream error misclassified as challenge")
	}
}

// A hard 404/410 is the listing-gone signal — its own outcome label so the
// watch layer can keep counting it toward unverifiable while genuine
// transport failures stay transient (review finding on #112).
func TestGoneOutcome(t *testing.T) {
	page := "https://shop.example.com/products/dead"
	f := &routerFetcher{pages: map[string]*wowa.FetchResponse{
		page: {Status: 404, Body: "not found"},
	}}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "shopify", Title: "Dead", URL: page},
	})
	if got := out[0].Outcome; got != "gone" {
		t.Fatalf("outcome = %q, want gone", got)
	}
	if !out[0].ExtractionFailed {
		t.Fatal("gone page must still be ExtractionFailed")
	}
}

// A solver-clearance timeout must escalate to the render tier rather than
// dead-ending as fetch_failed — the ebay signature from prod. With no
// renderer wired the candidate lands render_deferred + NeedsRender, not a
// silent fetch_failed.
func TestSolverTimeoutEscalates(t *testing.T) {
	page := "https://www.ebay.com/itm/123"
	f := &stubFetcher{err: &wowa.RemoteError{Endpoint: "/api/v1/fetch", StatusCode: 502,
		Message: "proxy pool error: solver failed: timeout waiting for cf_clearance"}}
	p := New(f, nil, testConfig())
	out := p.Enrich(t.Context(), []sources.Candidate{
		{Source: "direct", Title: "", URL: page},
	})
	if got := out[0].Outcome; got != "render_deferred" {
		t.Fatalf("outcome = %q, want render_deferred (escalated past fetch)", got)
	}
	if !out[0].NeedsRender {
		t.Fatal("unrendered wall must keep NeedsRender")
	}
}
