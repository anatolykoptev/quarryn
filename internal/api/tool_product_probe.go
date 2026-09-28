package api

import (
	"context"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const productProbeDesc = "Run the acceptance probes against the live configured pipeline (ADR-6). " +
	"Three fixed checks: jeff_reachable (one noul Ask through the configured jeff client), " +
	"wowa_reachable (one wowa /fetch of a fixed benign page), and injection_probe — a canned " +
	"listing carrying an embedded prompt-injection payload through the real " +
	"extract+match path, passing only if the injected text never reaches the jeff state and " +
	"the candidate is not auto-passed. Read-only toward third parties; safe to run any time. " +
	"Each run increments quarryn_probe_total{probe,result}."

// productProbeInput takes no arguments — probes are fixed canned checks.
type productProbeInput struct{}

func registerProductProbe(srv *mcp.Server, d deps) {
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        toolProductProbe,
		Description: productProbeDesc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ productProbeInput) (*mcp.CallToolResult, error) {
		return handleProductProbe(ctx, d)
	})
}

// handleProductProbe runs the probe set. The tool is registered even when
// the search pipeline failed to init — a standalone runner still answers
// reachability, which is exactly when operators probe.
func handleProductProbe(ctx context.Context, d deps) (*mcp.CallToolResult, error) {
	if d.prober == nil {
		return errResult("probes unavailable: no runner wired"), nil
	}
	return jsonResult(d.prober.Run(ctx))
}
