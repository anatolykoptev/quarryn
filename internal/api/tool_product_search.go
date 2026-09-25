package api

import (
	"context"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-product-search/internal/rank"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const productSearchDesc = "Search marketplace adapters for products matching a query and rank them " +
	"against your criteria. Criteria mix deterministic constraints " +
	"(price_max:500, price_min:100, currency:usd, brand:sony, not_keyword:refurbished, availability:in_stock) " +
	"with free-text subjective criteria judged per product (\"good battery life\", \"durable build\"). " +
	"Returns ranked products with fused scores, per-criterion verdicts and deal signals " +
	"(discount_pct, thumbs, rating). degraded:true means the judge service was unreachable and " +
	"ranking ran on deterministic features only. Read-only; latency is tens of seconds — " +
	"it scrapes real marketplace pages."

func registerProductSearch(srv *mcp.Server, d deps) {
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        toolProductSearch,
		Description: productSearchDesc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in productSearchInput) (*mcp.CallToolResult, error) {
		return handleProductSearch(ctx, d, in)
	})
}

// handleProductSearch is the tool body: funnel → extract → match through
// the retained searcher, then rank fusion and the public projection. The
// pipeline init error and empty-query input are tool errors, not panics.
func handleProductSearch(ctx context.Context, d deps, in productSearchInput) (*mcp.CallToolResult, error) {
	if d.initErr != nil {
		return d.unavailable(), nil
	}
	if in.Query == "" {
		return errResult("query is required"), nil
	}
	limit := clampMaxResults(in.MaxResults)
	out, err := d.searcher.SearchDetailed(ctx, in.Query, in.Criteria, limit)
	if err != nil {
		logToolError(toolProductSearch, err)
		return errResult(err.Error()), nil
	}

	ranked := rank.Rank(out.Candidates, out.Questions, out.Degraded, d.weights, d.passMin)
	resp := searchOutput{
		RequestID:     out.RequestID,
		Results:       make([]productResult, 0, min(limit, len(ranked))),
		Sources:       out.Sources,
		Degraded:      out.Degraded,
		DegradeReason: out.DegradeReason,
	}
	for i, r := range ranked {
		if i >= limit {
			break
		}
		resp.Results = append(resp.Results, project(r))
	}
	return jsonResult(resp)
}

// project maps one ranked candidate to the egress-safe result shape.
// PublicProduct is the only product data that may leave the box; the
// listing URL and adapter name ride alongside it.
func project(r rank.Result) productResult {
	jc := r.Judged
	return productResult{
		URL:             jc.URL,
		Adapter:         jc.Source,
		PublicProduct:   jc.ProductPublic(),
		Score:           r.Score,
		Confidence:      r.Confidence,
		Passed:          jc.Passed,
		MatchedCriteria: r.MatchedCriteria,
		DealSignals:     r.DealSignals,
		ExcludedReason:  r.ExcludedReason,
		UnjudgedReason:  r.UnjudgedReason,
	}
}
