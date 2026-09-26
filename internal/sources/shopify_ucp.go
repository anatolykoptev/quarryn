package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

// Shopify's Universal Commerce Protocol (UCP) exposes catalog search as
// MCP-over-HTTP: every storefront answers search_catalog at
// https://<shop>/api/ucp/mcp and Shopify hosts a global cross-store catalog
// at catalog.shopify.com/api/ucp/mcp. Every call must carry
// meta["ucp-agent"].profile — a publicly fetchable, CACHEABLE JSON document
// declaring the agent's capabilities (Shopify rejects profiles served
// without a valid Cache-Control).
//
// The leg is dark until SHOPIFY_UCP_PROFILE_URL points at our hosted
// profile; per-shop UCP failures fall back to the products.json scrape.

const (
	// ucpGlobalCatalogURL is Shopify's Global Catalog MCP endpoint (GA
	// 2026, anonymous tier needs no auth — just the agent profile).
	ucpGlobalCatalogURL = "https://catalog.shopify.com/api/ucp/mcp"
	// ucpGlobalLimit is the global catalog's documented max page size.
	ucpGlobalLimit = 50
	// ucpShopLimit caps per-store catalog results.
	ucpShopLimit = 50
	// ucpRPCTimeoutSecs bounds one MCP call through wowa fetch.
	ucpRPCTimeoutSecs = 30
)

// zeroDecimalCurrencies are ISO-4217 codes whose minor unit IS the major
// unit — UCP money amounts for them are already whole.
var zeroDecimalCurrencies = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "ISK": true,
	"JPY": true, "KMF": true, "KRW": true, "PYG": true, "RWF": true,
	"UGX": true, "VND": true, "VUV": true, "XAF": true, "XOF": true, "XPF": true,
}

var ucpRPCID atomic.Int64

type ucpRPCRequest struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      int64        `json:"id"`
	Method  string       `json:"method"`
	Params  ucpRPCParams `json:"params"`
}

type ucpRPCParams struct {
	Name      string        `json:"name"`
	Arguments ucpSearchArgs `json:"arguments"`
}

type ucpSearchArgs struct {
	Catalog ucpCatalogArgs `json:"catalog"`
	Meta    ucpMeta        `json:"meta"`
}

type ucpCatalogArgs struct {
	Query      string         `json:"query"`
	Pagination *ucpPagination `json:"pagination,omitempty"`
}

type ucpPagination struct {
	Limit int `json:"limit"`
}

type ucpMeta struct {
	Agent ucpAgent `json:"ucp-agent"`
}

type ucpAgent struct {
	Profile string `json:"profile"`
}

