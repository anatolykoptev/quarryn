package extract

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/go-product-search/internal/money"
)

// productJSONSchema is the fixed JSON Schema handed to wowa
// /api/v1/extract for the LLM fallback (ADR-2). The wire enum/bounds
// constraints mirror validate.go — the merged output still passes the same
// strict problems() check, so a schema-conforming-but-implausible value
// (price 1e30, rating 9) is rejected like any other.
var productJSONSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "name":         {"type": "string", "maxLength": 500},
    "price":        {"type": "number", "exclusiveMinimum": 0, "maximum": 10000000},
    "currency":     {"type": "string", "pattern": "^[A-Z]{3}$"},
    "availability": {"type": "string", "enum": ["in_stock","out_of_stock","pre_order","backorder","limited","discontinued"]},
    "condition":    {"type": "string", "enum": ["new","like_new","refurbished","used","for_parts","damaged"]},
    "rating":       {"type": "number", "minimum": 0, "maximum": 5},
    "seller_name":  {"type": "string", "maxLength": 200},
    "image_url":    {"type": "string", "format": "uri"},
    "description":  {"type": "string", "maxLength": 2000}
  },
  "required": ["name", "price"]
}`)

// extractPrompt pins the LLM task: the schema carries the shape, the prompt
// carries the discipline (omit, never guess).
const extractPrompt = `Extract this product listing's structured data: name, ` +
	`price (number only), currency (ISO 4217), availability, condition, ` +
	`seller display name, rating (0-5), primary image URL, and a ` +
	`one-paragraph description. Return JSON conforming to the schema. Omit ` +
	`a field rather than guess when the page does not show it.`

// llmProduct mirrors productJSONSchema's wire keys for decode.
type llmProduct struct {
	Name         string   `json:"name"`
	Price        *float64 `json:"price"`
	Currency     string   `json:"currency"`
	Availability string   `json:"availability"`
	Condition    string   `json:"condition"`
	Rating       *float64 `json:"rating"`
	SellerName   string   `json:"seller_name"`
	ImageURL     string   `json:"image_url"`
	Description  string   `json:"description"`
}

// llmExtract runs the fenced wowa /api/v1/extract call (ADR-2) over the
// candidate's page. wowa fetches the page itself, so this works even where
// the plain fetch hit a bot wall. The decoded payload is normalized like
// schema.org output; strict validation happens after merge, identical for
// every source.
func (p *Pipeline) llmExtract(ctx context.Context, pageURL string) (*Product, error) {
	resp, err := p.llm.Extract(ctx, wowa.ExtractRequest{
		URL:      pageURL,
		Prompt:   extractPrompt,
		Schema:   productJSONSchema,
		MaxChars: p.cfg.LLMMaxChars,
	})
	if err != nil {
		return nil, fmt.Errorf("extract: wowa extract: %w", err)
	}
	var lp llmProduct
	if err := json.Unmarshal(resp.Data, &lp); err != nil {
		return nil, fmt.Errorf("extract: decode llm payload: %w", err)
	}
	prod := &Product{
		Name:         lp.Name,
		URL:          pageURL,
		Currency:     normalizeCurrency(lp.Currency),
		Availability: normalizeAvailability(lp.Availability),
		Condition:    normalizeCondition(lp.Condition),
		SellerName:   lp.SellerName,
		Rating:       lp.Rating,
		ImageURL:     lp.ImageURL,
		Description:  lp.Description,
		Source:       domainOf(pageURL),
		Method:       MethodLLM,
	}
	if lp.Price != nil {
		if m, ok := money.FromFloat(*lp.Price, prod.Currency); ok {
			prod.PriceMinor = &m
		}
	}
	if len(resp.Data) <= maxRawLen {
		prod.Raw = resp.Data
	}
	return prod, nil
}
