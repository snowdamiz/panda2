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
	t.Setenv("DISCORD_CLIENT_SECRET", "secret")
	t.Setenv("DISCORD_INSTALL_REDIRECT_URI", "https://api.panda.example/discord/install/callback")
	t.Setenv("OPENROUTER_API_KEY", "key")
	t.Setenv("PUBLIC_APP_URL", "https://panda.example")
	t.Setenv("SOLANA_RPC_URL", "https://api.devnet.solana.com")
	t.Setenv("SOLANA_TREASURY_WALLET", "treasury-wallet")
	t.Setenv("SOLANA_STARTER_LAMPORTS", "19000000")
	t.Setenv("SOLANA_PLUS_LAMPORTS", "49000000")
	t.Setenv("SOLANA_PRO_LAMPORTS", "99000000")
	t.Setenv("SOLANA_BUSINESS_LAMPORTS", "249000000")

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
			"public_key": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"owner_user_ids": ["42", "77", "42"]
		},
		"openrouter": {
			"base_url": "https://openrouter.example/api/v1",
			"default_model": "provider/model",
			"classifier_model": "provider/classifier",
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
		"music": {
			"ytdlp_path": "/usr/local/bin/yt-dlp",
			"ffmpeg_path": "/usr/local/bin/ffmpeg",
			"sidecar_dir": "tmp-data/music-tools"
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
	if cfg.DiscordPublicKey != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected discord public key: %q", cfg.DiscordPublicKey)
	}
	if !cfg.IsOwner("42") || !cfg.IsOwner("77") {
		t.Fatalf("expected owner ids from config file, got %#v", cfg.OwnerUserIDs)
	}
	if cfg.OpenRouterBaseURL != "https://openrouter.example/api/v1" || cfg.OpenRouterModel != "provider/model" {
		t.Fatalf("unexpected OpenRouter routing config: base=%q model=%q", cfg.OpenRouterBaseURL, cfg.OpenRouterModel)
	}
	if cfg.OpenRouterClassifierModel != "provider/classifier" {
		t.Fatalf("unexpected classifier model %q", cfg.OpenRouterClassifierModel)
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
	if cfg.MusicYTDLPPath != "/usr/local/bin/yt-dlp" || cfg.MusicFFmpegPath != "/usr/local/bin/ffmpeg" {
		t.Fatalf("unexpected music paths: ytdlp=%q ffmpeg=%q", cfg.MusicYTDLPPath, cfg.MusicFFmpegPath)
	}
	if cfg.MusicSidecarDir != "tmp-data/music-tools" {
		t.Fatalf("unexpected music sidecar dir: %q", cfg.MusicSidecarDir)
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
			"classifier_model": "provider/classifier-file",
			"fallback_models": ["provider/file-fallback"]
		}
	}`)
	t.Setenv("PANDA_CONFIG", configPath)
	t.Setenv("DISCORD_APPLICATION_ID", "app-from-env")
	t.Setenv("DISCORD_PUBLIC_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("OPENROUTER_DEFAULT_MODEL", "provider/from-env")
	t.Setenv("OPENROUTER_CLASSIFIER_MODEL", "provider/classifier-env")
	t.Setenv("OPENROUTER_FALLBACK_MODELS", "provider/env-a,provider/env-b")
	t.Setenv("BRAVE_SEARCH_API_KEY", "brave-key")
	t.Setenv("BRAVE_SEARCH_BASE_URL", "https://brave-env.example/res/v1")
	t.Setenv("YTDLP_PATH", "/opt/bin/yt-dlp")
	t.Setenv("FFMPEG_PATH", "/opt/bin/ffmpeg")
	t.Setenv("MUSIC_SIDECAR_DIR", "/opt/panda/music-bin")
	t.Setenv("OWNER_USER_IDS", "99")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DiscordApplicationID != "app-from-env" {
		t.Fatalf("expected env application id, got %q", cfg.DiscordApplicationID)
	}
	if cfg.DiscordPublicKey != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("expected env public key, got %q", cfg.DiscordPublicKey)
	}
	if cfg.OpenRouterModel != "provider/from-env" {
		t.Fatalf("expected env default model, got %q", cfg.OpenRouterModel)
	}
	if cfg.OpenRouterClassifierModel != "provider/classifier-env" {
		t.Fatalf("expected env classifier model, got %q", cfg.OpenRouterClassifierModel)
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
	if cfg.MusicYTDLPPath != "/opt/bin/yt-dlp" || cfg.MusicFFmpegPath != "/opt/bin/ffmpeg" {
		t.Fatalf("expected env music paths, ytdlp=%q ffmpeg=%q", cfg.MusicYTDLPPath, cfg.MusicFFmpegPath)
	}
	if cfg.MusicSidecarDir != "/opt/panda/music-bin" {
		t.Fatalf("expected env music sidecar dir, got %q", cfg.MusicSidecarDir)
	}
}

func TestLoadEnvFile(t *testing.T) {
	clearConfigEnv(t)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "panda.config.json")
	envPath := filepath.Join(tempDir, ".env")
	writeConfigFile(t, configPath, `{
		"discord": {
			"application_id": "app-from-file",
			"owner_user_ids": ["42"]
		},
		"openrouter": {
			"default_model": "provider/from-file",
			"classifier_model": "provider/classifier-file",
			"fallback_models": ["provider/file-fallback"]
		},
		"runtime": {
			"port": "8088",
			"environment": "development"
		},
		"storage": {
			"sqlite_path": ":memory:"
		}
	}`)
	writeConfigFile(t, envPath, `
# comments and blank lines are ignored
export DISCORD_APPLICATION_ID=app-from-env-file
DISCORD_BOT_TOKEN="bot token"
OPENROUTER_API_KEY='router key'
OPENROUTER_DEFAULT_MODEL=provider/from-env-file
OPENROUTER_CLASSIFIER_MODEL=provider/classifier-env-file
OPENROUTER_FALLBACK_MODELS=provider/env-a, provider/env-b, provider/env-a
OWNER_USER_IDS=100, 200, 100
PORT=9099 # local port
USER_RATE_LIMIT=11
USER_RATE_LIMIT_WINDOW=90s
`)
	t.Setenv("PANDA_CONFIG", configPath)
	t.Setenv("PANDA_ENV_FILE", envPath)

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DiscordApplicationID != "app-from-env-file" || cfg.DiscordBotToken != "bot token" {
		t.Fatalf("expected env file discord settings, app=%q token=%q", cfg.DiscordApplicationID, cfg.DiscordBotToken)
	}
	if cfg.OpenRouterAPIKey != "router key" || cfg.OpenRouterModel != "provider/from-env-file" {
		t.Fatalf("expected env file OpenRouter settings, key=%q model=%q", cfg.OpenRouterAPIKey, cfg.OpenRouterModel)
	}
	if cfg.OpenRouterClassifierModel != "provider/classifier-env-file" {
		t.Fatalf("expected env file classifier model, got %q", cfg.OpenRouterClassifierModel)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/env-a" || cfg.OpenRouterFallbackModels[1] != "provider/env-b" {
		t.Fatalf("expected env file fallback models, got %#v", cfg.OpenRouterFallbackModels)
	}
	if cfg.IsOwner("42") || !cfg.IsOwner("100") || !cfg.IsOwner("200") {
		t.Fatalf("expected env file owner ids to override file ids, got %#v", cfg.OwnerUserIDs)
	}
	if cfg.Port != "9099" || cfg.UserRateLimit != 11 || cfg.UserRateLimitWindow.String() != "1m30s" {
		t.Fatalf("unexpected runtime overrides: port=%q limit=%d window=%s", cfg.Port, cfg.UserRateLimit, cfg.UserRateLimitWindow)
	}
}

func TestShellEnvOverridesEnvFile(t *testing.T) {
	clearConfigEnv(t)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "panda.config.json")
	envPath := filepath.Join(tempDir, ".env")
	writeConfigFile(t, configPath, `{
		"runtime": {
			"environment": "development"
		},
		"storage": {
			"sqlite_path": ":memory:"
		}
	}`)
	writeConfigFile(t, envPath, `
DISCORD_APPLICATION_ID=app-from-env-file
OPENROUTER_DEFAULT_MODEL=provider/from-env-file
OPENROUTER_CLASSIFIER_MODEL=provider/classifier-env-file
OWNER_USER_IDS=100
`)
	t.Setenv("PANDA_CONFIG", configPath)
	t.Setenv("PANDA_ENV_FILE", envPath)
	t.Setenv("DISCORD_APPLICATION_ID", "app-from-shell")
	t.Setenv("OPENROUTER_DEFAULT_MODEL", "provider/from-shell")
	t.Setenv("OPENROUTER_CLASSIFIER_MODEL", "provider/classifier-shell")
	t.Setenv("OWNER_USER_IDS", "200")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DiscordApplicationID != "app-from-shell" {
		t.Fatalf("expected shell env application id, got %q", cfg.DiscordApplicationID)
	}
	if cfg.OpenRouterModel != "provider/from-shell" {
		t.Fatalf("expected shell env default model, got %q", cfg.OpenRouterModel)
	}
	if cfg.OpenRouterClassifierModel != "provider/classifier-shell" {
		t.Fatalf("expected shell env classifier model, got %q", cfg.OpenRouterClassifierModel)
	}
	if cfg.IsOwner("100") || !cfg.IsOwner("200") {
		t.Fatalf("expected shell env owner ids to override env file ids, got %#v", cfg.OwnerUserIDs)
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

func TestLoadBillingPurchaseEnvOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("SQLITE_PATH", ":memory:")
	t.Setenv("PORT", "8081")
	t.Setenv("PUBLIC_APP_URL", "https://panda.example")
	t.Setenv("SOLANA_RPC_URL", "https://api.devnet.solana.com")
	t.Setenv("SOLANA_CLUSTER", "devnet")
	t.Setenv("SOLANA_TREASURY_WALLET", "treasury-wallet")
	t.Setenv("SOLANA_CONFIRMATION", "confirmed")
	t.Setenv("SOLANA_ORDER_EXPIRATION", "45m")
	t.Setenv("SOLANA_ACTIVATION_KEY_TTL", "24h")
	t.Setenv("BILLING_ALLOWED_ORIGINS", "https://panda.example, https://panda2-landing.fly.dev, https://panda.example")
	t.Setenv("SOLANA_PLAN_LAMPORTS", "starter:19000000,plus:49000000")
	t.Setenv("SOLANA_PRO_LAMPORTS", "99000000")
	t.Setenv("SOLANA_BUSINESS_LAMPORTS", "249000000")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SolanaRPCURL != "https://api.devnet.solana.com" || cfg.SolanaCluster != "devnet" || cfg.SolanaTreasuryWallet != "treasury-wallet" {
		t.Fatalf("unexpected Solana config: rpc=%q cluster=%q treasury=%q", cfg.SolanaRPCURL, cfg.SolanaCluster, cfg.SolanaTreasuryWallet)
	}
	if cfg.SolanaConfirmation != "confirmed" || cfg.SolanaOrderExpiration.String() != "45m0s" || cfg.SolanaActivationKeyTTL.String() != "24h0m0s" {
		t.Fatalf("unexpected Solana timing config: confirmation=%q order=%s key=%s", cfg.SolanaConfirmation, cfg.SolanaOrderExpiration, cfg.SolanaActivationKeyTTL)
	}
	if origins := cfg.PaymentAllowedOrigins(); len(origins) != 4 || origins[0] != "https://panda.example" || origins[1] != "https://panda2-landing.fly.dev" || origins[2] != "http://localhost:4321" || origins[3] != "http://127.0.0.1:4321" {
		t.Fatalf("unexpected billing allowed origins: %#v", origins)
	}
	if origins := cfg.InstallAllowedOrigins(); len(origins) != 4 || origins[0] != "https://panda.example" || origins[1] != "https://panda2-landing.fly.dev" || origins[2] != "http://localhost:4321" || origins[3] != "http://127.0.0.1:4321" {
		t.Fatalf("unexpected install allowed origins: %#v", origins)
	}
	if cfg.SolanaPlanLamports["starter"] != 19_000_000 || cfg.SolanaPlanLamports["plus"] != 49_000_000 || cfg.SolanaPlanLamports["pro"] != 99_000_000 || cfg.SolanaPlanLamports["business"] != 249_000_000 {
		t.Fatalf("unexpected SOL lamport map: %#v", cfg.SolanaPlanLamports)
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

func TestExplicitMissingEnvFileFails(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PANDA_ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))

	_, _, err := Load()
	if err == nil {
		t.Fatal("expected missing explicit env file to fail")
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PANDA_CONFIG",
		"PANDA_ENV_FILE",
		"DISCORD_BOT_TOKEN",
		"DISCORD_APPLICATION_ID",
		"DISCORD_GUILD_ID",
		"DISCORD_PUBLIC_KEY",
		"OPENROUTER_API_KEY",
		"OPENROUTER_BASE_URL",
		"OPENROUTER_DEFAULT_MODEL",
		"OPENROUTER_CLASSIFIER_MODEL",
		"OPENROUTER_FALLBACK_MODELS",
		"OPENROUTER_EMBEDDING_MODEL",
		"OPENROUTER_APP_URL",
		"OPENROUTER_APP_TITLE",
		"OPENROUTER_CIRCUIT_FAILURE_THRESHOLD",
		"OPENROUTER_CIRCUIT_COOLDOWN",
		"BRAVE_SEARCH_API_KEY",
		"BRAVE_SEARCH_BASE_URL",
		"PUBLIC_APP_URL",
		"BILLING_ALLOWED_ORIGINS",
		"SOLANA_RPC_URL",
		"SOLANA_CLUSTER",
		"SOLANA_TREASURY_WALLET",
		"SOLANA_CONFIRMATION",
		"SOLANA_ORDER_EXPIRATION",
		"SOLANA_ACTIVATION_KEY_TTL",
		"SOLANA_PLAN_LAMPORTS",
		"SOLANA_STARTER_LAMPORTS",
		"SOLANA_PLUS_LAMPORTS",
		"SOLANA_PRO_LAMPORTS",
		"SOLANA_BUSINESS_LAMPORTS",
		"YTDLP_PATH",
		"FFMPEG_PATH",
		"MUSIC_SIDECAR_DIR",
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
	t.Setenv("PANDA_ENV_FILE", "")
}

func writeConfigFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}
