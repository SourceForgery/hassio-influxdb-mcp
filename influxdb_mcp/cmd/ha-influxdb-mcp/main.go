package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"ha-influxdb-mcp/internal/app"
	"ha-influxdb-mcp/internal/config"
)

var version = "dev"

func main() {
	optionsPath := strings.TrimSpace(os.Getenv("OPTIONS_PATH"))
	if optionsPath == "" {
		optionsPath = config.DefaultOptionsPath
	}
	cfg, err := config.Load(optionsPath)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)
	runtime, err := app.New(cfg, version, logger)
	if err != nil {
		logger.Error("initialization failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	logger.Info("starting Home Assistant InfluxDB MCP", "version", version, "tunnel_enabled", cfg.OpenAITunnelEnabled)
	if err := runtime.Run(ctx); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
