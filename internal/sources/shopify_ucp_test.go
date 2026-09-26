package sources

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

// ucpGlobalFixture mirrors catalog.shopify.com search_catalog output —
// seller identity and variant URL ride on each variant; variant URLs carry
// utm_* + _gsid trackers the adapter must strip.
const ucpGlobalFixture = `{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "structuredContent": {
      "products": [
        {
          "id": "gid://shopify/p/abc123",
          "title": "JBL Charge 6 Portable Bluetooth Speaker (Black)",
          "description": {"plain": "A rugged, waterproof portable speaker."},
          "metadata": {"top_features": ["IP68 waterproof", "28h playtime"]},
          "variants": [
            {
              "id": "gid://shopify/ProductVariant/111",
              "url": "https://worldwidestereo.com/products/jbl-charge-6?variant=503&utm_source=shopify&utm_medium=catalog&_gsid=abc",
              "price": {"amount": 19995, "currency": "USD"},
              "availability": {"available": true},
              "media": [{"type": "image", "url": "https://cdn.shop.com/charge6.jpg"}],
              "seller": {"id": "gid://shopify/Shop/7", "name": "World Wide Stereo", "domain": "worldwidestereo.com"}
            }
          ]
        },
        {
          "id": "gid://shopify/p/def456",
          "title": "No URL product",
          "variants": []
        },
        {
          "id": "gid://shopify/p/jpy789",
          "title": "Yen gadget",
          "variants": [
            {
              "url": "https://shop.jp/products/yen",
              "price": {"amount": 4800, "currency": "JPY"},
              "availability": {"available": false},
              "seller": {"name": "JP Shop", "domain": "shop.jp"}
            }
          ]
        }
      ]
    }
  }
}`

// ucpShopFixture mirrors a storefront's own /api/ucp/mcp search_catalog —
// URL may sit on the product, seller block is absent (merchant = shop).
const ucpShopFixture = `{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "structuredContent": {
      "products": [
        {
          "id": "gid://shopify/Product/42",
          "title": "JBL Go 4 Portable Bluetooth Waterproof Speaker",
          "url": "https://shop-one.example.com/products/jbl-go-4",
          "price_range": {"min": {"amount": 4995, "currency": "USD"}},
          "variants": [{"id": "gid://shopify/ProductVariant/42v", "availability": {"available": true}}]
        }
      ]
    }
  }
}`

const ucpRPCErrorFixture = `{"jsonrpc":"2.0","id":3,"error":{"code":-32001,"message":"UCP discovery failed"}}`

func TestShopifyGlobalCatalogSearch(t *testing.T) {
	srv := newWowaFakeServer(t, map[string]string{
		"api/ucp/mcp": ucpGlobalFixture,
	}, nil)
	defer srv.Close()
	wc, err := wowa.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("wowa client: %v", err)
	}

	a := NewShopify(wc, ShopifyConfig{
		ProfileURL: "https://agent.example/ucp.json",
		GlobalURL:  srv.URL + "/api/ucp/mcp",
	})
	if !a.Enabled() {
		t.Fatal("adapter with fetcher+profile but no shops must be enabled")
	}
	res, err := a.Search(t.Context(), sources.Query{Text: "JBL"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 mappable products (one lacks URL), got %d", len(res))
	}
	assertGlobalSellerResult(t, res)
	assertZeroDecimalResult(t, res)
}

// assertGlobalSellerResult checks the seller-carrying global-catalog row.
func assertGlobalSellerResult(t *testing.T, res []sources.Result) {
	t.Helper()
	var got *sources.Result
	for i := range res {
		if res[i].Metadata[MetaSeller] == "worldwidestereo.com" {
			got = &res[i]
		}
	}
	if got == nil {
		t.Fatalf("missing worldwidestereo result: %+v", res)
	}
	checks := map[string]string{
		MetaPrice:        "199.95",
		MetaCurrency:     "USD",
		MetaAvailability: AvailabilityInStock,
		MetaMerchant:     "World Wide Stereo",
		MetaImageURL:     "https://cdn.shop.com/charge6.jpg",
	}
	for k, want := range checks {
		if got.Metadata[k] != want {
			t.Fatalf("%s: got %q want %q", k, got.Metadata[k], want)
		}
	}
	if strings.Contains(got.URL, "utm_") || strings.Contains(got.URL, "_gsid") {
		t.Fatalf("tracker params not stripped: %s", got.URL)
	}
	if !strings.Contains(got.URL, "variant=503") {
		t.Fatalf("functional params must survive cleaning: %s", got.URL)
	}
	if got.Content != "A rugged, waterproof portable speaker." {
		t.Fatalf("content should carry the plain description for fit judging: %q", got.Content)
	}
}

