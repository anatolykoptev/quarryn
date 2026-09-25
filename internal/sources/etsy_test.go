package sources

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
)

// etsyFixture is a realistic Etsy v3 /v3/application/listings/active payload.
const etsyFixture = `{
  "count": 2,
  "results": [
    {
      "listing_id": 1472098365,
      "title": "Handmade Ceramic Mug — Speckled Stoneware",
      "url": "https://www.etsy.com/listing/1472098365/handmade-ceramic-mug",
      "description": "Wheel-thrown stoneware mug.",
      "state": "active",
      "quantity": 3,
      "shop_id": 8812345,
      "price": {"amount": 4250, "divisor": 100, "currency_code": "USD"},
      "tags": ["ceramic", "mug", "stoneware"]
    },
    {
      "listing_id": 1501999887,
      "title": "Vintage Enamel Mug",
      "url": "https://www.etsy.com/listing/1501999887/vintage-enamel-mug",
      "state": "active",
      "quantity": 0,
      "shop_id": 7712001,
      "price": {"amount": 1899, "divisor": 100, "currency_code": "USD"}
    }
  ]
}`

func TestEtsySearchParsesListings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != etsyActiveListingsPath {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "key-abc:sec-xyz" {
			http.Error(w, "bad x-api-key "+got, http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("keywords") != "ceramic mug" {
			http.Error(w, "missing keywords", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(etsyFixture))
	}))
	defer srv.Close()

	a := NewEtsy("key-abc", "sec-xyz", srv.URL, srv.Client())
	if !a.Enabled() {
		t.Fatal("adapter with api key must be enabled")
	}
	res, err := a.Search(t.Context(), sources.Query{Text: "ceramic mug", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}

	first := res[0]
	if first.URL != "https://www.etsy.com/listing/1472098365/handmade-ceramic-mug" {
		t.Errorf("url = %q", first.URL)
	}
	if got := first.Metadata[MetaPrice]; got != "42.5" {
		t.Errorf("price = %q (amount/divisor)", got)
	}
	if got := first.Metadata[MetaCurrency]; got != "USD" {
		t.Errorf("currency = %q", got)
	}
	if got := first.Metadata[MetaListingID]; got != "1472098365" {
		t.Errorf("listing_id = %q", got)
	}
	if got := first.Metadata[MetaSeller]; got != "8812345" {
		t.Errorf("seller = %q", got)
	}
	if got := first.Metadata[MetaAvailability]; got != AvailabilityInStock {
		t.Errorf("availability = %q", got)
	}
	// quantity=0 → out_of_stock
	if got := res[1].Metadata[MetaAvailability]; got != AvailabilityOutOfStock {
		t.Errorf("res[1] availability = %q", got)
	}
}

func TestEtsyKeyOnlyHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer srv.Close()

	a := NewEtsy("key-abc", "", srv.URL, srv.Client())
	if _, err := a.Search(t.Context(), sources.Query{Text: "x"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotKey != "key-abc" {
		t.Fatalf("x-api-key = %q, want bare keystring", gotKey)
	}
}

func TestEtsyDisabledWithoutKey(t *testing.T) {
	a := NewEtsy("", "sec", "", nil)
	if a.Enabled() {
		t.Fatal("adapter without api key must be disabled")
	}
	if _, err := a.Search(t.Context(), sources.Query{Text: "x"}); err == nil {
		t.Fatal("Search on disabled adapter must error")
	}
}
