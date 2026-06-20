package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDevelopmentAllowsMissingCredentials(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("SQLITE_PATH", ":memory:")
	t.Setenv("PORT", "8081")

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
	clearConfigEnv(t)
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("SQLITE_PATH", ":memory:")
	t.Setenv("PORT", "8081")

	_, _, err := Load()
	if err == nil {
		t.Fatal("expected production config to fail without credentials")
	}
}

func TestDefaultSQLitePathUsesFlyDataDirInProduction(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("DISCORD_BOT_TOKEN", "token")
	t.Setenv("DISCORD_APPLICATION_ID", "123")
	t.Setenv("OPENROUTER_API_KEY", "key")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SQLitePath != "/data/panda.db" {
		t.Fatalf("expected /data/panda.db, got %q", cfg.SQLitePath)
	}
}

func TestLoadConfigFile(t *testing.T) {
	clearConfigEnv(t)
	configPath := filepath.Join(t.TempDir(), "panda.config.json")
	writeConfigFile(t, configPath, `{
		"discord": {
			"application_id": "app-from-file",
			"guild_id": "guild-1",
			"owner_user_ids": ["42", "77", "42"]
		},
		"openrouter": {
			"base_url": "https://openrouter.example/api/v1",
			"default_model": "provider/model",
			"fallback_models": ["provider/fallback-a", "provider/fallback-b", "provider/fallback-a"],
			"embedding_model": "provider/embed",
			"app_url": "https://panda.example",
			"app_title": "Panda Local",
			"circuit_breaker": {
				"failure_threshold": 3,
				"cooldown": "45s"
			}
		},
		"brave_search": {
			"base_url": "https://brave.example/res/v1"
		},
		"runtime": {
			"port": "9090",
			"environment": "development",
			"log_level": "debug",
			"user_rate_limit": 9,
			"user_rate_limit_window": "2m"
		},
		"storage": {
			"data_dir": "tmp-data",
			"sqlite_path": ":memory:"
		}
	}`)
	t.Setenv("PANDA_CONFIG", configPath)

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DiscordApplicationID != "app-from-file" || cfg.DiscordGuildID != "guild-1" {
		t.Fatalf("unexpected discord config: app=%q guild=%q", cfg.DiscordApplicationID, cfg.DiscordGuildID)
	}
	if !cfg.IsOwner("42") || !cfg.IsOwner("77") {
		t.Fatalf("expected owner ids from config file, got %#v", cfg.OwnerUserIDs)
	}
	if cfg.OpenRouterBaseURL != "https://openrouter.example/api/v1" || cfg.OpenRouterModel != "provider/model" {
		t.Fatalf("unexpected OpenRouter routing config: base=%q model=%q", cfg.OpenRouterBaseURL, cfg.OpenRouterModel)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/fallback-a" || cfg.OpenRouterFallbackModels[1] != "provider/fallback-b" {
		t.Fatalf("unexpected fallback models %#v", cfg.OpenRouterFallbackModels)
	}
	if cfg.OpenRouterEmbeddingModel != "provider/embed" || cfg.OpenRouterAppURL != "https://panda.example" || cfg.OpenRouterAppTitle != "Panda Local" {
		t.Fatalf("unexpected OpenRouter metadata: embed=%q url=%q title=%q", cfg.OpenRouterEmbeddingModel, cfg.OpenRouterAppURL, cfg.OpenRouterAppTitle)
	}
	if cfg.OpenRouterCircuitBreakerFailureThreshold != 3 || cfg.OpenRouterCircuitBreakerCooldown.String() != "45s" {
		t.Fatalf("unexpected circuit breaker config: threshold=%d cooldown=%s", cfg.OpenRouterCircuitBreakerFailureThreshold, cfg.OpenRouterCircuitBreakerCooldown)
	}
	if cfg.BraveSearchBaseURL != "https://brave.example/res/v1" {
		t.Fatalf("unexpected Brave Search base URL: %q", cfg.BraveSearchBaseURL)
	}
	if cfg.Port != "9090" || cfg.Environment != "development" || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected runtime config: port=%q environment=%q log=%q", cfg.Port, cfg.Environment, cfg.LogLevel)
	}
	if cfg.UserRateLimit != 9 || cfg.UserRateLimitWindow.String() != "2m0s" {
		t.Fatalf("unexpected rate limit config: limit=%d window=%s", cfg.UserRateLimit, cfg.UserRateLimitWindow)
	}
	if cfg.DataDir != "tmp-data" || cfg.SQLitePath != ":memory:" {
		t.Fatalf("unexpected storage config: data=%q sqlite=%q", cfg.DataDir, cfg.SQLitePath)
	}
}

