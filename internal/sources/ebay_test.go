package sources

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
)

// ebayFixture is a realistic Browse API item_summary/search payload.
const ebayFixture = `{
  "total": 2,
  "itemSummaries": [
    {
      "itemId": "v1|123456789|0",
      "title": "Sony WH-1000XM5 Wireless Headphones",
      "itemWebUrl": "https://www.ebay.com/itm/123456789",
      "price": {"value": "278.00", "currency": "USD"},
      "condition": "NEW",
      "seller": {"username": "audio-deals", "feedbackPercentage": "99.4"},
      "buyingOptions": ["FIXED_PRICE", "BEST_OFFER"],
      "image": {"imageUrl": "https://i.ebayimg.com/images/g/abc/s-l500.jpg"}
    },
    {
      "itemId": "v1|987654321|0",
      "title": "Sony WH-1000XM5 Headphones Refurbished",
      "itemWebUrl": "https://www.ebay.com/itm/987654321",
      "price": {"value": "199.99", "currency": "USD"},
      "condition": "REFURBISHED",
      "seller": {"username": "refurb-outlet"},
      "buyingOptions": ["FIXED_PRICE"]
    }
  ]
}`

// newEbayFakeServer emulates the eBay OAuth token endpoint and the Browse
// search endpoint, asserting the auth shape the adapter must send.
func newEbayFakeServer(t *testing.T, searchBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ebayTokenPath:
			user, pass, ok := r.BasicAuth()
			if !ok || user != "test-id" || pass != "test-secret" {
				http.Error(w, "bad basic auth", http.StatusUnauthorized)
				return
			}
			if r.FormValue("grant_type") != "client_credentials" {
				http.Error(w, "bad grant_type", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-1","expires_in":7200}`))
		case ebaySearchPath:
			if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("X-EBAY-C-MARKETPLACE-ID"); got != ebayDefaultMarketplace {
				http.Error(w, "missing marketplace", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(searchBody))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestEBaySearchParsesItems(t *testing.T) {
	srv := newEbayFakeServer(t, ebayFixture)
	defer srv.Close()

	a := NewEBay("test-id", "test-secret", srv.URL, srv.Client())
	if !a.Enabled() {
		t.Fatal("adapter with credentials must be enabled")
	}

	res, err := a.Search(t.Context(), sources.Query{Text: "sony wh-1000xm5", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}

	first := res[0]
	if first.Title != "Sony WH-1000XM5 Wireless Headphones" {
		t.Errorf("title = %q", first.Title)
	}
	if first.URL != "https://www.ebay.com/itm/123456789" {
		t.Errorf("url = %q", first.URL)
	}
	if got := first.Metadata[MetaPrice]; got != "278.00" {
		t.Errorf("price = %q", got)
	}
	if got := first.Metadata[MetaCurrency]; got != "USD" {
		t.Errorf("currency = %q", got)
	}
	if got := first.Metadata[MetaCondition]; got != "NEW" {
		t.Errorf("condition = %q", got)
	}
	if got := first.Metadata[MetaSeller]; got != "audio-deals" {
		t.Errorf("seller = %q", got)
	}
	if got := first.Metadata[MetaBuyingOptions]; got != "FIXED_PRICE,BEST_OFFER" {
		t.Errorf("buying_options = %q", got)
	}
	if got := first.Metadata[MetaAvailability]; got != AvailabilityInStock {
		t.Errorf("availability = %q", got)
	}
}

func TestEBayDisabledWithoutCredentials(t *testing.T) {
	a := NewEBay("", "", "", nil)
	if a.Enabled() {
		t.Fatal("adapter without credentials must be disabled")
	}
	if _, err := a.Search(t.Context(), sources.Query{Text: "x"}); err == nil {
		t.Fatal("Search on disabled adapter must error")
	}
}

func TestEBayDailyCap(t *testing.T) {
	srv := newEbayFakeServer(t, ebayFixture)
	defer srv.Close()

	a := NewEBay("test-id", "test-secret", srv.URL, srv.Client(), withEbayDailyCap(1))
	if _, err := a.Search(t.Context(), sources.Query{Text: "a"}); err != nil {
		t.Fatalf("first search within cap: %v", err)
	}
	if _, err := a.Search(t.Context(), sources.Query{Text: "b"}); err == nil ||
		!strings.Contains(err.Error(), "daily call cap") {
		t.Fatalf("second search past cap must fail with cap error, got %v", err)
	}
}

func TestEBayUpstreamError(t *testing.T) {
	srv := newEbayFakeServer(t, "")
	// Close immediately — requests to a dead server must surface an error.
	srv.Close()

	a := NewEBay("test-id", "test-secret", srv.URL, srv.Client())
	if _, err := a.Search(t.Context(), sources.Query{Text: "x"}); err == nil {
		t.Fatal("closed server must produce an error")
	}
}

// Ensure the token request is not retried per search — second search must
// reuse the cached token (server would fail a second token call? no — but
// assert caching via counting handler).
func TestEBayTokenCached(t *testing.T) {
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ebayTokenPath:
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok-1", "expires_in": 7200,
			})
		case ebaySearchPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(ebayFixture))
		}
	}))
	defer srv.Close()

	a := NewEBay("id", "secret", srv.URL, srv.Client(), withEbayDailyCap(10))
	for i := 0; i < 3; i++ {
		if _, err := a.Search(t.Context(), sources.Query{Text: "x"}); err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("tokenCalls = %d, want 1 (cached)", tokenCalls)
	}
}
