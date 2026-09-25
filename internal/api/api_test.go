package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/config"
	"github.com/anatolykoptev/go-product-search/internal/rank"
	"github.com/anatolykoptev/go-product-search/internal/search"
	pssources "github.com/anatolykoptev/go-product-search/internal/sources"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clearSourceEnv pins every adapter credential env var to empty so tests
// are hermetic regardless of the host's real environment.
func clearSourceEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"EBAY_CLIENT_ID", "EBAY_CLIENT_SECRET",
		"ETSY_API_KEY", "ETSY_SHARED_SECRET",
		"SHOPIFY_SHOPS", "INTERNAL_SERVICE_SECRET",
		"REDIS_URL",
	} {
		t.Setenv(k, "")
	}
}

// feedXML is a slickdeals-shaped RSS fixture: two items carrying Price and
// Thumbs markers (the thumbs feed the ADR-17 deal signal). Item links are
// TEST-NET literal IPs so the funnel SSRF guard passes without DNS.
const feedXML = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel><title>Slickdeals</title>
    <item>
      <title>Sony XM5 Headphones — hot deal</title>
      <link>http://203.0.113.60/item-a</link>
      <description><![CDATA[Price: $278.00<br/>Merchant: SecretSellerA<br/>Thumbs: 210]]></description>
    </item>
    <item>
      <title>Budget Earbuds deal</title>
      <link>http://203.0.113.60/item-b</link>
      <description><![CDATA[Price: $29.99<br/>Merchant: SecretSellerB<br/>Thumbs: 12]]></description>
    </item>
  </channel>
</rss>`

// detailPage renders the schema.org JSON-LD product page the extraction
// stage fetches for a feed item (SERP rows lack currency, so the detail
// tier always runs).
func detailPage(name, url string, price float64) string {
	return fmt.Sprintf(`<!doctype html><html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product",
 "name":%q, "url":%q,
 "offers":{"@type":"Offer","price":"%.2f","priceCurrency":"USD",
   "availability":"https://schema.org/InStock"},
 "aggregateRating":{"@type":"AggregateRating","ratingValue":"4.6"}}
