package api

import (
	"encoding/json"
	"log/slog"

	"github.com/anatolykoptev/go-product-search/internal/config"
	"github.com/anatolykoptev/go-product-search/internal/rank"
	"github.com/anatolykoptev/go-product-search/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool name constants — the per-tool timeout map in main.go keys on these.
const (
	toolProductSearch = "product_search"
	toolProductMatch  = "product_match"
)

// Result-capping defaults for product_search. The funnel already bounds
// the upstream pool at 50 (ADR-8); the same number caps the tool response.
const (
	defaultMaxResults = 10
	maxResultsCap     = 50
)

// deps bundles what the tool handlers close over: the retained searcher,
// the fusion weights/pass threshold resolved once from config, and the
// pipeline init error (non-nil → handlers report it instead of the tool
// silently missing).
type deps struct {
	searcher *search.Searcher
	weights  rank.Weights
	passMin  float64
	initErr  error
}

// RegisterTools binds product_search and product_match to the server. On a
// pipeline init error the tools still register — they return the stored
// error loudly rather than the tools vanishing from the listing (a missing
// tool reads as "unsupported", an erroring tool reads as "down").
//
// RESTBridge auto-exposes both under /api/tools/* — no REST handlers here.
func RegisterTools(srv *mcp.Server, searcher *search.Searcher, cfg config.Config, initErr error) {
	d := deps{
		searcher: searcher,
		weights: rank.Weights{
			Funnel: cfg.RankFunnelWeight,
			Deal:   cfg.RankDealWeight,
			Jeff:   cfg.RankJeffWeight,
		},
		passMin: cfg.JeffMatchMin,
		initErr: initErr,
	}
	registerProductSearch(srv, d)
	registerProductMatch(srv, d)
}

// unavailable reports the init error every tool handler surfaces when the
// pipeline never built.
func (d deps) unavailable() *mcp.CallToolResult {
	return errResult("search pipeline unavailable: " + d.initErr.Error())
}

// clampMaxResults resolves the caller's max_results to the effective cap.
func clampMaxResults(n int) int {
	switch {
	case n <= 0:
		return defaultMaxResults
	case n > maxResultsCap:
		return maxResultsCap
	}
	return n
}

// jsonResult marshals v into the tool's text payload. A marshal failure is
// impossible for these struct shapes in practice; it is still reported as
// a tool error rather than panicking the session.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return errResult("marshal output: " + err.Error()), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
}

// errResult is a tool-level error (IsError result, not a transport error —
// the fleet convention so MCP clients surface the message).
func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// logToolError warns on handler-level failures so tool errors are visible
// server-side, not only to the calling client.
func logToolError(tool string, err error) {
	slog.Warn("tool error", slog.String("tool", tool), slog.Any("error", err))
}
