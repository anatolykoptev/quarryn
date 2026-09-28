package api

import (
	"context"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/quarryn/internal/rank"
	"github.com/anatolykoptev/quarryn/internal/trust"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const productSearchDesc = "Search marketplace adapters for products matching a query and rank them " +
	"against your criteria. Criteria mix deterministic constraints " +
	"(price_max:500, price_min:100, currency:usd, brand:sony, not_keyword:refurbished, availability:in_stock, condition:used) " +
	"with free-text subjective criteria judged per product (\"good battery life\", \"durable build\"). " +
	"Returns ranked products with fused scores, per-criterion verdicts and deal signals " +
	"(discount_pct, thumbs, rating). degraded:true means the judge service was unreachable and " +
	"ranking ran on deterministic features only. Each result's url is the purchase page " +
	"(merchant URL when an outbound hop resolved, e.g. a deal-aggregator thread); " +
	"source_url keeps the originating listing when they differ. Read-only; latency is " +
	"tens of seconds — it scrapes real marketplace pages."

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
		resp.Results = append(resp.Results, project(r, d.trust))
	}
	resp.Brief = composeBrief(in.Query, out.Plan, ranked, out.Sources, d.trust)
	return jsonResult(resp)
}

// project maps one ranked candidate to the egress-safe result shape.
// PublicProduct is the only product data that may leave the box; the
// purchase URL and adapter name ride alongside it. URL carries the
// merchant page when an outbound hop resolved (matching what a user
// wants to open); the aggregator thread survives in SourceURL.
func project(r rank.Result, tp *trust.Provider) productResult {
	jc := r.Judged
	url, source := jc.Product.BuyURL, ""
	if url == "" {
		url = jc.URL
	} else if url != jc.URL {
		source = jc.URL
	}
	return productResult{
		URL:             url,
		SourceURL:       source,
		Adapter:         jc.Source,
		BuyURL:          jc.Product.BuyURL,
		Trust:           string(tp.ClassifyMerchant(jc.Product.BuyURL, jc.URL)),
		PublicProduct:   jc.ProductPublic(),
		Score:           r.Score,
		Confidence:      r.Confidence,
		Passed:          jc.Passed,
		MatchedCriteria: r.MatchedCriteria,
		DealSignals:     r.DealSignals,
		ExcludedReason:  string(r.ExcludedReason),
		ExcludedDetail:  r.ExcludedDetail,
		UnjudgedReason:  string(r.UnjudgedReason),
	}
}
