package sources

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

// slickdealsRSSFixture mirrors the frontpage RSS shape: price, merchant and
// thumbs arrive inside the description HTML, not as RSS elements.
const slickdealsRSSFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Slickdeals Frontpage</title>
    <item>
      <title>Sony WH-1000XM5 Wireless Noise-Canceling Headphones $278 + Free Shipping</title>
      <link>https://slickdeals.net/f/18273645-sony-wh-1000xm5</link>
      <description><![CDATA[<img src="https://static.slickdeals.net/x.jpg"/><br/>
        Price: $278.00<br/>Merchant: Amazon<br/>Thumbs: 142<br/>Posted 2 hours ago]]></description>
      <pubDate>Tue, 23 Sep 2026 10:00:00 -0700</pubDate>
    </item>
    <item>
      <title>75" Hisense U8 Mini-LED 4K TV $999.99</title>
      <link>https://slickdeals.net/f/18274001-hisense-u8</link>
      <description><![CDATA[Best Buy has 75" Hisense U8 for $999.99. 38 thumbs up.]]></description>
      <pubDate>Tue, 23 Sep 2026 09:30:00 -0700</pubDate>
    </item>
  </channel>
</rss>`

// newWowaFakeServer emulates go-wowa's /api/v1/fetch: it decodes the
// FetchRequest and answers with the canned body for the requested URL.
func newWowaFakeServer(t *testing.T, bodies map[string]string, gotURLs *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fetch" {
			http.NotFound(w, r)
			return
		}
		var req wowa.FetchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad fetch request", http.StatusBadRequest)
			return
		}
		if gotURLs != nil {
			*gotURLs = append(*gotURLs, req.URL)
		}
		body, ok := bodies[req.URL]
		if !ok {
			// Loose match: tests may key fixtures by host substring.
			for k, v := range bodies {
				if strings.Contains(req.URL, k) {
					body = v
					ok = true
					break
				}
			}
		}
		if !ok {
			_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: http.StatusNotFound, Body: ""})
			return
		}
		_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: http.StatusOK, Body: body})
	}))
}

func TestSlickdealsParsesRSS(t *testing.T) {
	var gotURLs []string
	srv := newWowaFakeServer(t, map[string]string{
		"slickdeals.net/newsearch.php": slickdealsRSSFixture,
	}, &gotURLs)
	defer srv.Close()

	wc, err := wowa.NewClient(srv.URL)
	if err != nil {
		t.Fatalf("wowa client: %v", err)
	}
	a := NewSlickdeals(wc, "")
	if !a.Enabled() {
		t.Fatal("adapter with fetcher must be enabled")
	}
	res, err := a.Search(t.Context(), sources.Query{Text: "headphones"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}
	if len(gotURLs) != 1 || !strings.Contains(gotURLs[0], "q=headphones") || !strings.Contains(gotURLs[0], "rss=1") {
		t.Fatalf("feed URL = %v", gotURLs)
	}

	first := res[0]
	if first.URL != "https://slickdeals.net/f/18273645-sony-wh-1000xm5" {
		t.Errorf("url = %q", first.URL)
	}
	if got := first.Metadata[MetaPrice]; got != "278.00" {
		t.Errorf("price = %q", got)
	}
	if got := first.Metadata[MetaThumbs]; got != "142" {
		t.Errorf("thumbs = %q", got)
	}
	if got := first.Metadata[MetaMerchant]; got != "Amazon" {
		t.Errorf("merchant = %q", got)
	}
	// Second item uses the "38 thumbs up" phrasing.
	if got := res[1].Metadata[MetaThumbs]; got != "38" {
		t.Errorf("res[1] thumbs = %q", got)
	}
	if got := res[1].Metadata[MetaPrice]; got != "999.99" {
		t.Errorf("res[1] price = %q", got)
	}
}

func TestSlickdealsFrontpageURL(t *testing.T) {
	var gotURLs []string
	srv := newWowaFakeServer(t, map[string]string{
		"slickdeals.net": slickdealsRSSFixture,
	}, &gotURLs)
	defer srv.Close()

	wc, _ := wowa.NewClient(srv.URL)
	a := NewSlickdeals(wc, "")
	if _, err := a.Search(t.Context(), sources.Query{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(gotURLs) != 1 || !strings.Contains(gotURLs[0], "mode=frontpage") {
		t.Fatalf("empty query must hit frontpage mode, got %v", gotURLs)
	}
}

func TestSlickdealsDisabledWithoutFetcher(t *testing.T) {
	a := NewSlickdeals(nil, "")
	if a.Enabled() {
		t.Fatal("adapter without fetcher must be disabled")
	}
	if _, err := a.Search(t.Context(), sources.Query{Text: "x"}); err == nil {
		t.Fatal("Search on disabled adapter must error")
	}
}

func TestSlickdealsUpstreamError(t *testing.T) {
	srv := newWowaFakeServer(t, map[string]string{}, nil)
	defer srv.Close()

	wc, _ := wowa.NewClient(srv.URL)
	a := NewSlickdeals(wc, "")
	if _, err := a.Search(t.Context(), sources.Query{Text: "x"}); err == nil {
		t.Fatal("upstream 404 must produce an error")
	}
}
