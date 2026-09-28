package extract

import (
	"errors"
	"testing"

	"github.com/anatolykoptev/go-kit/wowa"
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
