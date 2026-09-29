// quarryn — product search service: scrapes candidates via go-wowa,
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

	"github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/quarryn/internal/api"
	"github.com/anatolykoptev/quarryn/internal/auth"
	"github.com/anatolykoptev/quarryn/internal/config"
	"github.com/anatolykoptev/quarryn/internal/group"
	"github.com/anatolykoptev/quarryn/internal/orders"
	"github.com/anatolykoptev/quarryn/internal/postgres"
	"github.com/anatolykoptev/quarryn/internal/probe"
	"github.com/anatolykoptev/quarryn/internal/search"
	"github.com/anatolykoptev/quarryn/internal/watch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is the release-please-bumped fallback; the Dockerfile overrides it
// via -ldflags -X when the build context has a usable .git (plain checkout).
// Dozor worktree builds carry only a .git pointer file, so git describe fails
// there and this constant is what /healthz reports.
var version = "1.26.0" // x-release-please-version

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
	pgdb, feedback, orderStore := openStores(cfg)
	hooks := mcpserver.MCPHooks{
		OnToolResult: func(_ context.Context, name string, dur time.Duration, isErr bool) {
			slog.Info("tool_result", slog.String("tool", name),
				slog.Duration("duration", dur), slog.Bool("error", isErr))
		},
	}
	routes := func(mux *http.ServeMux) {
		// /healthz is the OPEN health probe — the bearer middleware exempts
		// it so docker healthcheck and monitoring smoke checks work without a
		// token. Everything else on this mux requires the bearer secret.
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":  "ok",
				"service": "quarryn",
				"version": version,
			})
		})
		// The feedback REST twin — same store as the product_feedback MCP
		// tool, for callers that want a plain POST.
		mux.HandleFunc("POST /api/v1/feedback", feedback.FeedbackHandler())
		// Order-confirmation ingest — forward-friendly raw .eml body; the
		// bearer middleware still guards it like every non-healthz route.
		mux.Handle("POST /api/v1/orders/ingest", api.OrdersIngestHandler(orderStore))
	}
	return mcpserver.Serve(&mcp.Implementation{
		Name:    "quarryn",
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
		watchStore, checker := newWatcher(searcher, pgdb, cfg, err)
		grouper := newGrouper(cfg)
		api.RegisterTools(srv, searcher, cfg, feedback, probeRunner(searcher, cfg), watchStore, checker, orderStore, grouper, err)
	})
}

// newWatcher assembles the price-watch pair (issue #53): needs both pg
// (state) and the searcher (observations) — with either missing the tool
// still registers and reports unavailable rather than silently vanishing.
func newWatcher(s *search.Searcher, pgdb *postgres.DB, cfg config.Config, initErr error) (*watch.Store, *watch.Checker) {
	if pgdb == nil || initErr != nil {
		if cfg.DatabaseURL != "" {
			slog.Warn("watch disabled — postgres or search pipeline unavailable")
		}
		return nil, nil
	}
	if cfg.WatchNotifyURL == "" {
		slog.Warn("WATCH_NOTIFY_URL unset — watch alerts will keep failing and retrying")
	}
	var notifier watch.Notifier
	switch cfg.WatchNotifyFormat {
	case "", "alertmanager":
		notifier = watch.NewAlertmanagerNotifier(cfg.WatchNotifyURL)
	case "json":
		notifier = watch.NewWebhookNotifier(cfg.WatchNotifyURL, cfg.WatchNotifySecret)
	default:
		slog.Warn("unknown WATCH_NOTIFY_FORMAT — using alertmanager",
			slog.String("format", cfg.WatchNotifyFormat))
		notifier = watch.NewAlertmanagerNotifier(cfg.WatchNotifyURL)
	}
	if cfg.BotNotifyURL != "" {
		// Bot-owned watches (owner="tg:*") alert their user through the
		// bot endpoint; fleet watches keep the default sink.
		notifier = watch.RoutingNotifier{
			TG:      watch.NewWebhookNotifier(cfg.BotNotifyURL, cfg.BotNotifySecret),
			Default: notifier,
		}
		slog.Info("watch notify: tg-owned watches route to bot", slog.String("url", cfg.BotNotifyURL))
	}
	st := watch.NewStore(pgdb.Pool())
	ch := &watch.Checker{
		Store:       st,
		Observer:    watch.NewSearcherObserver(s),
		Notify:      notifier,
		Evaluator:   s, // nil-jeff degrade: condition watches fail closed
		Tick:        cfg.WatchTick,
		MaxPerTick:  cfg.WatchMaxPerTick,
		OfferBudget: 2 * time.Minute,
		QueryBudget: 4 * time.Minute,
	}
	go ch.Run(context.Background())
	slog.Info("watch checker started", slog.Duration("tick", cfg.WatchTick))
	return st, ch
}

