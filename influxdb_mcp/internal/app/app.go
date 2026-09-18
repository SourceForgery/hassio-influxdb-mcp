package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"

	"ha-influxdb-mcp/internal/bridge"
	"ha-influxdb-mcp/internal/config"
	"ha-influxdb-mcp/internal/influx"
)

const serverInstructions = "This server provides read-only access to Home Assistant history in InfluxDB. Use list_entities when an entity ID or measurement is uncertain, and list_fields when the desired field is uncertain. Historical queries must always use a bounded start time. Prefer server-side aggregation with an interval when a raw query would return many points. No tool can modify InfluxDB or Home Assistant."

type Runtime struct {
	cfg         config.Options
	version     string
	logger      *slog.Logger
	influx      *influx.Client
	mcpServer   *mcp.Server
	handler     http.Handler
	tunnelReady atomic.Bool
}

func New(cfg config.Options, version string, logger *slog.Logger) (*Runtime, error) {
	client, err := influx.NewClient(
		cfg.InfluxURL,
		cfg.InfluxDatabase,
		cfg.InfluxUsername,
		cfg.InfluxPassword,
		cfg.InfluxVerifyTLS,
		cfg.RequestTimeout(),
	)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "home-assistant-influxdb",
		Version: version,
	}, &mcp.ServerOptions{Instructions: serverInstructions, Logger: logger})
	bridge.New(client, bridge.Limits{
		MaxPoints:           cfg.MaxPoints,
		MaxEntitiesPerQuery: cfg.MaxEntitiesPerQuery,
		MaxSchemaSeries:     cfg.MaxSchemaSeries,
		MaxLookback:         cfg.MaxLookback(),
	}).RegisterTools(server)

	runtime := &Runtime{
		cfg:       cfg,
		version:   version,
		logger:    logger,
		influx:    client,
		mcpServer: server,
	}
	runtime.handler = runtime.buildHandler()
	return runtime, nil
}

func (r *Runtime) Handler() http.Handler {
	return r.handler
}

func (r *Runtime) buildHandler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return r.mcpServer
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		MaxRequestBodyBytes:          1 << 20,
		PropagateRequestCancellation: true,
		Logger:                       r.logger,
	})
	originProtection := http.NewCrossOriginProtection()

	mux := http.NewServeMux()
	mux.Handle("/mcp", originProtection.Handler(mcpHandler))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", r.handleReady)
	mux.HandleFunc("GET /{$}", r.handleStatus)
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, req)
	})
}

func (r *Runtime) handleReady(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), min(5*time.Second, r.cfg.RequestTimeout()))
	defer cancel()
	if err := r.influx.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "influx": "unavailable"})
		return
	}
	if r.cfg.OpenAITunnelEnabled && !r.tunnelReady.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "influx": "ok", "tunnel": "connecting"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "influx": "ok", "tunnel": tunnelStatus(r.cfg.OpenAITunnelEnabled, r.tunnelReady.Load())})
}

func (r *Runtime) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":            "Home Assistant InfluxDB MCP",
		"version":         r.version,
		"mcp_endpoint":    "/mcp",
		"influx_database": r.cfg.InfluxDatabase,
		"tunnel":          tunnelStatus(r.cfg.OpenAITunnelEnabled, r.tunnelReady.Load()),
		"read_only":       true,
	})
}

func tunnelStatus(enabled, ready bool) string {
	if !enabled {
		return "disabled"
	}
	if ready {
		return "connected"
	}
	return "connecting"
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (r *Runtime) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", r.cfg.HTTPListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", r.cfg.HTTPListenAddress, err)
	}
	httpServer := &http.Server{
		Handler:           r.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	httpDone := make(chan error, 1)
	go func() {
		r.logger.Info("local MCP endpoint listening", "address", listener.Addr().String(), "path", "/mcp")
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		httpDone <- err
	}()

	var tunnelDone <-chan error
	if r.cfg.OpenAITunnelEnabled {
		ch := make(chan error, 1)
		tunnelDone = ch
		go func() { ch <- r.runTunnel(ctx) }()
	} else {
		r.logger.Warn("OpenAI Secure MCP Tunnel is disabled; only the local MCP endpoint is active")
	}

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-httpDone:
		if err != nil {
			runErr = fmt.Errorf("HTTP server stopped: %w", err)
		} else if ctx.Err() == nil {
			runErr = errors.New("HTTP server stopped unexpectedly")
		}
	case err := <-tunnelDone:
		if err != nil {
			runErr = fmt.Errorf("Secure MCP Tunnel stopped: %w", err)
		} else if ctx.Err() == nil {
			runErr = errors.New("Secure MCP Tunnel stopped unexpectedly")
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down HTTP server: %w", err)
	}
	return runErr
}

func (r *Runtime) runTunnel(ctx context.Context) error {
	serverTransport, tunnelTransport := mcp.NewInMemoryTransports()
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- r.mcpServer.Run(serverCtx, serverTransport)
	}()

	client, err := tunnelclient.New(tunnelclient.Config{
		TunnelID: r.cfg.OpenAITunnelID,
		APIKey:   r.cfg.OpenAITunnelAPIKey,
	}, tunnelTransport)
	if err != nil {
		return fmt.Errorf("configure tunnel client: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start tunnel client: %w", err)
	}
	defer func() {
		r.tunnelReady.Store(false)
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Stop(stopCtx)
	}()

	readyCtx, cancel := context.WithTimeout(ctx, r.cfg.TunnelReadyTimeout())
	defer cancel()
	if err := client.WaitUntilReady(readyCtx); err != nil {
		return fmt.Errorf("wait for tunnel readiness: %w", err)
	}
	r.tunnelReady.Store(true)
	r.logger.Info("OpenAI Secure MCP Tunnel connected", "tunnel_id", redactTunnelID(r.cfg.OpenAITunnelID))

	select {
	case <-ctx.Done():
		return nil
	case <-client.Done():
		return errors.New("tunnel client runtime ended")
	case err := <-serverDone:
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("in-memory MCP server stopped: %w", err)
	}
}

func redactTunnelID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return "configured"
	}
	return value[:9] + "..." + value[len(value)-4:]
}
