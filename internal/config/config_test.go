package config

import (
	"os"
	"testing"
)

func TestLoadDevelopmentAllowsMissingCredentials(t *testing.T) {
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("SQLITE_PATH", ":memory:")
	t.Setenv("PORT", "8081")
	t.Setenv("DISCORD_BOT_TOKEN", "")
	t.Setenv("DISCORD_APPLICATION_ID", "")
	t.Setenv("OPENROUTER_API_KEY", "")

	cfg, warnings, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DiscordConfigured() {
		t.Fatal("expected discord to be unconfigured")
	}
	if cfg.OpenRouterConfigured() {
		t.Fatal("expected openrouter to be unconfigured")
	}
	if len(warnings) < 2 {
		t.Fatalf("expected missing credential warnings, got %v", warnings)
	}
}

func TestProductionRequiresCredentials(t *testing.T) {
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("SQLITE_PATH", ":memory:")
	t.Setenv("PORT", "8081")
	t.Setenv("DISCORD_BOT_TOKEN", "")
	t.Setenv("DISCORD_APPLICATION_ID", "")
	t.Setenv("OPENROUTER_API_KEY", "")

	_, _, err := Load()
	if err == nil {
		t.Fatal("expected production config to fail without credentials")
	}
}

func TestDefaultSQLitePathUsesFlyDataDirInProduction(t *testing.T) {
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("DISCORD_BOT_TOKEN", "token")
	t.Setenv("DISCORD_APPLICATION_ID", "123")
	t.Setenv("OPENROUTER_API_KEY", "key")
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("DATA_DIR", "")

	oldFly := os.Getenv("FLY_APP_NAME")
	t.Setenv("FLY_APP_NAME", oldFly)

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SQLitePath != "/data/panda.db" {
		t.Fatalf("expected /data/panda.db, got %q", cfg.SQLitePath)
	}
}

func TestLoadOptionalRuntimeConfig(t *testing.T) {
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("SQLITE_PATH", ":memory:")
	t.Setenv("PORT", "8081")
	t.Setenv("OPENROUTER_EMBEDDING_MODEL", "openai/text-embedding-3-small")
	t.Setenv("OPENROUTER_FALLBACK_MODELS", "provider/a, provider/b, provider/a")
	t.Setenv("OPENROUTER_CIRCUIT_FAILURE_THRESHOLD", "3")
	t.Setenv("OPENROUTER_CIRCUIT_COOLDOWN", "45s")
	t.Setenv("ATTACHMENT_CACHE_TTL", "2h")
	t.Setenv("METRICS_ADDR", "127.0.0.1:9090")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.OpenRouterEmbeddingModel != "openai/text-embedding-3-small" {
		t.Fatalf("unexpected embedding model %q", cfg.OpenRouterEmbeddingModel)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/a" || cfg.OpenRouterFallbackModels[1] != "provider/b" {
		t.Fatalf("unexpected fallback models %#v", cfg.OpenRouterFallbackModels)
	}
	if cfg.OpenRouterCircuitBreakerFailureThreshold != 3 || cfg.OpenRouterCircuitBreakerCooldown.String() != "45s" {
		t.Fatalf("unexpected circuit breaker config: threshold=%d cooldown=%s", cfg.OpenRouterCircuitBreakerFailureThreshold, cfg.OpenRouterCircuitBreakerCooldown)
	}
	if cfg.AttachmentCacheTTL.String() != "2h0m0s" {
		t.Fatalf("unexpected attachment TTL %s", cfg.AttachmentCacheTTL)
	}
	if cfg.MetricsAddr != "127.0.0.1:9090" {
		t.Fatalf("unexpected metrics addr %q", cfg.MetricsAddr)
	}
}