// assertZeroDecimalResult checks the JPY row — minor unit IS major.
func assertZeroDecimalResult(t *testing.T, res []sources.Result) {
	t.Helper()
	var got *sources.Result
	for i := range res {
		if res[i].Metadata[MetaSeller] == "shop.jp" {
			got = &res[i]
		}
	}
	if got == nil {
		t.Fatalf("missing JPY result: %+v", res)
	}
	if got.Metadata[MetaPrice] != "4800" {
		t.Fatalf("JPY is zero-decimal: amount is already major, got %q", got.Metadata[MetaPrice])
	}
	if got.Metadata[MetaAvailability] != AvailabilityOutOfStock {
		t.Fatalf("availability: got %q", got.Metadata[MetaAvailability])
	}
}

func TestShopifyPerShopUCP(t *testing.T) {
	var gotURLs []string
	srv := newWowaFakeServer(t, map[string]string{
		"shop-one.example.com/api/ucp/mcp": ucpShopFixture,
	}, &gotURLs)
	defer srv.Close()
	wc, err := wowa.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("wowa client: %v", err)
	}

	a := NewShopify(wc, ShopifyConfig{
		Shops:      []string{"shop-one.example.com"},
		ProfileURL: "https://agent.example/ucp.json",
		GlobalOff:  true,
	})
	res, err := a.Search(t.Context(), sources.Query{Text: "JBL"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	if res[0].Metadata[MetaSeller] != "shop-one.example.com" {
		t.Fatalf("seller should fall back to shop host: %q", res[0].Metadata[MetaSeller])
	}
	if res[0].Metadata[MetaPrice] != "49.95" {
		t.Fatalf("price_range.min fallback: got %q", res[0].Metadata[MetaPrice])
	}
	for _, u := range gotURLs {
		if strings.Contains(u, "products.json") {
			t.Fatalf("products.json must not be fetched when UCP succeeds: %v", gotURLs)
		}
	}
}

func TestShopifyUCPFallsBackToProductsJSON(t *testing.T) {
	var gotURLs []string
	srv := newWowaFakeServer(t, map[string]string{
		"api/ucp/mcp":   ucpRPCErrorFixture,
		"products.json": shopifyFixture,
	}, &gotURLs)
	defer srv.Close()
	wc, err := wowa.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("wowa client: %v", err)
	}

	a := NewShopify(wc, ShopifyConfig{
		Shops:      []string{"shop-one.example.com"},
		ProfileURL: "https://agent.example/ucp.json",
		GlobalURL:  srv.URL + "/api/ucp/mcp",
	})
	res, err := a.Search(t.Context(), sources.Query{Text: "mug"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected the 1 mug product after UCP fallback, got %d", len(res))
	}
}

func TestShopifyUCPDarkWithoutProfile(t *testing.T) {
	var gotURLs []string
	srv := newWowaFakeServer(t, map[string]string{
		"products.json": shopifyFixture,
	}, &gotURLs)
	defer srv.Close()
	wc, err := wowa.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("wowa client: %v", err)
	}

	a := NewShopify(wc, ShopifyConfig{Shops: []string{"shop-one.example.com"}})
	res, err := a.Search(t.Context(), sources.Query{Text: "mug"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("products.json path unchanged without profile, got %d", len(res))
	}
	for _, u := range gotURLs {
		if strings.Contains(u, "api/ucp/mcp") {
			t.Fatalf("UCP must stay dark without a profile: %v", gotURLs)
		}
	}
}