</script></head><body>%s</body></html>`, name, url, price, name)
}

// wowaStub fakes go-wowa /api/v1/fetch: item URLs answer their schema.org
// detail page, anything else the RSS feed.
func wowaStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fetch" {
			http.NotFound(w, r)
			return
		}
		var req wowa.FetchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var body string
		switch {
		case strings.Contains(req.URL, "item-a"):
			body = detailPage("Sony XM5 Headphones", req.URL, 278.00)
		case strings.Contains(req.URL, "item-b"):
			body = detailPage("Budget Earbuds", req.URL, 29.99)
		default:
			body = feedXML
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wowa.FetchResponse{Status: 200, Body: body})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// jeffStub fakes jeff /v1/systemone: every packed question answers with a
// noul keyed on the candidate state's product name.
func jeffStub(t *testing.T, probs map[string]float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State struct {
				Name string `json:"name"`
			} `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		answers := make(map[string]any, len(req.Questions))
		for id := range req.Questions {
			answers[id] = map[string]any{"type": "noul", "noul": probs[req.State.Name]}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testDeps(t *testing.T, cfg config.Config) deps {
	t.Helper()
	s, err := search.New(cfg)
	if err != nil {
		t.Fatalf("search.New: %v", err)
	}
	return deps{
		searcher: s,
		weights:  rank.Weights{Funnel: 0.3, Deal: 0.2, Jeff: 0.5},
		passMin:  0.55,
	}
}

func decodeResult(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res == nil || res.IsError {
		t.Fatalf("tool error result: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("non-text content: %+v", res.Content[0])
	}
	if err := json.Unmarshal([]byte(tc.Text), v); err != nil {
		t.Fatalf("decode %q: %v", tc.Text, err)
	}
}

// TestProductSearchE2E drives the whole chain — funnel → extract → jeff
// match → rank fusion → public projection — against stubbed wowa and jeff
// services.
func TestProductSearchE2E(t *testing.T) {
	clearSourceEnv(t)
	wowaSrv := wowaStub(t)
	// Search-path names are the SERP titles (extraction keeps the title);
	// the product_match path's name comes from the schema.org detail page.
	jeffSrv := jeffStub(t, map[string]float64{
		"Sony XM5 Headphones — hot deal": 0.9,
		"Sony XM5 Headphones":            0.9,
		"Budget Earbuds deal":            0.3,
	})
	d := testDeps(t, config.Config{
		WowaURL: wowaSrv.URL, JeffURL: jeffSrv.URL, JeffToken: "t",
	})

	res, err := handleProductSearch(t.Context(), d, productSearchInput{
		Query:    "headphones",
		Criteria: []string{"good sound"},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out searchOutput
	decodeResult(t, res, &out)

	if len(out.Results) != 2 {
		t.Fatalf("results = %+v", out.Results)
	}
	assertTopResult(t, out.Results[0])
	assertSecondResult(t, out.Results[1])
	if out.Degraded {
		t.Fatalf("clean run marked degraded: %s", out.DegradeReason)
	}
	assertSlickdealsOK(t, out.Sources)
}

// assertTopResult checks the winning product's fused score, criterion
// explanation and deal signals.
func assertTopResult(t *testing.T, top productResult) {
	t.Helper()
	if !strings.Contains(top.URL, "item-a") {
		t.Fatalf("top result = %+v", top)
	}
	if !top.Passed || top.Score <= 0 || top.Confidence == "" {
		t.Fatalf("top = %+v", top)
	}
	if len(top.MatchedCriteria) != 1 ||
		top.MatchedCriteria[0].Criterion != "good sound" ||
		top.MatchedCriteria[0].Prob != 0.9 || !top.MatchedCriteria[0].Pass {
		t.Fatalf("matched_criteria = %+v", top.MatchedCriteria)
	}
	if top.DealSignals == nil || top.DealSignals.Thumbs == nil || *top.DealSignals.Thumbs != 210 {
		t.Fatalf("deal_signals = %+v", top.DealSignals)
	}
}

// assertSecondResult checks the verdict-failing product: still returned,
// passed=false, its criterion verdict present but under threshold.
func assertSecondResult(t *testing.T, second productResult) {
	t.Helper()
	if second.Passed || len(second.MatchedCriteria) != 1 || second.MatchedCriteria[0].Pass {
		t.Fatalf("second = %+v", second)
	}
}

// assertSlickdealsOK requires the one enabled adapter to report ok.
func assertSlickdealsOK(t *testing.T, sources []pssources.SourceStatus) {
	t.Helper()
	for _, st := range sources {
		if st.Name == "slickdeals" && st.Outcome == "ok" {
			return
		}
	}
	t.Fatalf("sources = %+v", sources)
}

// TestProductSearchDegraded — jeff down (unconfigured) must surface the
// degrade flag while results still rank on features alone.
func TestProductSearchDegraded(t *testing.T) {
	clearSourceEnv(t)
	wowaSrv := wowaStub(t)
	d := testDeps(t, config.Config{WowaURL: wowaSrv.URL}) // no JeffURL

	res, err := handleProductSearch(t.Context(), d, productSearchInput{
		Query:    "headphones",
		Criteria: []string{"good sound"},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out searchOutput
	decodeResult(t, res, &out)
	if !out.Degraded || out.DegradeReason == "" {
		t.Fatalf("degraded surface missing: %+v", out)
	}
	if len(out.Results) == 0 {
		t.Fatal("degraded run returned no results — feature ranking must still answer")
	}
	for _, r := range out.Results {
		if len(r.MatchedCriteria) != 0 {
			t.Fatalf("unjudged result gained criteria: %+v", r.MatchedCriteria)
		}
		if r.UnjudgedReason == "" {
			t.Fatalf("unjudged_reason missing: %+v", r)
		}
	}
	// Feature-only ordering: the high-thumbs Sony still leads.
	if !strings.Contains(out.Results[0].URL, "item-a") {
		t.Fatalf("feature-only order = %+v", out.Results)
	}
}

// TestPublicSchemaNoSellerFields — the egress boundary, asserted on the
// marshaled wire shape: no seller-identifying keys or merchant text from
// Candidate.Metadata may appear anywhere in the tool output.
func TestPublicSchemaNoSellerFields(t *testing.T) {
	clearSourceEnv(t)
	wowaSrv := wowaStub(t)
	jeffSrv := jeffStub(t, map[string]float64{"Sony XM5 Headphones — hot deal": 0.9})
	d := testDeps(t, config.Config{WowaURL: wowaSrv.URL, JeffURL: jeffSrv.URL, JeffToken: "t"})

	res, err := handleProductSearch(t.Context(), d, productSearchInput{Query: "headphones"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tc := res.Content[0].(*mcp.TextContent)

	// Merchant names live in Candidate.Metadata — they must never leak.
	for _, leaked := range []string{"SecretSellerA", "SecretSellerB"} {
		if strings.Contains(tc.Text, leaked) {
			t.Fatalf("seller-identifying text %q leaked into output", leaked)
		}
	}

	// And no banned KEY anywhere in the JSON tree.
	var decoded any
	if err := json.Unmarshal([]byte(tc.Text), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertNoBannedKeys(t, decoded)
}

// assertNoBannedKeys walks a decoded JSON tree and fails on any key that
// would expose internal or seller-identifying state.
func assertNoBannedKeys(t *testing.T, v any) {
	t.Helper()
	banned := map[string]bool{
		"seller": true, "seller_name": true, "merchant": true,
		"raw": true, "metadata": true, "content": true,
	}
	var walk func(any)
	walk = func(n any) {
		switch node := n.(type) {
		case map[string]any:
			for k, sub := range node {
				if banned[k] {
					t.Errorf("banned key %q in tool output", k)
				}
				walk(sub)
			}
		case []any:
			for _, sub := range node {
				walk(sub)
			}
		}
	}
	walk(v)
}

// TestProductMatchE2E — the single-URL path: SSRF-clean URL → fetch +
// extract + match on one candidate, same public projection back.
func TestProductMatchE2E(t *testing.T) {
	clearSourceEnv(t)
	wowaSrv := wowaStub(t)
	jeffSrv := jeffStub(t, map[string]float64{"Sony XM5 Headphones": 0.9})
	d := testDeps(t, config.Config{
		WowaURL: wowaSrv.URL, JeffURL: jeffSrv.URL, JeffToken: "t",
	})

	res, err := handleProductMatch(t.Context(), d, productMatchInput{
		ProductURL: "http://203.0.113.60/item-a",
		Criteria:   []string{"good sound"},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out matchOutput
	decodeResult(t, res, &out)
	if !strings.Contains(out.Result.URL, "item-a") || !out.Result.Passed {
		t.Fatalf("result = %+v", out.Result)
	}
	if len(out.Result.MatchedCriteria) != 1 || out.Result.MatchedCriteria[0].Prob != 0.9 {
		t.Fatalf("matched_criteria = %+v", out.Result.MatchedCriteria)
	}
}

// TestProductMatchGuards — a private-range URL dies at the SSRF screen and
// a malformed criterion dies at planning, both as loud tool errors.
func TestProductMatchGuards(t *testing.T) {
	clearSourceEnv(t)
	d := testDeps(t, config.Config{WowaURL: "http://127.0.0.1:1"})

	res, err := handleProductMatch(t.Context(), d, productMatchInput{
		ProductURL: "http://127.0.0.1:9/internal",
	})
	if err != nil || !res.IsError {
		t.Fatalf("loopback URL not rejected: res=%+v err=%v", res, err)
	}
	res, err = handleProductMatch(t.Context(), d, productMatchInput{
		ProductURL: "http://203.0.113.60/item-a",
		Criteria:   []string{"price_max:abc"},
	})
	if err != nil || !res.IsError {
		t.Fatalf("malformed criterion not rejected: res=%+v err=%v", res, err)
	}
}

// TestInitErrorStubs — when the pipeline fails to build the tools still
// exist and return the stored error instead of vanishing.
func TestInitErrorStubs(t *testing.T) {
	initErr := errors.New("wowa unreachable at startup")
	d := deps{initErr: initErr}

	res, err := handleProductSearch(t.Context(), d, productSearchInput{Query: "x"})
	if err != nil || !res.IsError {
		t.Fatalf("search stub: res=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "wowa unreachable") {
		t.Fatalf("stored error not surfaced: %+v", res)
	}
	res, err = handleProductMatch(t.Context(), d, productMatchInput{ProductURL: "http://x.example/1"})
	if err != nil || !res.IsError {
		t.Fatalf("match stub: res=%+v err=%v", res, err)
	}
}

// TestRegisterToolsSmoke — registration itself must not panic, either on a
// live searcher or on the init-error stub path.
func TestRegisterToolsSmoke(t *testing.T) {
	clearSourceEnv(t)
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	RegisterTools(srv, nil, config.Config{}, errors.New("pipeline down"))
}

// TestClampMaxResults bounds.
func TestClampMaxResults(t *testing.T) {
	for in, want := range map[int]int{0: defaultMaxResults, -3: defaultMaxResults, 7: 7, 500: maxResultsCap} {
		if got := clampMaxResults(in); got != want {
			t.Fatalf("clamp(%d) = %d, want %d", in, got, want)
		}
	}
}
