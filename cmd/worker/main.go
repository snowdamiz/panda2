package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/maintenance"
	"github.com/sn0w/panda2/internal/observability"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func main() {
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

	dataStore, err := store.Open(ctx, cfg.SQLitePath)
	if err != nil {
		logger.Error("failed to open store", slog.Any("err", err))
		os.Exit(1)
	}
	defer dataStore.Close()

	conversations := repository.NewConversationRepository(dataStore.DB)
	attachments := repository.NewAttachmentRepository(dataStore.DB)
	jobs := repository.NewJobRepository(dataStore.DB)
	billingService := billing.NewService(repository.NewBillingRepository(dataStore.DB), billing.Config{
		PublicURL:              cfg.PublicAppURL,
		SolanaRPCURL:           cfg.SolanaRPCURL,
		SolanaCluster:          cfg.SolanaCluster,
		SolanaTreasuryWallet:   cfg.SolanaTreasuryWallet,
		SolanaConfirmation:     cfg.SolanaConfirmation,
		SolanaPlanLamports:     cfg.SolanaPlanLamports,
		SolanaPackLamports:     cfg.SolanaPackLamports,
		SolanaUSDCentsPerSOL:   cfg.SolanaUSDCentsPerSOL,
		SolanaOrderExpiration:  cfg.SolanaOrderExpiration,
		SolanaActivationKeyTTL: cfg.SolanaActivationKeyTTL,
	})
	maintenanceService := maintenance.NewService(conversations, attachments, dataStore).
		WithCreditExpirer(billingService)
	worker := queue.NewWorker(jobs, "panda-worker")
	worker.Register("maintenance.cleanup", func(ctx context.Context, _ store.Job) error {
		_, err := maintenanceService.Cleanup(ctx, time.Now().UTC())
		return err
	})

	logger.Info("worker started")
	if err := worker.Run(ctx, "", 5*time.Second); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("worker stopped with error", slog.Any("err", err))
		os.Exit(1)
	}
}
