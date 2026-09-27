package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go-engine/sources"
)

const (
	// ebayDefaultBaseURL is the eBay production API host. Both the OAuth
	// token endpoint and the Browse API hang off it.
	ebayDefaultBaseURL = "https://api.ebay.com"
	// ebayTokenPath is eBay's OAuth2 client-credentials endpoint.
	ebayTokenPath = "/identity/v1/oauth2/token"
	// ebaySearchPath is the Browse API item_summary search endpoint.
	ebaySearchPath = "/buy/browse/v1/item_summary/search"
	// ebayScope is the OAuth scope the Browse API requires.
	ebayScope = "https://api.ebay.com/oauth/api_scope/buy.item.browse"
	// ebayDefaultMarketplace is sent as X-EBAY-C-MARKETPLACE-ID.
	ebayDefaultMarketplace = "EBAY_US"
	// ebayDefaultLimit / ebayMaxLimit bound the per-request page size.
	ebayDefaultLimit = 20
	ebayMaxLimit     = 50
	// ebayDailyCallCap is the Browse API free-tier allowance (5k calls/day).
	// The adapter refuses searches past the cap rather than burning the
	// upstream quota on 429s.
	ebayDailyCallCap = 5000
)

// ebayAdapter searches eBay via the Browse API (OAuth2 client credentials).
// It ships dark: without EBAY_CLIENT_ID/EBAY_CLIENT_SECRET it is registered
// but Enabled() reports false and the funnel skips it.
type ebayAdapter struct {
	clientID     string
	clientSecret string
	baseURL      string
	marketplace  string
	http         *http.Client
	dailyCap     int64

	mu       sync.Mutex
	token    string
	tokenExp time.Time

	quotaMu   sync.Mutex
	quotaDay  string
	quotaUsed int64
}

// ebayOption mutates an ebayAdapter — unexported; tests use it in-package.
type ebayOption func(*ebayAdapter)

// withEbayDailyCap overrides the free-tier daily call cap (tests).
func withEbayDailyCap(n int64) ebayOption {
	return func(a *ebayAdapter) { a.dailyCap = n }
}

