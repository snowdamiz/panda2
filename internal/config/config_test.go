package config

import (
	"os"
	"path/filepath"
	"strings"
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
	setRequiredProductionEnv(t)

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SQLitePath != "/data/panda.db" {
		t.Fatalf("expected /data/panda.db, got %q", cfg.SQLitePath)
	}
}

func TestProductionRejectsPublicAppURLWithInternalPort(t *testing.T) {
	clearConfigEnv(t)
	setRequiredProductionEnv(t)
	t.Setenv("PUBLIC_APP_URL", "https://pandaclanker.xyz:8080")
	t.Setenv("PORT", "8080")

	_, _, err := Load()
	if err == nil {
		t.Fatal("expected production config to reject PUBLIC_APP_URL with internal runtime port")
	}
	if !strings.Contains(err.Error(), "must not include internal runtime port") {
		t.Fatalf("unexpected error: %v", err)
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
				"image_base_url": "https://images.example/api/v1",
				"image_model": "provider/image-model",
				"clip_detection_model": "provider/clip-model",
				"clip_detection_timeout": "6m",
				"clip_detection_max_tokens": 11223,
				"clip_composition_model": "provider/composition-model",
				"clip_composition_timeout": "5m",
				"clip_composition_max_tokens": 12345,
				"image_timeout": "2m",
				"image_max_bytes": 1234567,
				"fallback_models": ["provider/fallback-a", "provider/fallback-b", "provider/fallback-a"],
			"provider_order": ["cerebras", "groq", "cerebras"],
			"allow_provider_fallbacks": true,
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
		"lemonfox": {
			"api_key": "lemon-from-file",
			"base_url": "https://lemon.example/v1",
			"youtube_audio_chunk_duration": "7m",
			"youtube_clip_min_duration": "6s",
			"youtube_clip_max_duration": "55s",
			"youtube_clip_max_bytes": 4567890,
			"youtube_clip_thumbnail_max_count": 6,
			"youtube_clip_thumbnail_max_edge": 640,
			"youtube_clip_vertical_resolution": "720x1280",
			"youtube_clip_landscape_resolution": "1280x720",
			"youtube_clip_caption_font_path": "/fonts/file-caption.ttf",
			"youtube_clip_caption_font_family": "File Caption"
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
			"sqlite_path": ":memory:",
			"r2_account_id": "account-from-file",
			"r2_endpoint": "https://r2-file.example",
			"r2_access_key_id": "r2-access-file",
			"r2_secret_access_key": "r2-secret-file",
			"r2_bucket": "clips-file",
			"r2_public_base_url": "https://cdn-file.example",
			"r2_clip_prefix": "file-clips"
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
	if cfg.OpenRouterImageBaseURL != "https://images.example/api/v1" || cfg.OpenRouterImageModel != "provider/image-model" {
		t.Fatalf("unexpected OpenRouter image config: base=%q model=%q", cfg.OpenRouterImageBaseURL, cfg.OpenRouterImageModel)
	}
	if cfg.OpenRouterClipDetectionModel != "provider/clip-model" {
		t.Fatalf("unexpected OpenRouter clip detection model: %q", cfg.OpenRouterClipDetectionModel)
	}
	if cfg.OpenRouterClipDetectionTimeout.String() != "6m0s" || cfg.OpenRouterClipDetectionMaxTokens != 11223 {
		t.Fatalf("unexpected OpenRouter clip detection config: timeout=%s max_tokens=%d", cfg.OpenRouterClipDetectionTimeout, cfg.OpenRouterClipDetectionMaxTokens)
	}
	if cfg.OpenRouterClipCompositionModel != "provider/composition-model" || cfg.OpenRouterClipCompositionTimeout.String() != "5m0s" || cfg.OpenRouterClipCompositionMaxTokens != 12345 {
		t.Fatalf("unexpected OpenRouter clip composition config: model=%q timeout=%s max_tokens=%d", cfg.OpenRouterClipCompositionModel, cfg.OpenRouterClipCompositionTimeout, cfg.OpenRouterClipCompositionMaxTokens)
	}
	if cfg.OpenRouterImageTimeout.String() != "2m0s" || cfg.OpenRouterImageMaxBytes != 1234567 {
		t.Fatalf("unexpected OpenRouter image limits: timeout=%s max_bytes=%d", cfg.OpenRouterImageTimeout, cfg.OpenRouterImageMaxBytes)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/fallback-a" || cfg.OpenRouterFallbackModels[1] != "provider/fallback-b" {
		t.Fatalf("unexpected fallback models %#v", cfg.OpenRouterFallbackModels)
	}
	if len(cfg.OpenRouterProviderOrder) != 2 || cfg.OpenRouterProviderOrder[0] != "cerebras" || cfg.OpenRouterProviderOrder[1] != "groq" {
		t.Fatalf("unexpected provider order %#v", cfg.OpenRouterProviderOrder)
	}
	if !cfg.OpenRouterAllowProviderFallbacks {
		t.Fatal("expected OpenRouter provider fallback setting from config file")
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
	if !cfg.LemonfoxConfigured() || cfg.LemonfoxAPIKey != "lemon-from-file" || cfg.LemonfoxBaseURL != "https://lemon.example/v1" || cfg.YouTubeAudioChunkDuration.String() != "7m0s" {
		t.Fatalf("unexpected Lemonfox config: configured=%t key=%q base=%q chunk=%s", cfg.LemonfoxConfigured(), cfg.LemonfoxAPIKey, cfg.LemonfoxBaseURL, cfg.YouTubeAudioChunkDuration)
	}
	if cfg.YouTubeClipMinDuration.String() != "6s" || cfg.YouTubeClipMaxDuration.String() != "55s" || cfg.YouTubeClipMaxBytes != 4567890 {
		t.Fatalf("unexpected YouTube clip limits: min=%s max=%s bytes=%d", cfg.YouTubeClipMinDuration, cfg.YouTubeClipMaxDuration, cfg.YouTubeClipMaxBytes)
	}
	if cfg.YouTubeClipThumbnailMaxCount != 6 || cfg.YouTubeClipThumbnailMaxEdge != 640 || cfg.YouTubeClipVerticalResolution != "720x1280" || cfg.YouTubeClipLandscapeResolution != "1280x720" {
		t.Fatalf("unexpected YouTube clip visual config: count=%d edge=%d vertical=%q landscape=%q", cfg.YouTubeClipThumbnailMaxCount, cfg.YouTubeClipThumbnailMaxEdge, cfg.YouTubeClipVerticalResolution, cfg.YouTubeClipLandscapeResolution)
	}
	if cfg.YouTubeClipCaptionFontPath != "/fonts/file-caption.ttf" || cfg.YouTubeClipCaptionFontFamily != "File Caption" {
		t.Fatalf("unexpected YouTube caption font config: path=%q family=%q", cfg.YouTubeClipCaptionFontPath, cfg.YouTubeClipCaptionFontFamily)
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
	if !cfg.R2Configured() || cfg.R2AccountID != "account-from-file" || cfg.R2Endpoint != "https://r2-file.example" || cfg.R2AccessKeyID != "r2-access-file" || cfg.R2SecretAccessKey != "r2-secret-file" || cfg.R2Bucket != "clips-file" || cfg.R2PublicBaseURL != "https://cdn-file.example" || cfg.R2ClipPrefix != "file-clips" {
		t.Fatalf("unexpected R2 config: configured=%t account=%q endpoint=%q access=%q secret=%q bucket=%q public=%q prefix=%q", cfg.R2Configured(), cfg.R2AccountID, cfg.R2Endpoint, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Bucket, cfg.R2PublicBaseURL, cfg.R2ClipPrefix)
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
				"image_model": "provider/image-from-file",
				"fallback_models": ["provider/file-fallback"],
			"provider_order": ["groq"],
			"allow_provider_fallbacks": true
		}
	}`)
	t.Setenv("PANDA_CONFIG", configPath)
	t.Setenv("DISCORD_APPLICATION_ID", "app-from-env")
	t.Setenv("DISCORD_PUBLIC_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("OPENROUTER_DEFAULT_MODEL", "provider/from-env")
	t.Setenv("OPENROUTER_API_KEY", "router-from-env")
	t.Setenv("OPENROUTER_IMAGE_BASE_URL", "https://images-env.example/api/v1")
	t.Setenv("OPENROUTER_IMAGE_MODEL", "provider/image-from-env")
	t.Setenv("OPENROUTER_CLIP_DETECTION_MODEL", "provider/clip-from-env")
	t.Setenv("OPENROUTER_CLIP_DETECTION_TIMEOUT", "8m")
	t.Setenv("OPENROUTER_CLIP_DETECTION_MAX_TOKENS", "9988")
	t.Setenv("OPENROUTER_CLIP_COMPOSITION_MODEL", "provider/composition-from-env")
	t.Setenv("OPENROUTER_CLIP_COMPOSITION_TIMEOUT", "7m")
	t.Setenv("OPENROUTER_CLIP_COMPOSITION_MAX_TOKENS", "9876")
	t.Setenv("OPENROUTER_IMAGE_TIMEOUT", "75s")
	t.Setenv("OPENROUTER_IMAGE_MAX_BYTES", "7654321")
	t.Setenv("OPENROUTER_FALLBACK_MODELS", "provider/env-a,provider/env-b")
	t.Setenv("OPENROUTER_PROVIDER_ORDER", "cerebras,groq,cerebras")
	t.Setenv("OPENROUTER_ALLOW_PROVIDER_FALLBACKS", "false")
	t.Setenv("BRAVE_SEARCH_API_KEY", "brave-key")
	t.Setenv("BRAVE_SEARCH_BASE_URL", "https://brave-env.example/res/v1")
	t.Setenv("LEMONFOX_API_KEY", "lemon-from-env")
	t.Setenv("LEMONFOX_BASE_URL", "https://lemon-env.example/v1")
	t.Setenv("YOUTUBE_AUDIO_CHUNK_DURATION", "4m")
	t.Setenv("YOUTUBE_CLIP_MIN_DURATION", "7s")
	t.Setenv("YOUTUBE_CLIP_MAX_DURATION", "42s")
	t.Setenv("YOUTUBE_CLIP_MAX_BYTES", "6789012")
	t.Setenv("YOUTUBE_CLIP_THUMBNAIL_MAX_COUNT", "5")
	t.Setenv("YOUTUBE_CLIP_THUMBNAIL_MAX_EDGE", "512")
	t.Setenv("YOUTUBE_CLIP_VERTICAL_RESOLUTION", "900x1600")
	t.Setenv("YOUTUBE_CLIP_LANDSCAPE_RESOLUTION", "1600x900")
	t.Setenv("YOUTUBE_CLIP_CAPTION_FONT_PATH", "/fonts/env-caption.ttf")
	t.Setenv("YOUTUBE_CLIP_CAPTION_FONT_FAMILY", "Env Caption")
	t.Setenv("YTDLP_PATH", "/opt/bin/yt-dlp")
	t.Setenv("FFMPEG_PATH", "/opt/bin/ffmpeg")
	t.Setenv("MUSIC_SIDECAR_DIR", "/opt/panda/music-bin")
	t.Setenv("R2_ACCOUNT_ID", "account-from-env")
	t.Setenv("R2_ENDPOINT", "https://r2-env.example")
	t.Setenv("R2_ACCESS_KEY_ID", "r2-access-env")
	t.Setenv("R2_SECRET_ACCESS_KEY", "r2-secret-env")
	t.Setenv("R2_BUCKET", "clips-env")
	t.Setenv("R2_PUBLIC_BASE_URL", "https://cdn-env.example")
	t.Setenv("R2_CLIP_PREFIX", "env-clips")
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
	if cfg.OpenRouterImageBaseURL != "https://images-env.example/api/v1" || cfg.OpenRouterImageModel != "provider/image-from-env" {
		t.Fatalf("expected env image settings, base=%q model=%q", cfg.OpenRouterImageBaseURL, cfg.OpenRouterImageModel)
	}
	if cfg.OpenRouterClipDetectionModel != "provider/clip-from-env" || !cfg.OpenRouterClipDetectionConfigured() {
		t.Fatalf("expected env clip model, got model=%q configured=%t", cfg.OpenRouterClipDetectionModel, cfg.OpenRouterClipDetectionConfigured())
	}
	if cfg.OpenRouterClipDetectionTimeout.String() != "8m0s" || cfg.OpenRouterClipDetectionMaxTokens != 9988 {
		t.Fatalf("expected env clip detection config, timeout=%s max_tokens=%d", cfg.OpenRouterClipDetectionTimeout, cfg.OpenRouterClipDetectionMaxTokens)
	}
	if cfg.OpenRouterClipCompositionModel != "provider/composition-from-env" || !cfg.OpenRouterClipCompositionConfigured() || cfg.OpenRouterClipCompositionTimeout.String() != "7m0s" || cfg.OpenRouterClipCompositionMaxTokens != 9876 {
		t.Fatalf("expected env clip composition config, model=%q configured=%t timeout=%s max_tokens=%d", cfg.OpenRouterClipCompositionModel, cfg.OpenRouterClipCompositionConfigured(), cfg.OpenRouterClipCompositionTimeout, cfg.OpenRouterClipCompositionMaxTokens)
	}
	if cfg.OpenRouterImageTimeout.String() != "1m15s" || cfg.OpenRouterImageMaxBytes != 7654321 {
		t.Fatalf("expected env image limits, timeout=%s max_bytes=%d", cfg.OpenRouterImageTimeout, cfg.OpenRouterImageMaxBytes)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/env-a" || cfg.OpenRouterFallbackModels[1] != "provider/env-b" {
		t.Fatalf("expected env fallback models, got %#v", cfg.OpenRouterFallbackModels)
	}
	if len(cfg.OpenRouterProviderOrder) != 2 || cfg.OpenRouterProviderOrder[0] != "cerebras" || cfg.OpenRouterProviderOrder[1] != "groq" {
		t.Fatalf("expected env provider order, got %#v", cfg.OpenRouterProviderOrder)
	}
	if cfg.OpenRouterAllowProviderFallbacks {
		t.Fatal("expected env provider fallback setting to override config file")
	}
	if cfg.IsOwner("42") || !cfg.IsOwner("99") {
		t.Fatalf("expected env owner ids to override file ids, got %#v", cfg.OwnerUserIDs)
	}
	if !cfg.BraveSearchConfigured() || cfg.BraveSearchBaseURL != "https://brave-env.example/res/v1" {
		t.Fatalf("expected env Brave Search settings, configured=%t base=%q", cfg.BraveSearchConfigured(), cfg.BraveSearchBaseURL)
	}
	if !cfg.LemonfoxConfigured() || cfg.LemonfoxAPIKey != "lemon-from-env" || cfg.LemonfoxBaseURL != "https://lemon-env.example/v1" || cfg.YouTubeAudioChunkDuration.String() != "4m0s" {
		t.Fatalf("expected env Lemonfox settings, configured=%t key=%q base=%q chunk=%s", cfg.LemonfoxConfigured(), cfg.LemonfoxAPIKey, cfg.LemonfoxBaseURL, cfg.YouTubeAudioChunkDuration)
	}
	if cfg.YouTubeClipMinDuration.String() != "7s" || cfg.YouTubeClipMaxDuration.String() != "42s" || cfg.YouTubeClipMaxBytes != 6789012 {
		t.Fatalf("expected env YouTube clip limits, min=%s max=%s bytes=%d", cfg.YouTubeClipMinDuration, cfg.YouTubeClipMaxDuration, cfg.YouTubeClipMaxBytes)
	}
	if cfg.YouTubeClipThumbnailMaxCount != 5 || cfg.YouTubeClipThumbnailMaxEdge != 512 || cfg.YouTubeClipVerticalResolution != "900x1600" || cfg.YouTubeClipLandscapeResolution != "1600x900" {
		t.Fatalf("expected env YouTube visual clip settings, count=%d edge=%d vertical=%q landscape=%q", cfg.YouTubeClipThumbnailMaxCount, cfg.YouTubeClipThumbnailMaxEdge, cfg.YouTubeClipVerticalResolution, cfg.YouTubeClipLandscapeResolution)
	}
	if cfg.YouTubeClipCaptionFontPath != "/fonts/env-caption.ttf" || cfg.YouTubeClipCaptionFontFamily != "Env Caption" {
		t.Fatalf("expected env YouTube caption font settings, path=%q family=%q", cfg.YouTubeClipCaptionFontPath, cfg.YouTubeClipCaptionFontFamily)
	}
	if cfg.MusicYTDLPPath != "/opt/bin/yt-dlp" || cfg.MusicFFmpegPath != "/opt/bin/ffmpeg" {
		t.Fatalf("expected env music paths, ytdlp=%q ffmpeg=%q", cfg.MusicYTDLPPath, cfg.MusicFFmpegPath)
	}
	if cfg.MusicSidecarDir != "/opt/panda/music-bin" {
		t.Fatalf("expected env music sidecar dir, got %q", cfg.MusicSidecarDir)
	}
	if !cfg.R2Configured() || cfg.R2AccountID != "account-from-env" || cfg.R2Endpoint != "https://r2-env.example" || cfg.R2AccessKeyID != "r2-access-env" || cfg.R2SecretAccessKey != "r2-secret-env" || cfg.R2Bucket != "clips-env" || cfg.R2PublicBaseURL != "https://cdn-env.example" || cfg.R2ClipPrefix != "env-clips" {
		t.Fatalf("expected env R2 settings, configured=%t account=%q endpoint=%q access=%q secret=%q bucket=%q public=%q prefix=%q", cfg.R2Configured(), cfg.R2AccountID, cfg.R2Endpoint, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Bucket, cfg.R2PublicBaseURL, cfg.R2ClipPrefix)
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
OPENROUTER_IMAGE_MODEL=provider/image-from-env-file
OPENROUTER_CLIP_DETECTION_TIMEOUT=9m
OPENROUTER_CLIP_DETECTION_MAX_TOKENS=8877
OPENROUTER_IMAGE_TIMEOUT=80s
OPENROUTER_IMAGE_MAX_BYTES=2345678
OPENROUTER_FALLBACK_MODELS=provider/env-a, provider/env-b, provider/env-a
OPENROUTER_PROVIDER_ORDER=cerebras, groq, cerebras
OPENROUTER_ALLOW_PROVIDER_FALLBACKS=true
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
	if cfg.OpenRouterImageModel != "provider/image-from-env-file" || cfg.OpenRouterImageTimeout.String() != "1m20s" || cfg.OpenRouterImageMaxBytes != 2345678 {
		t.Fatalf("expected env file OpenRouter image settings, model=%q timeout=%s max_bytes=%d", cfg.OpenRouterImageModel, cfg.OpenRouterImageTimeout, cfg.OpenRouterImageMaxBytes)
	}
	if cfg.OpenRouterClipDetectionTimeout.String() != "9m0s" || cfg.OpenRouterClipDetectionMaxTokens != 8877 {
		t.Fatalf("expected env file clip detection config, timeout=%s max_tokens=%d", cfg.OpenRouterClipDetectionTimeout, cfg.OpenRouterClipDetectionMaxTokens)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/env-a" || cfg.OpenRouterFallbackModels[1] != "provider/env-b" {
		t.Fatalf("expected env file fallback models, got %#v", cfg.OpenRouterFallbackModels)
	}
	if len(cfg.OpenRouterProviderOrder) != 2 || cfg.OpenRouterProviderOrder[0] != "cerebras" || cfg.OpenRouterProviderOrder[1] != "groq" {
		t.Fatalf("expected env file provider order, got %#v", cfg.OpenRouterProviderOrder)
	}
	if !cfg.OpenRouterAllowProviderFallbacks {
		t.Fatal("expected env file provider fallback setting")
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
OWNER_USER_IDS=100
`)
	t.Setenv("PANDA_CONFIG", configPath)
	t.Setenv("PANDA_ENV_FILE", envPath)
	t.Setenv("DISCORD_APPLICATION_ID", "app-from-shell")
	t.Setenv("OPENROUTER_DEFAULT_MODEL", "provider/from-shell")
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
	t.Setenv("OPENROUTER_IMAGE_MODEL", "provider/image")
	t.Setenv("OPENROUTER_IMAGE_TIMEOUT", "55s")
	t.Setenv("OPENROUTER_IMAGE_MAX_BYTES", "3456789")
	t.Setenv("OPENROUTER_FALLBACK_MODELS", "provider/a, provider/b, provider/a")
	t.Setenv("OPENROUTER_PROVIDER_ORDER", "cerebras, groq, cerebras")
	t.Setenv("OPENROUTER_ALLOW_PROVIDER_FALLBACKS", "true")
	t.Setenv("OPENROUTER_CIRCUIT_FAILURE_THRESHOLD", "3")
	t.Setenv("OPENROUTER_CIRCUIT_COOLDOWN", "45s")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.OpenRouterEmbeddingModel != "openai/text-embedding-3-small" {
		t.Fatalf("unexpected embedding model %q", cfg.OpenRouterEmbeddingModel)
	}
	if cfg.OpenRouterImageModel != "provider/image" || cfg.OpenRouterImageTimeout.String() != "55s" || cfg.OpenRouterImageMaxBytes != 3456789 {
		t.Fatalf("unexpected image config: model=%q timeout=%s max_bytes=%d", cfg.OpenRouterImageModel, cfg.OpenRouterImageTimeout, cfg.OpenRouterImageMaxBytes)
	}
	if len(cfg.OpenRouterFallbackModels) != 2 || cfg.OpenRouterFallbackModels[0] != "provider/a" || cfg.OpenRouterFallbackModels[1] != "provider/b" {
		t.Fatalf("unexpected fallback models %#v", cfg.OpenRouterFallbackModels)
	}
	if len(cfg.OpenRouterProviderOrder) != 2 || cfg.OpenRouterProviderOrder[0] != "cerebras" || cfg.OpenRouterProviderOrder[1] != "groq" {
		t.Fatalf("unexpected provider order %#v", cfg.OpenRouterProviderOrder)
	}
	if !cfg.OpenRouterAllowProviderFallbacks {
		t.Fatal("unexpected provider fallback setting")
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
	t.Setenv("SOLANA_USD_CENTS_PER_SOL", "7070")

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
	if cfg.SolanaPackLamports["starter"] != 19_000_000 || cfg.SolanaPackLamports["plus"] != 49_000_000 || cfg.SolanaUSDCentsPerSOL != 7070 {
		t.Fatalf("unexpected SOL pack pricing config: lamports=%#v cents=%d", cfg.SolanaPackLamports, cfg.SolanaUSDCentsPerSOL)
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
		"DISCORD_CLIENT_SECRET",
		"DISCORD_INSTALL_REDIRECT_URI",
		"OPENROUTER_API_KEY",
		"OPENROUTER_BASE_URL",
		"OPENROUTER_DEFAULT_MODEL",
		"OPENROUTER_IMAGE_BASE_URL",
		"OPENROUTER_IMAGE_MODEL",
		"OPENROUTER_CLIP_DETECTION_MODEL",
		"OPENROUTER_CLIP_DETECTION_TIMEOUT",
		"OPENROUTER_IMAGE_TIMEOUT",
		"OPENROUTER_IMAGE_MAX_BYTES",
		"OPENROUTER_FALLBACK_MODELS",
		"OPENROUTER_PROVIDER_ORDER",
		"OPENROUTER_ALLOW_PROVIDER_FALLBACKS",
		"OPENROUTER_EMBEDDING_MODEL",
		"OPENROUTER_APP_URL",
		"OPENROUTER_APP_TITLE",
		"OPENROUTER_CIRCUIT_FAILURE_THRESHOLD",
		"OPENROUTER_CIRCUIT_COOLDOWN",
		"BRAVE_SEARCH_API_KEY",
		"BRAVE_SEARCH_BASE_URL",
		"LEMONFOX_API_KEY",
		"LEMONFOX_BASE_URL",
		"YOUTUBE_AUDIO_CHUNK_DURATION",
		"YOUTUBE_CLIP_MIN_DURATION",
		"YOUTUBE_CLIP_MAX_DURATION",
		"YOUTUBE_CLIP_MAX_BYTES",
		"YOUTUBE_CLIP_THUMBNAIL_MAX_COUNT",
		"YOUTUBE_CLIP_THUMBNAIL_MAX_EDGE",
		"YOUTUBE_CLIP_VERTICAL_RESOLUTION",
		"YOUTUBE_CLIP_LANDSCAPE_RESOLUTION",
		"YOUTUBE_CLIP_CAPTION_FONT_PATH",
		"YOUTUBE_CLIP_CAPTION_FONT_FAMILY",
		"PUBLIC_APP_URL",
		"BILLING_ALLOWED_ORIGINS",
		"SOLANA_RPC_URL",
		"SOLANA_CLUSTER",
		"SOLANA_TREASURY_WALLET",
		"SOLANA_CONFIRMATION",
		"SOLANA_ORDER_EXPIRATION",
		"SOLANA_ACTIVATION_KEY_TTL",
		"SOLANA_PLAN_LAMPORTS",
		"SOLANA_PACK_LAMPORTS",
		"SOLANA_STARTER_LAMPORTS",
		"SOLANA_PLUS_LAMPORTS",
		"SOLANA_PRO_LAMPORTS",
		"SOLANA_BUSINESS_LAMPORTS",
		"SOLANA_PACK_STARTER_LAMPORTS",
		"SOLANA_PACK_PLUS_LAMPORTS",
		"SOLANA_PACK_PRO_LAMPORTS",
		"SOLANA_PACK_BUSINESS_LAMPORTS",
		"SOLANA_USD_CENTS_PER_SOL",
		"YTDLP_PATH",
		"FFMPEG_PATH",
		"MUSIC_SIDECAR_DIR",
		"R2_ACCOUNT_ID",
		"R2_ENDPOINT",
		"R2_ACCESS_KEY_ID",
		"R2_SECRET_ACCESS_KEY",
		"R2_BUCKET",
		"R2_PUBLIC_BASE_URL",
		"R2_CLIP_PREFIX",
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

func setRequiredProductionEnv(t *testing.T) {
	t.Helper()
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
}

func writeConfigFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}
