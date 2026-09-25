package sources

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

// shopifyFixture mirrors a storefront's /products.json payload.
const shopifyFixture = `{
  "products": [
    {
      "id": 8123456789,
      "title": "Enamel Camp Mug — 12oz",
      "handle": "enamel-camp-mug-12oz",
      "vendor": "Campware",
      "product_type": "Mugs",
      "tags": ["mug", "enamel", "camping"],
      "images": [{"src": "https://cdn.shop.com/mug.jpg"}],
      "variants": [
        {"id": 1, "title": "Default", "price": "24.00", "compare_at_price": "32.00", "available": true, "sku": "MUG-12"},
        {"id": 2, "title": "XL", "price": "30.00", "compare_at_price": null, "available": false, "sku": "MUG-16"}
      ]
    },
    {
      "id": 8123456799,
      "title": "Titanium Spork",
      "handle": "titanium-spork",
      "vendor": "Campware",
      "product_type": "Cutlery",
      "tags": ["spork", "titanium"],
      "variants": [
        {"id": 3, "title": "Default", "price": "12.50", "compare_at_price": null, "available": true}
      ]
    }
  ]
}`

const shopifyFixtureShop2 = `{
  "products": [
    {
      "id": 55,
      "title": "Ceramic Pour-Over Mug",
      "handle": "ceramic-pour-over-mug",
      "vendor": "Kilnhouse",
      "variants": [
        {"id": 9, "title": "Default", "price": "38.00", "compare_at_price": "47.50", "available": true}
      ]
    }
  ]
}`

func newShopifyWowa(t *testing.T) (*httptest.Server, *wowa.Client, *[]string) {
	t.Helper()
	var gotURLs []string
	srv := newWowaFakeServer(t, map[string]string{
		"shop-one.example.com": shopifyFixture,
		"shop-two.example.com": shopifyFixtureShop2,
	}, &gotURLs)
	wc, err := wowa.NewClient(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("wowa client: %v", err)
	}
	return srv, wc, &gotURLs
}

func TestShopifyMultiShopSearch(t *testing.T) {
	srv, wc, gotURLs := newShopifyWowa(t)
	defer srv.Close()

	a := NewShopify(wc, []string{"https://shop-one.example.com/", "shop-two.example.com"})
	if !a.Enabled() {
		t.Fatal("adapter with fetcher+shops must be enabled")
	}
	res, err := a.Search(t.Context(), sources.Query{Text: "mug"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(*gotURLs) != 2 {
		t.Fatalf("expected 2 shop fetches, got %v", *gotURLs)
	}
	// "mug" must filter out the titanium spork.
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2 mugs", len(res))
	}

	first := res[0]
	if first.URL != "https://shop-one.example.com/products/enamel-camp-mug-12oz" {
		t.Errorf("url = %q", first.URL)
	}
	if got := first.Metadata[MetaPrice]; got != "24" {
		t.Errorf("price = %q (lowest in-stock variant)", got)
	}
	if got := first.Metadata[MetaDiscountPct]; got != "25.0" {
		t.Errorf("discount_pct = %q ((32-24)/32)", got)
	}
	if got := first.Metadata[MetaSeller]; got != "shop-one.example.com" {
		t.Errorf("seller = %q", got)
	}
	if got := first.Metadata[MetaMerchant]; got != "Campware" {
		t.Errorf("merchant = %q", got)
	}
	if got := first.Metadata[MetaAvailability]; got != AvailabilityInStock {
		t.Errorf("availability = %q", got)
	}
	if got := first.Metadata[MetaImageURL]; got != "https://cdn.shop.com/mug.jpg" {
		t.Errorf("image_url = %q", got)
	}
}

func TestShopifyQueryFilter(t *testing.T) {
	srv, wc, _ := newShopifyWowa(t)
	defer srv.Close()

	a := NewShopify(wc, []string{"shop-one.example.com"})
	res, err := a.Search(t.Context(), sources.Query{Text: "spork"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || !strings.Contains(res[0].URL, "titanium-spork") {
		t.Fatalf("spork filter got %v", res)
	}
}

func TestShopifyDisabled(t *testing.T) {
	if NewShopify(nil, []string{"x.example.com"}).Enabled() {
		t.Fatal("no fetcher must disable")
	}
	if NewShopify(stubFetcher{}, nil).Enabled() {
		t.Fatal("no shops must disable")
	}
	if _, err := NewShopify(nil, nil).Search(t.Context(), sources.Query{Text: "x"}); err == nil {
		t.Fatal("Search on disabled adapter must error")
	}
}

func TestShopifyPartialFailure(t *testing.T) {
	var gotURLs []string
	srv := newWowaFakeServer(t, map[string]string{
		"good.example.com": shopifyFixture,
		// bad.example.com intentionally absent → 404 upstream
	}, &gotURLs)
	defer srv.Close()
	wc, _ := wowa.NewClient(srv.URL)

	a := NewShopify(wc, []string{"bad.example.com", "good.example.com"})
	res, err := a.Search(t.Context(), sources.Query{Text: ""})
	if err != nil {
		t.Fatalf("one failing shop must not error the adapter: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2 from good shop", len(res))
	}
}