// NewEBay returns the eBay Browse API adapter. Empty credentials ship the
// adapter dark. baseURL/httpClient are injectable for tests; empty/nil
// selects the production endpoint and the default client.
func NewEBay(clientID, clientSecret, baseURL string, httpClient *http.Client, opts ...ebayOption) Adapter {
	if baseURL == "" {
		baseURL = ebayDefaultBaseURL
	}
	if httpClient == nil {
		httpClient = defaultHTTP
	}
	a := &ebayAdapter{
		clientID:     clientID,
		clientSecret: clientSecret,
		baseURL:      strings.TrimRight(baseURL, "/"),
		marketplace:  ebayDefaultMarketplace,
		http:         httpClient,
		dailyCap:     ebayDailyCallCap,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Name implements sources.Source.
func (a *ebayAdapter) Name() string { return "ebay" }

// Spec implements Adapter.
func (a *ebayAdapter) Spec() SourceSpec {
	return SourceSpec{
		FetchClass: FetchClassAPI,
		Manifest: Manifest{
			ID:           a.Name(),
			AllowedHosts: []string{"ebay.com", "*.ebay.com"},
		},
	}
}

// Enabled implements Adapter — the adapter ships dark without credentials.
func (a *ebayAdapter) Enabled() bool {
	return a.clientID != "" && a.clientSecret != ""
}

// ebayTokenResponse is the subset of eBay's OAuth token payload we use.
type ebayTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// ebaySearchResponse models GET /buy/browse/v1/item_summary/search.
type ebaySearchResponse struct {
	Total         int        `json:"total"`
	ItemSummaries []ebayItem `json:"itemSummaries"`
}

type ebayItem struct {
	ItemID     string `json:"itemId"`
	Title      string `json:"title"`
	ItemWebURL string `json:"itemWebUrl"`
	Price      struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"price"`
	Condition string `json:"condition"`
	Seller    struct {
		Username string `json:"username"`
	} `json:"seller"`
	BuyingOptions []string `json:"buyingOptions"`
	Image         struct {
		ImageURL string `json:"imageUrl"`
	} `json:"image"`
}

// Search implements sources.Source against the Browse API.
func (a *ebayAdapter) Search(ctx context.Context, q sources.Query) ([]sources.Result, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("ebay: adapter disabled (missing EBAY_CLIENT_ID/EBAY_CLIENT_SECRET)")
	}
	if err := a.consumeQuota(); err != nil {
		return nil, err
	}
	token, err := a.accessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("ebay: token: %w", err)
	}

	resp, err := a.doSearch(ctx, q, token)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ebay: search status %d: %s", resp.StatusCode, readErrBody(resp.Body))
	}

	var body ebaySearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("ebay: decode search response: %w", err)
	}

	out := make([]sources.Result, 0, len(body.ItemSummaries))
	for _, it := range body.ItemSummaries {
		out = append(out, ebayResult(it))
	}
	return out, nil
}

// doSearch issues the authorized Browse API search request.
func (a *ebayAdapter) doSearch(ctx context.Context, q sources.Query, token string) (*http.Response, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = ebayDefaultLimit
	}
	if limit > ebayMaxLimit {
		limit = ebayMaxLimit
	}
	u := a.baseURL + ebaySearchPath + "?q=" + url.QueryEscape(q.Text) +
		"&limit=" + strconv.Itoa(limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("ebay: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-EBAY-C-MARKETPLACE-ID", a.marketplace)
	req.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ebay: search request: %w", err)
	}
	return resp, nil
}

// ebayResult maps one item_summary entry to a Result with typed metadata.
func ebayResult(it ebayItem) sources.Result {
	md := map[string]string{
		MetaListingID:    it.ItemID,
		MetaAvailability: AvailabilityInStock, // Browse results are live buyable listings
	}
	if it.Price.Value != "" {
		md[MetaPrice] = it.Price.Value
		md[MetaCurrency] = it.Price.Currency
	}
	if it.Condition != "" {
		md[MetaCondition] = it.Condition
	}
	if it.Seller.Username != "" {
		md[MetaSeller] = it.Seller.Username
	}
	if len(it.BuyingOptions) > 0 {
		md[MetaBuyingOptions] = strings.Join(it.BuyingOptions, ",")
	}
	if it.Image.ImageURL != "" {
		md[MetaImageURL] = it.Image.ImageURL
	}
	return sources.Result{
		Title:    it.Title,
		URL:      it.ItemWebURL,
		Content:  it.Title, // dedup signal: same product ≈ same title
		Metadata: md,
	}
}

// accessToken returns a cached OAuth token, fetching a fresh one when the
// cached token is missing or within a minute of expiry.
func (a *ebayAdapter) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.tokenExp.Add(-time.Minute)) {
		return a.token, nil
	}

	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {ebayScope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+ebayTokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.SetBasicAuth(a.clientID, a.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token status %d: %s", resp.StatusCode, readErrBody(resp.Body))
	}

	var tok ebayTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("token response carried no access_token")
	}
	exp := tok.ExpiresIn
	if exp <= 0 {
		exp = 3600 // eBay tokens are 2h; conservative fallback
	}
	a.token = tok.AccessToken
	a.tokenExp = time.Now().Add(time.Duration(exp) * time.Second)
	return a.token, nil
}

// consumeQuota counts one call against the free-tier daily cap (UTC day
// boundary). Past the cap it returns an error instead of hitting the API —
// a refused call is cheaper than a burned one plus a 429.
func (a *ebayAdapter) consumeQuota() error {
	a.quotaMu.Lock()
	defer a.quotaMu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if a.quotaDay != today {
		a.quotaDay = today
		a.quotaUsed = 0
	}
	if a.quotaUsed >= a.dailyCap {
		return fmt.Errorf("ebay: daily call cap %d reached", a.dailyCap)
	}
	a.quotaUsed++
	return nil
}

// readErrBody returns a bounded prefix of an error response body for logs.
func readErrBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}
