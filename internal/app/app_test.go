package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestInstallResultURLBuildsLandingRoute(t *testing.T) {
	if got := installResultURL("https://pandaclanker.xyz/", "install/success/"); got != "https://pandaclanker.xyz/install/success/" {
		t.Fatalf("unexpected install result URL: %q", got)
	}
}

func TestOpenRouterClipDetectionConfigUsesDefaultProviderRouting(t *testing.T) {
	cfg := config.Config{
		OpenRouterAPIKey:                         "key",
		OpenRouterBaseURL:                        "https://openrouter.example/api/v1",
		OpenRouterAppURL:                         "https://panda.example",
		OpenRouterAppTitle:                       "Panda Test",
		OpenRouterProviderOrder:                  []string{"cerebras"},
		OpenRouterAllowProviderFallbacks:         false,
		OpenRouterClipDetectionTimeout:           2 * time.Minute,
		OpenRouterClipCompositionTimeout:         3 * time.Minute,
		OpenRouterCircuitBreakerFailureThreshold: 3,
		OpenRouterCircuitBreakerCooldown:         45 * time.Second,
	}
	chatConfig := openRouterChatConfig(cfg)
	if len(chatConfig.ProviderOrder) != 1 || chatConfig.ProviderOrder[0] != "cerebras" {
		t.Fatalf("chat config should keep configured provider routing: %+v", chatConfig.ProviderOrder)
	}
	if chatConfig.AllowProviderFallbacks {
		t.Fatal("chat config should keep configured provider fallback setting")
	}

	clipConfig := openRouterClipDetectionConfig(cfg)
	if len(clipConfig.ProviderOrder) != 0 {
		t.Fatalf("clip detection should use default provider routing, got %+v", clipConfig.ProviderOrder)
	}
	if !clipConfig.AllowProviderFallbacks {
		t.Fatal("clip detection should allow default provider fallback routing")
	}
	if clipConfig.Timeout != cfg.OpenRouterClipDetectionTimeout {
		t.Fatalf("clip detection should use configured timeout, got %s", clipConfig.Timeout)
	}
	if clipConfig.APIKey != cfg.OpenRouterAPIKey || clipConfig.BaseURL != cfg.OpenRouterBaseURL || clipConfig.AppURL != cfg.OpenRouterAppURL || clipConfig.AppTitle != cfg.OpenRouterAppTitle {
		t.Fatalf("clip detection should preserve OpenRouter connection metadata: %+v", clipConfig)
	}

	compositionConfig := openRouterClipCompositionConfig(cfg)
	if len(compositionConfig.ProviderOrder) != 0 {
		t.Fatalf("clip composition should use default provider routing, got %+v", compositionConfig.ProviderOrder)
	}
	if !compositionConfig.AllowProviderFallbacks {
		t.Fatal("clip composition should allow default provider fallback routing")
	}
	if compositionConfig.Timeout != cfg.OpenRouterClipCompositionTimeout {
		t.Fatalf("clip composition should use configured timeout, got %s", compositionConfig.Timeout)
	}
	if compositionConfig.APIKey != cfg.OpenRouterAPIKey || compositionConfig.BaseURL != cfg.OpenRouterBaseURL || compositionConfig.AppURL != cfg.OpenRouterAppURL || compositionConfig.AppTitle != cfg.OpenRouterAppTitle {
		t.Fatalf("clip composition should preserve OpenRouter connection metadata: %+v", compositionConfig)
	}
}

func TestWorkerHandlesQueuedComposedEventJobs(t *testing.T) {
	ctx := context.Background()
	service, err := New(ctx, config.Config{
		SQLitePath:          t.TempDir() + "/panda.db",
		Port:                "8080",
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "test-model",
		BraveSearchBaseURL:  "https://api.search.brave.com/res/v1",
		SolanaPlanLamports:  map[string]int64{},
		MusicSidecarDir:     t.TempDir(),
		UserRateLimit:       5,
		UserRateLimitWindow: 0,
		OwnerUserIDs:        map[string]struct{}{},
		Environment:         "development",
		LogLevel:            "error",
		SolanaCluster:       "devnet",
		SolanaConfirmation:  "finalized",
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