func TestEnvOverridesConfigFile(t *testing.T) {
	clearConfigEnv(t)
	configPath := filepath.Join(t.TempDir(), "panda.config.json")
	writeConfigFile(t, configPath, `{
		"discord": {
			"application_id": "app-from-file",
			"owner_user_ids": ["42"]
		},
		"openrouter": {
			"default_model": "provider/from-file",
			"fallback_models": ["provider/file-fallback"]
		}
	}`)
	t.Setenv("PANDA_CONFIG", configPath)
	t.Setenv("DISCORD_APPLICATION_ID", "app-from-env")
	t.Setenv("OPENROUTER_DEFAULT_MODEL", "provider/from-env")
	t.Setenv("OPENROUTER_FALLBACK_MODELS", "provider/env-a,provider/env-b")
	t.Setenv("BRAVE_SEARCH_API_KEY", "brave-key")
	t.Setenv("BRAVE_SEARCH_BASE_URL", "https://brave-env.example/res/v1")
	t.Setenv("OWNER_USER_IDS", "99")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DiscordApplicationID != "app-from-env" {
		t.Fatalf("expected env application id, got %q", cfg.DiscordApplicationID)
	}
	if cfg.OpenRouterModel != "provider/from-env" {
		t.Fatalf("expected env default model, got %q", cfg.OpenRouterModel)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/env-a" || cfg.OpenRouterFallbackModels[1] != "provider/env-b" {
		t.Fatalf("expected env fallback models, got %#v", cfg.OpenRouterFallbackModels)
	}
	if cfg.IsOwner("42") || !cfg.IsOwner("99") {
		t.Fatalf("expected env owner ids to override file ids, got %#v", cfg.OwnerUserIDs)
	}
	if !cfg.BraveSearchConfigured() || cfg.BraveSearchBaseURL != "https://brave-env.example/res/v1" {
		t.Fatalf("expected env Brave Search settings, configured=%t base=%q", cfg.BraveSearchConfigured(), cfg.BraveSearchBaseURL)
	}
}

func TestLoadOptionalRuntimeEnvOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("SQLITE_PATH", ":memory:")
	t.Setenv("PORT", "8081")
	t.Setenv("OPENROUTER_EMBEDDING_MODEL", "openai/text-embedding-3-small")
	t.Setenv("OPENROUTER_FALLBACK_MODELS", "provider/a, provider/b, provider/a")
	t.Setenv("OPENROUTER_CIRCUIT_FAILURE_THRESHOLD", "3")
	t.Setenv("OPENROUTER_CIRCUIT_COOLDOWN", "45s")

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
}

func TestExplicitMissingConfigFileFails(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PANDA_CONFIG", filepath.Join(t.TempDir(), "missing.json"))

	_, _, err := Load()
	if err == nil {
		t.Fatal("expected missing explicit config file to fail")
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PANDA_CONFIG",
		"DISCORD_BOT_TOKEN",
		"DISCORD_APPLICATION_ID",
		"DISCORD_GUILD_ID",
		"OPENROUTER_API_KEY",
		"OPENROUTER_BASE_URL",
		"OPENROUTER_DEFAULT_MODEL",
		"OPENROUTER_FALLBACK_MODELS",
		"OPENROUTER_EMBEDDING_MODEL",
		"OPENROUTER_APP_URL",
		"OPENROUTER_APP_TITLE",
		"OPENROUTER_CIRCUIT_FAILURE_THRESHOLD",
		"OPENROUTER_CIRCUIT_COOLDOWN",
		"BRAVE_SEARCH_API_KEY",
		"BRAVE_SEARCH_BASE_URL",
		"SQLITE_PATH",
		"DATA_DIR",
		"PORT",
		"ENVIRONMENT",
		"LOG_LEVEL",
		"OWNER_USER_IDS",
		"USER_RATE_LIMIT",
		"USER_RATE_LIMIT_WINDOW",
		"FLY_APP_NAME",
	} {
		oldValue, hadValue := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func(name, oldValue string, hadValue bool) func() {
			return func() {
				if hadValue {
					_ = os.Setenv(name, oldValue)
					return
				}
				_ = os.Unsetenv(name)
			}
		}(name, oldValue, hadValue))
	}
}

func writeConfigFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}
