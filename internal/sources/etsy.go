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

	"github.com/anatolykoptev/go-engine/sources"
)

const (
	// etsyDefaultBaseURL is the Etsy Open API v3 host.
	etsyDefaultBaseURL = "https://openapi.etsy.com"
	// etsyActiveListingsPath is the public active-listing search endpoint.
	etsyActiveListingsPath = "/v3/application/listings/active"
	etsyDefaultLimit       = 25
	etsyMaxLimit           = 100 // Etsy v3 hard cap
)

// etsyAdapter searches Etsy via API v3. Authentication is the x-api-key
// header carrying the app keystring (optionally keystring:shared_secret —
// the form Etsy accepts for server-side API-key auth; the seller-side OAuth
// PKCE flow in etsy-forge is for write scopes and unnecessary for read-only
// listing search). Ships dark without ETSY_API_KEY.
type etsyAdapter struct {
	apiKey       string
	sharedSecret string
	baseURL      string
	http         *http.Client
}

// NewEtsy returns the Etsy API v3 adapter. Empty apiKey ships the adapter
// dark. baseURL/httpClient are injectable for tests; empty/nil selects the
// production endpoint and the default client.
func NewEtsy(apiKey, sharedSecret, baseURL string, httpClient *http.Client) Adapter {
	if baseURL == "" {
		baseURL = etsyDefaultBaseURL
	}
	if httpClient == nil {
		httpClient = defaultHTTP
	}
	return &etsyAdapter{
		apiKey:       apiKey,
		sharedSecret: sharedSecret,
		baseURL:      strings.TrimRight(baseURL, "/"),
		http:         httpClient,
	}
}

// Name implements sources.Source.
func (a *etsyAdapter) Name() string { return "etsy" }

// Spec implements Adapter.
func (a *etsyAdapter) Spec() SourceSpec { return SourceSpec{FetchClass: FetchClassAPI} }

// Enabled implements Adapter — the adapter ships dark without an API key.
func (a *etsyAdapter) Enabled() bool { return a.apiKey != "" }

// etsySearchResponse models GET /v3/application/listings/active.
type etsySearchResponse struct {
	Count   int           `json:"count"`
	Results []etsyListing `json:"results"`
}

type etsyListing struct {
	ListingID   int64  `json:"listing_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	State       string `json:"state"`
	Quantity    int    `json:"quantity"`
	IsVintage   bool   `json:"is_vintage"`
	ShopID      int64  `json:"shop_id"`
	Price       struct {
		Amount       int64  `json:"amount"`
		Divisor      int64  `json:"divisor"`
		CurrencyCode string `json:"currency_code"`
	} `json:"price"`
	Tags []string `json:"tags"`
}

// Search implements sources.Source against the Etsy v3 API.
func (a *etsyAdapter) Search(ctx context.Context, q sources.Query) ([]sources.Result, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("etsy: adapter disabled (missing ETSY_API_KEY)")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = etsyDefaultLimit
	}
	if limit > etsyMaxLimit {
		limit = etsyMaxLimit
	}
	u := a.baseURL + etsyActiveListingsPath +
		"?keywords=" + url.QueryEscape(q.Text) +
		"&limit=" + strconv.Itoa(limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("etsy: build request: %w", err)
	}
	req.Header.Set("x-api-key", a.apiKeyHeader())
	req.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("etsy: search request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("etsy: search status %d: %s", resp.StatusCode, readErrBody(resp.Body))
	}

	var body etsySearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("etsy: decode search response: %w", err)
	}

	out := make([]sources.Result, 0, len(body.Results))
	for _, it := range body.Results {
		out = append(out, etsyResult(it))
	}
	return out, nil
}

// etsyResult maps one active listing to a Result with typed metadata.
// Etsy's price is an integer amount/divisor pair — converted here once.
func etsyResult(it etsyListing) sources.Result {
	md := map[string]string{
		MetaListingID: strconv.FormatInt(it.ListingID, 10),
	}
	if it.Price.Divisor > 0 {
		md[MetaPrice] = strconv.FormatFloat(
			float64(it.Price.Amount)/float64(it.Price.Divisor), 'f', -1, 64)
		md[MetaCurrency] = it.Price.CurrencyCode
	}
	if it.IsVintage {
		md[MetaCondition] = "vintage"
	}
	if it.Quantity > 0 && it.State == "active" {
		md[MetaAvailability] = AvailabilityInStock
	} else {
		md[MetaAvailability] = AvailabilityOutOfStock
	}
	if it.ShopID != 0 {
		md[MetaSeller] = strconv.FormatInt(it.ShopID, 10)
	}
	return sources.Result{
		Title:    it.Title,
		URL:      it.URL,
		Content:  it.Title,
		Metadata: md,
	}
}

// apiKeyHeader builds the x-api-key value: the app keystring, extended to
// keystring:shared_secret when the secret is configured (Etsy accepts that
// form for API-key authentication).
func (a *etsyAdapter) apiKeyHeader() string {
	if a.sharedSecret != "" {
		return a.apiKey + ":" + a.sharedSecret
	}
	return a.apiKey
}
