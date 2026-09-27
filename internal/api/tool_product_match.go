package api

import (
	"context"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/quarryn/internal/rank"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const productMatchDesc = "Judge one product page URL against criteria without running a search: " +
	"the page is fetched and extracted through the same pipeline search candidates take, " +
	"then deterministic constraints and (when configured) jeff noul verdicts are applied. " +
	"Accepts the same criterion vocabulary as product_search. Use it to re-check a specific " +
	"listing, e.g. one a user already has open. Read-only."

func registerProductMatch(srv *mcp.Server, d deps) {
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        toolProductMatch,
		Description: productMatchDesc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in productMatchInput) (*mcp.CallToolResult, error) {
		return handleProductMatch(ctx, d, in)
	})
}

// handleProductMatch is the thin single-URL reuse of the per-candidate
// path: SSRF screen → extract → match, then the same fusion/projection as
// search results so callers get one consistent shape back.
func handleProductMatch(ctx context.Context, d deps, in productMatchInput) (*mcp.CallToolResult, error) {
	if d.initErr != nil {
		return d.unavailable(), nil
	}
	if in.ProductURL == "" {
		return errResult("product_url is required"), nil
	}
	out, err := d.searcher.MatchURL(ctx, in.ProductURL, in.Criteria)
	if err != nil {
		logToolError(toolProductMatch, err)
		return errResult(err.Error()), nil
	}
	ranked := rank.Rank(out.Candidates, out.Questions, out.Degraded, d.weights, d.passMin)
	if len(ranked) == 0 {
		return errResult("no result for " + in.ProductURL), nil
	}
	return jsonResult(matchOutput{
		RequestID:     out.RequestID,
		Result:        project(ranked[0], d.trust),
		Degraded:      out.Degraded,
		DegradeReason: out.DegradeReason,
	})
}