// openStores builds the pg-backed state: pool, feedback sink (ADR-10,
// JSONL fallback when pg is down), and the orders store (issue #57).
// pg is optional — a missing/unreachable database degrades to file-only
// feedback and disables watches/orders, never kills the service.
func openStores(cfg config.Config) (*postgres.DB, *api.FeedbackStore, *orders.Store) {
	var pgdb *postgres.DB
	if cfg.DatabaseURL != "" {
		db, err := postgres.New(context.Background(), cfg.DatabaseURL)
		if err != nil {
			slog.Error("postgres unavailable — feedback falls back to JSONL",
				slog.Any("error", err))
		} else {
			pgdb = db
		}
	}
	feedback := api.NewFeedbackStorePG(cfg.FeedbackFile, pgdb)
	var orderStore *orders.Store
	if pgdb != nil {
		orderStore = orders.NewStore(pgdb.Pool())
	}
	return pgdb, feedback, orderStore
}

// newGrouper builds the durable product-identity assigner (issue #98
// embedding tier). It needs the groups registry (pgvector host); the
// embedder is optional inside it — without EMBED_URL exact identifiers
// still persist, keyless products just never join. Either failure logs
// and returns nil: grouping degrades to ephemeral exact-only and the
// service stays up — an auxiliary path never decides availability.
func newGrouper(cfg config.Config) *group.Assigner {
	if cfg.GroupsDatabaseURL == "" {
		return nil
	}
	store, err := group.NewStore(context.Background(), cfg.GroupsDatabaseURL, cfg.EmbedDim)
	if err != nil {
		slog.Error("group store unavailable — grouping stays ephemeral", slog.Any("error", err))
		return nil
	}
	var emb embed.Embedder
	if cfg.EmbedURL != "" {
		// Same client recipe as go-search: bearer from EMBED_TOKEN,
		// process-local LRU, chunk cap matching the embed-server's
		// EMBED_MAX_INPUT_ARRAY, and a circuit breaker so a dead
		// sidecar fails fast instead of stalling every search.
		c, cerr := embed.NewClient(cfg.EmbedURL,
			embed.WithModel(cfg.EmbedModel),
			embed.WithDim(cfg.EmbedDim),
			embed.WithTimeout(15*time.Second),
			embed.WithChunkSize(32),
			embed.WithCircuit(embed.CircuitConfig{}),
			embed.WithCache(group.NewEmbedCache(2048)),
		)
		if cerr != nil {
			slog.Warn("embedder init failed — exact-tier grouping only",
				slog.String("url", cfg.EmbedURL), slog.Any("error", cerr))
		} else {
			emb = c
		}
	}
	slog.Info("group assigner up",
		slog.String("model", cfg.EmbedModel), slog.Bool("embedder", emb != nil))
	return group.NewAssigner(store, emb, cfg.EmbedModel,
		float32(cfg.GroupEmbedThreshold), float32(cfg.GroupEmbedWeak), cfg.GroupTopK)
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
		// check_now on a query watch is a full pipeline run — same tier.
		"product_watch": cfg.SearchToolTimeout,
	}
}

// mcpConfig builds the MCP server configuration. SSE mode (JSONResponse=false)
// + ToolKeepaliveInterval: long tool calls emit progress notifications every
// 10s so proxies don't abandon an in-flight call (same reasoning as go-wowa).
// RESTBridge exposes every registered tool under /api/tools/* plus the
// generated openapi spec.
func mcpConfig(cfg config.Config, mcpReceiving []mcp.Middleware, routes func(*http.ServeMux)) mcpserver.Config {
	return mcpserver.Config{
		Name:                       "quarryn",
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
