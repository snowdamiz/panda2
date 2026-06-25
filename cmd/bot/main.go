package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sn0w/panda2/internal/app"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/observability"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		runHealthcheck()
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	defer stop()

	cfg, warnings, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.LogLevel, os.Stdout)
	slog.SetDefault(logger)
	for _, warning := range warnings {
		logger.Warn("configuration warning", slog.String("warning", warning))
	}

	service, err := app.New(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize app", slog.Any("err", err))
		os.Exit(1)
	}
	defer service.Close(context.Background())

	if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("service stopped with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func runHealthcheck() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if cfg, _, err := config.Load(); err == nil && cfg.Port != "" {
		port = cfg.Port
	}

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "healthcheck returned %s\n", resp.Status)
		os.Exit(1)
	}
}