type ucpRPCResponse struct {
	Result *struct {
		StructuredContent *struct {
			Products []ucpProduct `json:"products"`
		} `json:"structuredContent"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ucpProduct is the slice of the UCP catalog-search product we consume
// (extra fields ignored). The global catalog carries the storefront URL and
// seller identity on each VARIANT; per-store search may put url on the
// product itself.
type ucpProduct struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Desc  *struct {
		Plain string `json:"plain"`
	} `json:"description"`
	PriceRange *struct {
		Min ucpMoney `json:"min"`
	} `json:"price_range"`
	Metadata *struct {
		TopFeatures []string `json:"top_features"`
	} `json:"metadata"`
	Variants []ucpVariant `json:"variants"`
}

type ucpMoney struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type ucpVariant struct {
	URL          string    `json:"url"`
	Price        *ucpMoney `json:"price"`
	Availability *struct {
		Available *bool `json:"available"`
	} `json:"availability"`
	Media []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"media"`
	Seller *struct {
		Name   string `json:"name"`
		Domain string `json:"domain"`
		Links  []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"links"`
	} `json:"seller"`
}

// ucpSearchBody builds the tools/call search_catalog request body.
func ucpSearchBody(query, profileURL string, limit int) (string, error) {
	req := ucpRPCRequest{
		JSONRPC: "2.0",
		ID:      ucpRPCID.Add(1),
		Method:  "tools/call",
		Params: ucpRPCParams{
			Name: "search_catalog",
			Arguments: ucpSearchArgs{
				Catalog: ucpCatalogArgs{
					Query:      query,
					Pagination: &ucpPagination{Limit: limit},
				},
				Meta: ucpMeta{Agent: ucpAgent{Profile: profileURL}},
			},
		},
	}
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ucpSearch posts one search_catalog call and returns the decoded products.
func (a *shopifyAdapter) ucpSearch(ctx context.Context, mcpURL, query string, limit int) ([]ucpProduct, error) {
	body, err := ucpSearchBody(query, a.profileURL, limit)
	if err != nil {
		return nil, fmt.Errorf("ucp: marshal request: %w", err)
	}
	resp, err := a.fetch.Fetch(ctx, wowa.FetchRequest{
		URL:         mcpURL,
		Method:      http.MethodPost,
		Body:        body,
		ContentType: "application/json",
		Headers:     map[string]string{"accept": "application/json"},
		TimeoutSecs: ucpRPCTimeoutSecs,
	})
	if err != nil {
		return nil, fmt.Errorf("ucp %s: wowa fetch: %w", mcpURL, err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("ucp %s: upstream status %d", mcpURL, resp.Status)
	}
	var rpc ucpRPCResponse
	if err := json.Unmarshal([]byte(resp.Body), &rpc); err != nil {
		return nil, fmt.Errorf("ucp %s: parse response: %w", mcpURL, err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("ucp %s: rpc error %d: %s", mcpURL, rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil || rpc.Result.StructuredContent == nil {
		return nil, fmt.Errorf("ucp %s: empty result", mcpURL)
	}
	return rpc.Result.StructuredContent.Products, nil
}

// ucpProductToResult maps a UCP product to a Result. Products without a
// purchasable URL are skipped (never fabricated).
func ucpProductToResult(shop string, p ucpProduct) (sources.Result, bool) {
	variant := ucpFirstURLVariant(p)
	rawURL := p.URL
	if variant != nil {
		rawURL = variant.URL
	}
	rawURL = CleanTrackingURL(rawURL)
	if rawURL == "" {
		return sources.Result{}, false
	}
	return sources.Result{
		Title:    p.Title,
		URL:      rawURL,
		Content:  ucpContent(p),
		Metadata: ucpMetadata(shop, p, variant),
	}, true
}

// ucpFirstURLVariant picks the first variant carrying a storefront URL.
func ucpFirstURLVariant(p ucpProduct) *ucpVariant {
	for i := range p.Variants {
		if p.Variants[i].URL != "" {
			return &p.Variants[i]
		}
	}
	return nil
}

// ucpMetadata builds the metadata map: price from the chosen variant
// (product price_range.min as fallback), availability, seller identity
// (global catalog) or the shop host, and the first image media.
func ucpMetadata(shop string, p ucpProduct, variant *ucpVariant) map[string]string {
	md := map[string]string{
		MetaListingID: p.ID,
		MetaSeller:    shop,
	}
	ucpSetPrice(md, p, variant)
	ucpSetAvailability(md, variant)
	ucpSetSeller(md, shop, variant)
	ucpSetImage(md, variant)
	return md
}

// ucpSetPrice writes MetaPrice/MetaCurrency from the variant price or the
// product's price_range.min fallback.
func ucpSetPrice(md map[string]string, p ucpProduct, variant *ucpVariant) {
	money := (*ucpMoney)(nil)
	if variant != nil && variant.Price != nil {
		money = variant.Price
	} else if p.PriceRange != nil {
		money = &p.PriceRange.Min
	}
	if money != nil && money.Currency != "" {
		md[MetaPrice] = ucpMinorToMajor(*money)
		md[MetaCurrency] = money.Currency
	}
}

// ucpSetAvailability writes MetaAvailability when the variant states it.
func ucpSetAvailability(md map[string]string, variant *ucpVariant) {
	if variant == nil || variant.Availability == nil || variant.Availability.Available == nil {
		return
	}
	if *variant.Availability.Available {
		md[MetaAvailability] = AvailabilityInStock
	} else {
		md[MetaAvailability] = AvailabilityOutOfStock
	}
}

// ucpSetSeller writes seller/merchant identity: the global catalog carries
// the real seller on each variant; per-store results fall back to the
// shop host.
func ucpSetSeller(md map[string]string, shop string, variant *ucpVariant) {
	sellerName := shop
	if variant != nil && variant.Seller != nil {
		if variant.Seller.Domain != "" {
			md[MetaSeller] = variant.Seller.Domain
		}
		if variant.Seller.Name != "" {
			sellerName = variant.Seller.Name
		}
	}
	if sellerName != "" {
		md[MetaMerchant] = sellerName
	}
}

// ucpSetImage writes MetaImageURL from the first variant image media.
func ucpSetImage(md map[string]string, variant *ucpVariant) {
	if variant == nil {
		return
	}
	for _, m := range variant.Media {
		if m.Type == "image" && m.URL != "" {
			md[MetaImageURL] = m.URL
			break
		}
	}
}

// ucpContent picks the fit-judging text: plain description first, then the
// joined top_features list, then the bare title.
func ucpContent(p ucpProduct) string {
	if p.Desc != nil && p.Desc.Plain != "" {
		return p.Desc.Plain
	}
	if p.Metadata != nil && len(p.Metadata.TopFeatures) > 0 {
		return strings.Join(p.Metadata.TopFeatures, ". ")
	}
	return p.Title
}

// ucpMinorToMajor converts integer minor units to a decimal major-unit
// string, honoring zero-decimal ISO-4217 currencies (JPY, KRW, …).
func ucpMinorToMajor(m ucpMoney) string {
	if zeroDecimalCurrencies[strings.ToUpper(m.Currency)] {
		return strconv.FormatInt(m.Amount, 10)
	}
	return strconv.FormatFloat(float64(m.Amount)/100, 'f', 2, 64)
}
