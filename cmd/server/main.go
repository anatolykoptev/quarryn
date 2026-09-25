// go-product-search — product search service: scrapes candidates via go-wowa,
// matches them via the jeff decision service. MCP transport + REST bridge.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/go-product-search/internal/api"
	"github.com/anatolykoptev/go-product-search/internal/auth"
	"github.com/anatolykoptev/go-product-search/internal/config"
	"github.com/anatolykoptev/go-product-search/internal/probe"
	"github.com/anatolykoptev/go-product-search/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var version = "dev"

func main() {
	cfg := config.Load()
	startPrometheusScrape(cfg.PromPort, slog.Default())

	if err := runMCPServer(cfg); err != nil {
		slog.Error("server failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// runMCPServer wires hooks + routes and starts the MCP/REST listener.
// mcpserver.Serve owns the lifecycle: signal.NotifyContext(SIGINT/SIGTERM) →
// graceful shutdown with cfg.ShutdownTimeout.
func runMCPServer(cfg config.Config) error {
	// The ADR-10 outcome sink is built once and shared by the
	// product_feedback tool and the POST /api/v1/feedback REST twin.
	// Unwritable path → log-only mode, never fatal.
	feedback := api.NewFeedbackStore(cfg.FeedbackFile)
	hooks := mcpserver.MCPHooks{
		OnToolResult: func(_ context.Context, name string, dur time.Duration, isErr bool) {
			slog.Info("tool_result", slog.String("tool", name),
				slog.Duration("duration", dur), slog.Bool("error", isErr))
		},
	}
	routes := func(mux *http.ServeMux) {
		// /healthz is the OPEN health probe — the bearer middleware exempts
		// it so docker healthcheck and dozor smoke checks work without a
		// token. Everything else on this mux requires the bearer secret.
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":  "ok",
				"service": "go-product-search",
				"version": version,
			})
		})
		// The feedback REST twin — same store as the product_feedback MCP
		// tool, for callers that want a plain POST.
		mux.HandleFunc("POST /api/v1/feedback", feedback.FeedbackHandler())
	}
	return mcpserver.Serve(&mcp.Implementation{
		Name:    "go-product-search",
		Version: version,
	}, mcpConfig(cfg, []mcp.Middleware{hooks.Middleware()}, routes), func(srv *mcp.Server) {
		// The searcher is built once here so the tool closures capture the
		// retained pipeline — misconfig (bad WOWA_URL, zero enabled
		// adapters) surfaces in the log before the first tool call. On init
		// failure the tools still register and return the stored error:
		// a loudly erroring tool beats one that silently never existed.
		searcher, err := search.New(cfg)
		if err != nil {
			slog.Error("search pipeline init failed", slog.Any("error", err))
		}
		api.RegisterTools(srv, searcher, cfg, feedback, probeRunner(searcher, cfg), err)
	})
}

// probeRunner picks the acceptance-probe backend: the pipeline's own
// clients when it built, standalone clients when it did not — probes are
// most useful exactly when the pipeline is down, so a wowa URL parse
// failure must not silence them.
func probeRunner(s *search.Searcher, cfg config.Config) *probe.Runner {
	if s != nil {
		return s.Prober()
	}
	return probe.NewStandalone(cfg)
}

// toolTimeouts holds per-tool deadline overrides for the tools the next arc
// registers. Wired now so the budget lives at the transport layer and the
// tool handler only has to honour its context.
func toolTimeouts(cfg config.Config) map[string]time.Duration {
	return map[string]time.Duration{
		// product_search = wowa scrape (slow) + jeff match per candidate.
		"product_search": cfg.SearchToolTimeout,
		// product_match = jeff-only scoring of a supplied candidate set.
		"product_match": cfg.MatchToolTimeout,
	}
}

// mcpConfig builds the MCP server configuration. SSE mode (JSONResponse=false)
// + ToolKeepaliveInterval: long tool calls emit progress notifications every
// 10s so proxies don't abandon an in-flight call (same reasoning as go-wowa).
// RESTBridge exposes every registered tool under /api/tools/* plus the
// generated openapi spec.
func mcpConfig(cfg config.Config, mcpReceiving []mcp.Middleware, routes func(*http.ServeMux)) mcpserver.Config {
	return mcpserver.Config{
		Name:                       "go-product-search",
		Version:                    version,
		Port:                       cfg.Port,
		SchemaCache:                mcp.NewSchemaCache(),
		DisableLocalhostProtection: true,
		SessionTimeout:             10 * time.Minute,
		ToolTimeout:                cfg.ToolTimeout,
		ToolTimeouts:               toolTimeouts(cfg),
		RESTBridge:                 true,
		JSONResponse:               false,
		ToolKeepaliveInterval:      10 * time.Second,
		MCPReceivingMiddleware:     mcpReceiving,
		// Bearer auth wraps the WHOLE mux (REST + MCP + extra routes) —
		// /healthz is exempted inside the middleware.
		Middleware: []mcpserver.Middleware{auth.Bearer(cfg.InternalSecret)},
		Routes:     routes,
	}
}

// startPrometheusScrape exposes /metrics on PROM_PORT (9922 = PORT+1000, the
// fleet convention). A separate listener keeps scrape traffic off the
// bearer-authed main mux; the goroutine dies with the process.
func startPrometheusScrape(promPort string, logger *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              ":" + promPort,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("prometheus scrape endpoint", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("prom endpoint", slog.Any("error", err))
		}
	}()
}
