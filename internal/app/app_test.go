package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestInstallResultURLBuildsLandingRoute(t *testing.T) {
	if got := installResultURL("https://pandaclanker.xyz/", "install/success"); got != "https://pandaclanker.xyz/install/success" {
		t.Fatalf("unexpected install result URL: %q", got)
	}
}

func TestWorkerHandlesQueuedComposedEventJobs(t *testing.T) {
	ctx := context.Background()
	service, err := New(ctx, config.Config{
		SQLitePath:                t.TempDir() + "/panda.db",
		Port:                      "8080",
		OpenRouterBaseURL:         "https://openrouter.ai/api/v1",
		OpenRouterModel:           "test-model",
		BraveSearchBaseURL:        "https://api.search.brave.com/res/v1",
		SolanaPlanLamports:        map[string]int64{},
		MusicSidecarDir:           t.TempDir(),
		UserRateLimit:             5,
		UserRateLimitWindow:       0,
		OwnerUserIDs:              map[string]struct{}{},
		Environment:               "development",
		LogLevel:                  "error",
		SolanaCluster:             "devnet",
		SolanaConfirmation:        "finalized",
		OpenRouterClassifierModel: "test-classifier",
	}, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer service.Close(context.Background())

	jobs := repository.NewJobRepository(service.store.DB)
	job, err := jobs.Enqueue(ctx, store.Job{
		Kind:    composed.EventJobKind,
		GuildID: "guild-1",
		Payload: `{
			"guild_id": "guild-1",
			"event_id": "event-1",
			"event_type": "guild.member.joined",
			"user_id": "user-1"
		}`,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	worked, err := service.worker.WorkOnce(ctx, "")
	if err != nil {
		t.Fatalf("WorkOnce: %v", err)
	}
	if !worked {
		t.Fatal("expected worker to claim the composed event job")
	}

	var saved store.Job
	if err := service.store.DB.First(&saved, job.ID).Error; err != nil {
		t.Fatalf("lookup job: %v", err)
	}
	if saved.Status != "succeeded" {
		t.Fatalf("expected composed event job to succeed, got %+v", saved)
	}
}
