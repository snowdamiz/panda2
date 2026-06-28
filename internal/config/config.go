package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	configPathEnv                 = "PANDA_CONFIG"
	envFilePathEnv                = "PANDA_ENV_FILE"
	defaultConfigPath             = "panda.config.json"
	defaultEnvFilePath            = ".env"
	defaultDevDataDir             = "data"
	defaultProdDataDir            = "/data"
	defaultOpenRouterModel        = "openai/gpt-oss-120b"
	defaultOpenRouterImageModel   = "google/gemini-3.1-flash-image"
	defaultOpenRouterProvider     = "cerebras"
	defaultClipDetectionTimeout   = 2 * time.Minute
	defaultClipDetectionTokens    = 8192
	defaultClipCompositionModel   = "google/gemini-3.5-flash"
	defaultClipCompositionTimeout = 2 * time.Minute
	defaultClipCompositionTokens  = 8192
	defaultLemonfoxBaseURL        = "https://api.lemonfox.ai/v1"
	defaultYouTubeChunkDuration   = 10 * time.Minute
	defaultYouTubeClipMinDuration = 5 * time.Second
	defaultYouTubeClipMaxDuration = 90 * time.Second
	defaultYouTubeClipMaxBytes    = 100 * 1024 * 1024
	defaultYouTubeThumbnailCount  = 12
	defaultYouTubeThumbnailEdge   = 720
	defaultYouTubeVerticalRes     = "1080x1920"
	defaultYouTubeLandscapeRes    = "1920x1080"
	defaultSolanaCluster          = "devnet"
	defaultSolanaConfirmation     = "finalized"
	defaultImageTimeout           = 90 * time.Second
	defaultImageMaxBytes          = 8 * 1024 * 1024
	defaultSolanaOrderExpiration  = 30 * time.Minute
	defaultSolanaActivationKeyTTL = 48 * time.Hour
)

var paidPlanNames = []string{"starter", "plus", "pro", "business"}

type Config struct {
	DiscordBotToken                          string
	DiscordApplicationID                     string
	DiscordGuildID                           string
	DiscordPublicKey                         string
	DiscordClientSecret                      string
	DiscordInstallRedirectURI                string
	OpenRouterAPIKey                         string
	OpenRouterBaseURL                        string
	OpenRouterModel                          string
	OpenRouterImageBaseURL                   string
	OpenRouterImageModel                     string
	OpenRouterClipDetectionModel             string
	OpenRouterClipDetectionTimeout           time.Duration
	OpenRouterClipDetectionMaxTokens         int
	OpenRouterClipCompositionModel           string
	OpenRouterClipCompositionTimeout         time.Duration
	OpenRouterClipCompositionMaxTokens       int
	OpenRouterImageTimeout                   time.Duration
	OpenRouterImageMaxBytes                  int64
	OpenRouterFallbackModels                 []string
	OpenRouterProviderOrder                  []string
	OpenRouterAllowProviderFallbacks         bool
	OpenRouterEmbeddingModel                 string
	OpenRouterAppURL                         string
	OpenRouterAppTitle                       string
	OpenRouterCircuitBreakerFailureThreshold int
	OpenRouterCircuitBreakerCooldown         time.Duration
	BraveSearchAPIKey                        string
	BraveSearchBaseURL                       string
	LemonfoxAPIKey                           string
	LemonfoxBaseURL                          string
	YouTubeAudioChunkDuration                time.Duration
	YouTubeClipMinDuration                   time.Duration
	YouTubeClipMaxDuration                   time.Duration
	YouTubeClipMaxBytes                      int64
	YouTubeClipThumbnailMaxCount             int
	YouTubeClipThumbnailMaxEdge              int
	YouTubeClipVerticalResolution            string
	YouTubeClipLandscapeResolution           string
	PublicAppURL                             string
	BillingAllowedOrigins                    []string
	SolanaRPCURL                             string
	SolanaCluster                            string
	SolanaTreasuryWallet                     string
	SolanaConfirmation                       string
	SolanaOrderExpiration                    time.Duration
	SolanaActivationKeyTTL                   time.Duration
	SolanaPlanLamports                       map[string]int64
	MusicYTDLPPath                           string
	MusicFFmpegPath                          string
	MusicSidecarDir                          string
	R2AccountID                              string
	R2Endpoint                               string
	R2AccessKeyID                            string
	R2SecretAccessKey                        string
	R2Bucket                                 string
	R2PublicBaseURL                          string
	R2ClipPrefix                             string
	SQLitePath                               string
	DataDir                                  string
	Port                                     string
	Environment                              string
	LogLevel                                 string
	OwnerUserIDs                             map[string]struct{}
	UserRateLimit                            int
	UserRateLimitWindow                      time.Duration
}

type fileConfig struct {
	Discord     fileDiscordConfig     `json:"discord"`
	OpenRouter  fileOpenRouterConfig  `json:"openrouter"`
	BraveSearch fileBraveSearchConfig `json:"brave_search"`
	Lemonfox    fileLemonfoxConfig    `json:"lemonfox"`
	Billing     fileBillingConfig     `json:"billing"`
	Music       fileMusicConfig       `json:"music"`
	Runtime     fileRuntimeConfig     `json:"runtime"`
	Storage     fileStorageConfig     `json:"storage"`
}

type fileDiscordConfig struct {
	ApplicationID      string   `json:"application_id"`
	GuildID            string   `json:"guild_id"`
	PublicKey          string   `json:"public_key"`
	ClientSecret       string   `json:"client_secret"`
	InstallRedirectURI string   `json:"install_redirect_uri"`
	OwnerUserIDs       []string `json:"owner_user_ids"`
}

type fileOpenRouterConfig struct {
	BaseURL        string                   `json:"base_url"`
	DefaultModel   string                   `json:"default_model"`
	ImageBaseURL   string                   `json:"image_base_url"`
	ImageModel     string                   `json:"image_model"`
	ClipModel      string                   `json:"clip_detection_model"`
	ClipTimeout    string                   `json:"clip_detection_timeout"`
	ClipTokens     *int                     `json:"clip_detection_max_tokens"`
	ComposeModel   string                   `json:"clip_composition_model"`
	ComposeTimeout string                   `json:"clip_composition_timeout"`
	ComposeTokens  *int                     `json:"clip_composition_max_tokens"`
	ImageTimeout   string                   `json:"image_timeout"`
	ImageMaxBytes  *int64                   `json:"image_max_bytes"`
	FallbackModels []string                 `json:"fallback_models"`
	ProviderOrder  []string                 `json:"provider_order"`
	AllowFallbacks *bool                    `json:"allow_provider_fallbacks"`
	EmbeddingModel string                   `json:"embedding_model"`
	AppURL         string                   `json:"app_url"`
	AppTitle       string                   `json:"app_title"`
	CircuitBreaker fileCircuitBreakerConfig `json:"circuit_breaker"`
}

type fileCircuitBreakerConfig struct {
	FailureThreshold *int   `json:"failure_threshold"`
	Cooldown         string `json:"cooldown"`
}

type fileBraveSearchConfig struct {
	BaseURL string `json:"base_url"`
}

type fileLemonfoxConfig struct {
	APIKey                     string `json:"api_key"`
	BaseURL                    string `json:"base_url"`
	YouTubeAudioChunkDuration  string `json:"youtube_audio_chunk_duration"`
	YouTubeClipMinDuration     string `json:"youtube_clip_min_duration"`
	YouTubeClipMaxDuration     string `json:"youtube_clip_max_duration"`
	YouTubeClipMaxBytes        *int64 `json:"youtube_clip_max_bytes"`
	YouTubeThumbnailMaxCount   *int   `json:"youtube_clip_thumbnail_max_count"`
	YouTubeThumbnailMaxEdge    *int   `json:"youtube_clip_thumbnail_max_edge"`
	YouTubeVerticalResolution  string `json:"youtube_clip_vertical_resolution"`
	YouTubeLandscapeResolution string `json:"youtube_clip_landscape_resolution"`
}

type fileBillingConfig struct {
	PublicURL             string           `json:"public_url"`
	AllowedOrigins        []string         `json:"allowed_origins"`
	SolanaRPCURL          string           `json:"solana_rpc_url"`
	SolanaCluster         string           `json:"solana_cluster"`
	SolanaTreasuryWallet  string           `json:"solana_treasury_wallet"`
	SolanaConfirmation    string           `json:"solana_confirmation"`
	SolanaOrderExpiration string           `json:"solana_order_expiration"`
	SolanaActivationTTL   string           `json:"solana_activation_key_ttl"`
	SolanaPlanLamports    map[string]int64 `json:"solana_plan_lamports"`
}

type fileMusicConfig struct {
	YTDLPPath  string `json:"ytdlp_path"`
	FFmpegPath string `json:"ffmpeg_path"`
	SidecarDir string `json:"sidecar_dir"`
}

type fileRuntimeConfig struct {
	Port                string `json:"port"`
	Environment         string `json:"environment"`
	LogLevel            string `json:"log_level"`
	UserRateLimit       *int   `json:"user_rate_limit"`
	UserRateLimitWindow string `json:"user_rate_limit_window"`
}

type fileStorageConfig struct {
	DataDir           string `json:"data_dir"`
	SQLitePath        string `json:"sqlite_path"`
	R2AccountID       string `json:"r2_account_id"`
	R2Endpoint        string `json:"r2_endpoint"`
	R2AccessKeyID     string `json:"r2_access_key_id"`
	R2SecretAccessKey string `json:"r2_secret_access_key"`
	R2Bucket          string `json:"r2_bucket"`
	R2PublicBaseURL   string `json:"r2_public_base_url"`
	R2ClipPrefix      string `json:"r2_clip_prefix"`
}

func Load() (Config, []string, error) {
	cfg := defaultConfig()
	if err := applyConfigFile(&cfg); err != nil {
		return cfg, nil, err
	}
	if err := applyEnvFile(&cfg); err != nil {
		return cfg, nil, err
	}
	applyEnv(&cfg)
	finalize(&cfg)

	warnings, err := cfg.Validate()
	return cfg, warnings, err
}

func (c Config) Validate() ([]string, error) {
	var warnings []string
	if c.Port == "" {
		return nil, errors.New("runtime.port (PORT) must not be empty")
	}
	if _, err := strconv.Atoi(c.Port); err != nil {
		return nil, fmt.Errorf("runtime.port (PORT) must be numeric: %w", err)
	}
	if c.SQLitePath == "" {
		return nil, errors.New("storage.sqlite_path (SQLITE_PATH) must not be empty")
	}
	if c.OpenRouterBaseURL == "" {
		return nil, errors.New("openrouter.base_url (OPENROUTER_BASE_URL) must not be empty")
	}
	if c.OpenRouterModel == "" {
		return nil, errors.New("openrouter.default_model (OPENROUTER_DEFAULT_MODEL) must not be empty")
	}
	if c.OpenRouterImageBaseURL == "" {
		return nil, errors.New("openrouter.image_base_url (OPENROUTER_IMAGE_BASE_URL) must not be empty")
	}
	if c.OpenRouterImageModel == "" {
		return nil, errors.New("openrouter.image_model (OPENROUTER_IMAGE_MODEL) must not be empty")
	}
	if c.OpenRouterClipDetectionTimeout <= 0 {
		return nil, errors.New("openrouter.clip_detection_timeout (OPENROUTER_CLIP_DETECTION_TIMEOUT) must be greater than zero")
	}
	if c.OpenRouterClipDetectionMaxTokens <= 0 {
		return nil, errors.New("openrouter.clip_detection_max_tokens (OPENROUTER_CLIP_DETECTION_MAX_TOKENS) must be greater than zero")
	}
	if c.OpenRouterClipCompositionTimeout <= 0 {
		return nil, errors.New("openrouter.clip_composition_timeout (OPENROUTER_CLIP_COMPOSITION_TIMEOUT) must be greater than zero")
	}
	if c.OpenRouterClipCompositionMaxTokens <= 0 {
		return nil, errors.New("openrouter.clip_composition_max_tokens (OPENROUTER_CLIP_COMPOSITION_MAX_TOKENS) must be greater than zero")
	}
	if c.OpenRouterImageTimeout <= 0 {
		return nil, errors.New("openrouter.image_timeout (OPENROUTER_IMAGE_TIMEOUT) must be greater than zero")
	}
	if c.OpenRouterImageMaxBytes <= 0 {
		return nil, errors.New("openrouter.image_max_bytes (OPENROUTER_IMAGE_MAX_BYTES) must be greater than zero")
	}
	if c.BraveSearchBaseURL == "" {
		return nil, errors.New("brave_search.base_url (BRAVE_SEARCH_BASE_URL) must not be empty")
	}
	if c.LemonfoxBaseURL == "" {
		return nil, errors.New("lemonfox.base_url (LEMONFOX_BASE_URL) must not be empty")
	}
	if c.YouTubeAudioChunkDuration <= 0 {
		return nil, errors.New("lemonfox.youtube_audio_chunk_duration (YOUTUBE_AUDIO_CHUNK_DURATION) must be greater than zero")
	}
	if c.YouTubeClipMinDuration <= 0 {
		return nil, errors.New("lemonfox.youtube_clip_min_duration (YOUTUBE_CLIP_MIN_DURATION) must be greater than zero")
	}
	if c.YouTubeClipMaxDuration <= 0 {
		return nil, errors.New("lemonfox.youtube_clip_max_duration (YOUTUBE_CLIP_MAX_DURATION) must be greater than zero")
	}
	if c.YouTubeClipMaxDuration < c.YouTubeClipMinDuration {
		return nil, errors.New("lemonfox.youtube_clip_max_duration (YOUTUBE_CLIP_MAX_DURATION) must be at least youtube_clip_min_duration")
	}
	if c.YouTubeClipMaxBytes <= 0 {
		return nil, errors.New("lemonfox.youtube_clip_max_bytes (YOUTUBE_CLIP_MAX_BYTES) must be greater than zero")
	}
	if c.YouTubeClipThumbnailMaxCount <= 0 {
		return nil, errors.New("lemonfox.youtube_clip_thumbnail_max_count (YOUTUBE_CLIP_THUMBNAIL_MAX_COUNT) must be greater than zero")
	}
	if c.YouTubeClipThumbnailMaxEdge <= 0 {
		return nil, errors.New("lemonfox.youtube_clip_thumbnail_max_edge (YOUTUBE_CLIP_THUMBNAIL_MAX_EDGE) must be greater than zero")
	}
	if err := validateResolution("lemonfox.youtube_clip_vertical_resolution", c.YouTubeClipVerticalResolution); err != nil {
		return nil, err
	}
	if err := validateResolution("lemonfox.youtube_clip_landscape_resolution", c.YouTubeClipLandscapeResolution); err != nil {
		return nil, err
	}
	if c.SolanaPlanLamports == nil {
		c.SolanaPlanLamports = map[string]int64{}
	}
	if c.MusicSidecarDir == "" {
		return nil, errors.New("music.sidecar_dir (MUSIC_SIDECAR_DIR) must not be empty")
	}
	if c.DiscordPublicKey != "" {
		decoded, err := hex.DecodeString(c.DiscordPublicKey)
		if err != nil || len(decoded) != 32 {
			return nil, errors.New("discord.public_key (DISCORD_PUBLIC_KEY) must be a 32-byte hex-encoded Ed25519 public key")
		}
	}
	if c.OpenRouterCircuitBreakerFailureThreshold < 0 {
		return nil, errors.New("openrouter.circuit_breaker.failure_threshold (OPENROUTER_CIRCUIT_FAILURE_THRESHOLD) must not be negative")
	}
	if c.OpenRouterCircuitBreakerFailureThreshold > 0 && c.OpenRouterCircuitBreakerCooldown <= 0 {
		return nil, errors.New("openrouter.circuit_breaker.cooldown (OPENROUTER_CIRCUIT_COOLDOWN) must be greater than zero")
	}
	if c.UserRateLimit <= 0 {
		return nil, errors.New("runtime.user_rate_limit (USER_RATE_LIMIT) must be greater than zero")
	}
	if c.UserRateLimitWindow <= 0 {
		return nil, errors.New("runtime.user_rate_limit_window (USER_RATE_LIMIT_WINDOW) must be greater than zero")
	}
	if strings.TrimSpace(c.SolanaCluster) == "" {
		return nil, errors.New("billing.solana_cluster (SOLANA_CLUSTER) must not be empty")
	}
	switch strings.ToLower(strings.TrimSpace(c.SolanaConfirmation)) {
	case "confirmed", "finalized":
	default:
		return nil, errors.New("billing.solana_confirmation (SOLANA_CONFIRMATION) must be confirmed or finalized")
	}
	if c.SolanaOrderExpiration <= 0 {
		return nil, errors.New("billing.solana_order_expiration (SOLANA_ORDER_EXPIRATION) must be greater than zero")
	}
	if c.SolanaActivationKeyTTL <= 0 {
		return nil, errors.New("billing.solana_activation_key_ttl (SOLANA_ACTIVATION_KEY_TTL) must be greater than zero")
	}
	if err := validateSolanaPlanLamports(c.SolanaPlanLamports); err != nil {
		return nil, err
	}

	if !c.DiscordConfigured() {
		warnings = append(warnings, "Discord credentials are not fully configured; gateway and command registration will be skipped")
	}
	if c.DiscordPublicKey == "" {
		warnings = append(warnings, "DISCORD_PUBLIC_KEY is not configured; Discord owner-only install webhooks are disabled")
	}
	if c.DiscordPublicKey != "" && c.DiscordBotToken == "" {
		warnings = append(warnings, "DISCORD_PUBLIC_KEY is configured but DISCORD_BOT_TOKEN is missing; denied installs cannot be removed from guilds")
	}
	if c.DiscordApplicationID != "" && c.DiscordClientSecret == "" {
		warnings = append(warnings, "DISCORD_CLIENT_SECRET is not configured; feature-based install OAuth callbacks are disabled")
	}
	if c.DiscordApplicationID != "" && c.DiscordInstallRedirectURI == "" {
		warnings = append(warnings, "DISCORD_INSTALL_REDIRECT_URI is not configured; feature-based install OAuth callbacks are disabled")
	}
	if !c.OpenRouterConfigured() {
		warnings = append(warnings, "OPENROUTER_API_KEY is not configured; natural-language assistant responses are disabled")
	}
	if !c.LemonfoxConfigured() {
		warnings = append(warnings, "LEMONFOX_API_KEY is not configured; YouTube video summarization is disabled")
	}
	if !c.OpenRouterClipDetectionConfigured() || !c.OpenRouterClipCompositionConfigured() || !c.R2Configured() {
		warnings = append(warnings, "YouTube clipping is not fully configured; set OPENROUTER_CLIP_DETECTION_MODEL, OPENROUTER_CLIP_COMPOSITION_MODEL, and R2 storage settings to enable clip links")
	}
	if c.PublicAppURL == "" {
		warnings = append(warnings, "PUBLIC_APP_URL is not configured; billing and support links will be limited")
	}
	if len(c.PaymentAllowedOrigins()) == 0 {
		warnings = append(warnings, "BILLING_ALLOWED_ORIGINS is not configured; cross-origin landing payments will be disabled")
	}
	if !c.SolanaPaymentsConfigured() {
		warnings = append(warnings, "SOL payment settings are incomplete; paid self-serve purchases cannot be verified")
	}

	if strings.EqualFold(c.Environment, "production") {
		if !c.DiscordConfigured() {
			return warnings, errors.New("production requires DISCORD_BOT_TOKEN and a Discord application ID")
		}
		if c.DiscordClientSecret == "" {
			return warnings, errors.New("production requires DISCORD_CLIENT_SECRET for feature-based installs")
		}
		if c.DiscordInstallRedirectURI == "" {
			return warnings, errors.New("production requires DISCORD_INSTALL_REDIRECT_URI for feature-based installs")
		}
		if !c.OpenRouterConfigured() {
			return warnings, errors.New("production requires OPENROUTER_API_KEY")
		}
		if c.PublicAppURL == "" {
			return warnings, errors.New("production requires PUBLIC_APP_URL")
		}
		if err := validateProductionPublicAppURL(c.PublicAppURL, c.Port); err != nil {
			return warnings, err
		}
		if c.SolanaRPCURL == "" {
			return warnings, errors.New("production SOL billing requires SOLANA_RPC_URL")
		}
		if c.SolanaTreasuryWallet == "" {
			return warnings, errors.New("production SOL billing requires SOLANA_TREASURY_WALLET")
		}
		if missing := missingSolanaPaidPlans(c.SolanaPlanLamports); len(missing) > 0 {
			return warnings, fmt.Errorf("production SOL billing requires lamports for paid plans: %s", strings.Join(missing, ", "))
		}
	}

	return warnings, nil
}

func validateProductionPublicAppURL(publicURL, runtimePort string) error {
	publicURL = strings.TrimSpace(publicURL)
	if publicURL == "" {
		return nil
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("PUBLIC_APP_URL must be an absolute URL, got %q", publicURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	if port := parsed.Port(); port != "" && port == strings.TrimSpace(runtimePort) {
		return fmt.Errorf("PUBLIC_APP_URL %q must not include internal runtime port %s for a non-local production host", publicURL, runtimePort)
	}
	return nil
}

func validateResolution(name string, value string) error {
	width, height, ok := parseResolution(value)
	if !ok || width <= 0 || height <= 0 {
		return fmt.Errorf("%s must be a resolution like 1080x1920", name)
	}
	if width%2 != 0 || height%2 != 0 {
		return fmt.Errorf("%s must use even width and height for H.264 output", name)
	}
	return nil
}

func parseResolution(value string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	height, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, false
	}
	return width, height, true
}

func (c Config) DiscordConfigured() bool {
	return c.DiscordBotToken != "" && c.DiscordApplicationID != ""
}

func (c Config) DiscordWebhookConfigured() bool {
	return c.DiscordPublicKey != ""
}

func (c Config) OpenRouterConfigured() bool {
	return c.OpenRouterAPIKey != ""
}

func (c Config) OpenRouterImagesConfigured() bool {
	return strings.TrimSpace(c.OpenRouterAPIKey) != "" &&
		strings.TrimSpace(c.OpenRouterImageBaseURL) != "" &&
		strings.TrimSpace(c.OpenRouterImageModel) != ""
}

func (c Config) OpenRouterClipDetectionConfigured() bool {
	return strings.TrimSpace(c.OpenRouterAPIKey) != "" &&
		strings.TrimSpace(c.OpenRouterClipDetectionModel) != ""
}

func (c Config) OpenRouterClipCompositionConfigured() bool {
	return strings.TrimSpace(c.OpenRouterAPIKey) != "" &&
		strings.TrimSpace(c.OpenRouterClipCompositionModel) != ""
}

func (c Config) BraveSearchConfigured() bool {
	return c.BraveSearchAPIKey != ""
}

func (c Config) LemonfoxConfigured() bool {
	return strings.TrimSpace(c.LemonfoxAPIKey) != ""
}

func (c Config) R2Configured() bool {
	return strings.TrimSpace(c.R2AccessKeyID) != "" &&
		strings.TrimSpace(c.R2SecretAccessKey) != "" &&
		strings.TrimSpace(c.R2Bucket) != "" &&
		strings.TrimSpace(c.R2PublicBaseURL) != "" &&
		(strings.TrimSpace(c.R2Endpoint) != "" || strings.TrimSpace(c.R2AccountID) != "")
}

func (c Config) SolanaPaymentsConfigured() bool {
	return strings.TrimSpace(c.SolanaRPCURL) != "" &&
		strings.TrimSpace(c.SolanaTreasuryWallet) != "" &&
		len(missingSolanaPaidPlans(c.SolanaPlanLamports)) == 0
}

func (c Config) PaymentAllowedOrigins() []string {
	origins := append([]string(nil), c.BillingAllowedOrigins...)
	if strings.TrimSpace(c.PublicAppURL) != "" {
		origins = append(origins, c.PublicAppURL)
	}
	if !strings.EqualFold(strings.TrimSpace(c.Environment), "production") {
		origins = append(origins, localLandingOrigins()...)
	}
	return normalizeList(origins)
}

func (c Config) InstallAllowedOrigins() []string {
	origins := append([]string(nil), c.PaymentAllowedOrigins()...)
	origins = append(origins, localLandingOrigins()...)
	return normalizeList(origins)
}

func localLandingOrigins() []string {
	return []string{"http://localhost:4321", "http://127.0.0.1:4321"}
}

func (c Config) IsOwner(userID string) bool {
	_, ok := c.OwnerUserIDs[userID]
	return ok
}

func defaultConfig() Config {
	return Config{
		OpenRouterBaseURL:                        "https://openrouter.ai/api/v1",
		OpenRouterModel:                          defaultOpenRouterModel,
		OpenRouterImageBaseURL:                   "https://openrouter.ai/api/v1",
		OpenRouterImageModel:                     defaultOpenRouterImageModel,
		OpenRouterClipDetectionTimeout:           defaultClipDetectionTimeout,
		OpenRouterClipDetectionMaxTokens:         defaultClipDetectionTokens,
		OpenRouterClipCompositionModel:           defaultClipCompositionModel,
		OpenRouterClipCompositionTimeout:         defaultClipCompositionTimeout,
		OpenRouterClipCompositionMaxTokens:       defaultClipCompositionTokens,
		OpenRouterImageTimeout:                   defaultImageTimeout,
		OpenRouterImageMaxBytes:                  defaultImageMaxBytes,
		OpenRouterProviderOrder:                  []string{defaultOpenRouterProvider},
		OpenRouterAllowProviderFallbacks:         true,
		OpenRouterAppTitle:                       "Panda Assistant",
		OpenRouterCircuitBreakerFailureThreshold: 5,
		OpenRouterCircuitBreakerCooldown:         30 * time.Second,
		BraveSearchBaseURL:                       "https://api.search.brave.com/res/v1",
		LemonfoxBaseURL:                          defaultLemonfoxBaseURL,
		YouTubeAudioChunkDuration:                defaultYouTubeChunkDuration,
		YouTubeClipMinDuration:                   defaultYouTubeClipMinDuration,
		YouTubeClipMaxDuration:                   defaultYouTubeClipMaxDuration,
		YouTubeClipMaxBytes:                      defaultYouTubeClipMaxBytes,
		YouTubeClipThumbnailMaxCount:             defaultYouTubeThumbnailCount,
		YouTubeClipThumbnailMaxEdge:              defaultYouTubeThumbnailEdge,
		YouTubeClipVerticalResolution:            defaultYouTubeVerticalRes,
		YouTubeClipLandscapeResolution:           defaultYouTubeLandscapeRes,
		R2ClipPrefix:                             "clips",
		SolanaCluster:                            defaultSolanaCluster,
		SolanaConfirmation:                       defaultSolanaConfirmation,
		SolanaOrderExpiration:                    defaultSolanaOrderExpiration,
		SolanaActivationKeyTTL:                   defaultSolanaActivationKeyTTL,
		SolanaPlanLamports:                       map[string]int64{},
		Port:                                     "8080",
		Environment:                              defaultEnvironment(),
		LogLevel:                                 "info",
		OwnerUserIDs:                             map[string]struct{}{},
		UserRateLimit:                            5,
		UserRateLimitWindow:                      time.Minute,
	}
}

func applyConfigFile(cfg *Config) error {
	path, required := configFilePath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if required {
			return fmt.Errorf("read %s %q: %w", configPathEnv, path, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config file %q: %w", path, err)
	}

	var file fileConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("parse config file %q: %w", path, err)
	}
	return applyFileConfig(cfg, file)
}

func configFilePath() (string, bool) {
	if value, ok := os.LookupEnv(configPathEnv); ok {
		value = strings.TrimSpace(value)
		if value != "" {
			return value, true
		}
	}
	return defaultConfigPath, false
}

func applyEnvFile(cfg *Config) error {
	path, required, enabled := envFilePath()
	if !enabled {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if required {
			return fmt.Errorf("read %s %q: %w", envFilePathEnv, path, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read env file %q: %w", path, err)
	}
	values, err := parseEnvFile(data)
	if err != nil {
		return fmt.Errorf("parse env file %q: %w", path, err)
	}
	applyEnvValues(cfg, func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	})
	return nil
}

func envFilePath() (string, bool, bool) {
	if value, ok := os.LookupEnv(envFilePathEnv); ok {
		value = strings.TrimSpace(value)
		if value == "" {
			return "", false, false
		}
		return value, true, true
	}
	return defaultEnvFilePath, false, true
}

func parseEnvFile(data []byte) (map[string]string, error) {
	values := map[string]string{}
	for index, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		key, value, ok, err := parseEnvLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", index+1, err)
		}
		if !ok {
			continue
		}
		values[key] = value
	}
	return values, nil
}

func parseEnvLine(line string) (string, string, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = stripEnvExport(line)

	index := strings.Index(line, "=")
	if index < 0 {
		return "", "", false, errors.New("expected KEY=value")
	}
	key := strings.TrimSpace(line[:index])
	if !isEnvKey(key) {
		return "", "", false, fmt.Errorf("invalid key %q", key)
	}
	value, err := parseEnvValue(strings.TrimSpace(line[index+1:]))
	if err != nil {
		return "", "", false, err
	}
	return key, value, true, nil
}

func stripEnvExport(line string) string {
	if len(line) <= len("export") || !strings.HasPrefix(line, "export") {
		return line
	}
	if !isEnvSpace(line[len("export")]) {
		return line
	}
	return strings.TrimSpace(line[len("export"):])
}

func parseEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '\'':
		return parseSingleQuotedEnvValue(value)
	case '"':
		return parseDoubleQuotedEnvValue(value)
	default:
		return parseUnquotedEnvValue(value), nil
	}
}

func parseSingleQuotedEnvValue(value string) (string, error) {
	for index := 1; index < len(value); index++ {
		if value[index] != '\'' {
			continue
		}
		if err := validateEnvValueSuffix(value[index+1:]); err != nil {
			return "", err
		}
		return value[1:index], nil
	}
	return "", errors.New("unterminated single-quoted value")
}

func parseDoubleQuotedEnvValue(value string) (string, error) {
	var result strings.Builder
	for index := 1; index < len(value); index++ {
		switch value[index] {
		case '"':
			if err := validateEnvValueSuffix(value[index+1:]); err != nil {
				return "", err
			}
			return result.String(), nil
		case '\\':
			if index+1 >= len(value) {
				return "", errors.New("unfinished escape sequence")
			}
			index++
			switch value[index] {
			case 'n':
				result.WriteByte('\n')
			case 'r':
				result.WriteByte('\r')
			case 't':
				result.WriteByte('\t')
			case '"', '\\', '#':
				result.WriteByte(value[index])
			default:
				result.WriteByte('\\')
				result.WriteByte(value[index])
			}
		default:
			result.WriteByte(value[index])
		}
	}
	return "", errors.New("unterminated double-quoted value")
}

func parseUnquotedEnvValue(value string) string {
	for index := range value {
		if value[index] == '#' && (index == 0 || isEnvSpace(value[index-1])) {
			return strings.TrimSpace(value[:index])
		}
	}
	return strings.TrimSpace(value)
}

func validateEnvValueSuffix(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "#") {
		return nil
	}
	return fmt.Errorf("unexpected trailing content %q", value)
}

func isEnvKey(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if index == 0 {
			if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' {
				continue
			}
			return false
		}
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func isEnvSpace(character byte) bool {
	return character == ' ' || character == '\t'
}

func applyFileConfig(cfg *Config, file fileConfig) error {
	if value := strings.TrimSpace(file.Discord.ApplicationID); value != "" {
		cfg.DiscordApplicationID = value
	}
	if value := strings.TrimSpace(file.Discord.GuildID); value != "" {
		cfg.DiscordGuildID = value
	}
	if value := strings.TrimSpace(file.Discord.PublicKey); value != "" {
		cfg.DiscordPublicKey = value
	}
	if value := strings.TrimSpace(file.Discord.ClientSecret); value != "" {
		cfg.DiscordClientSecret = value
	}
	if value := strings.TrimSpace(file.Discord.InstallRedirectURI); value != "" {
		cfg.DiscordInstallRedirectURI = value
	}
	if file.Discord.OwnerUserIDs != nil {
		cfg.OwnerUserIDs = listToSet(file.Discord.OwnerUserIDs)
	}

	if value := strings.TrimSpace(file.OpenRouter.BaseURL); value != "" {
		cfg.OpenRouterBaseURL = value
	}
	if value := strings.TrimSpace(file.OpenRouter.DefaultModel); value != "" {
		cfg.OpenRouterModel = value
	}
	if value := strings.TrimSpace(file.OpenRouter.ImageBaseURL); value != "" {
		cfg.OpenRouterImageBaseURL = value
	}
	if value := strings.TrimSpace(file.OpenRouter.ImageModel); value != "" {
		cfg.OpenRouterImageModel = value
	}
	if value := strings.TrimSpace(file.OpenRouter.ClipModel); value != "" {
		cfg.OpenRouterClipDetectionModel = value
	}
	if value := strings.TrimSpace(file.OpenRouter.ClipTimeout); value != "" {
		parsed, err := parseDuration("openrouter.clip_detection_timeout", value)
		if err != nil {
			return err
		}
		cfg.OpenRouterClipDetectionTimeout = parsed
	}
	if file.OpenRouter.ClipTokens != nil {
		cfg.OpenRouterClipDetectionMaxTokens = *file.OpenRouter.ClipTokens
	}
	if value := strings.TrimSpace(file.OpenRouter.ComposeModel); value != "" {
		cfg.OpenRouterClipCompositionModel = value
	}
	if value := strings.TrimSpace(file.OpenRouter.ComposeTimeout); value != "" {
		parsed, err := parseDuration("openrouter.clip_composition_timeout", value)
		if err != nil {
			return err
		}
		cfg.OpenRouterClipCompositionTimeout = parsed
	}
	if file.OpenRouter.ComposeTokens != nil {
		cfg.OpenRouterClipCompositionMaxTokens = *file.OpenRouter.ComposeTokens
	}
	if value := strings.TrimSpace(file.OpenRouter.ImageTimeout); value != "" {
		parsed, err := parseDuration("openrouter.image_timeout", value)
		if err != nil {
			return err
		}
		cfg.OpenRouterImageTimeout = parsed
	}
	if file.OpenRouter.ImageMaxBytes != nil {
		cfg.OpenRouterImageMaxBytes = *file.OpenRouter.ImageMaxBytes
	}
	if file.OpenRouter.FallbackModels != nil {
		cfg.OpenRouterFallbackModels = normalizeList(file.OpenRouter.FallbackModels)
	}
	if file.OpenRouter.ProviderOrder != nil {
		cfg.OpenRouterProviderOrder = normalizeList(file.OpenRouter.ProviderOrder)
	}
	if file.OpenRouter.AllowFallbacks != nil {
		cfg.OpenRouterAllowProviderFallbacks = *file.OpenRouter.AllowFallbacks
	}
	if value := strings.TrimSpace(file.OpenRouter.EmbeddingModel); value != "" {
		cfg.OpenRouterEmbeddingModel = value
	}
	if value := strings.TrimSpace(file.OpenRouter.AppURL); value != "" {
		cfg.OpenRouterAppURL = value
	}
	if value := strings.TrimSpace(file.OpenRouter.AppTitle); value != "" {
		cfg.OpenRouterAppTitle = value
	}
	if file.OpenRouter.CircuitBreaker.FailureThreshold != nil {
		cfg.OpenRouterCircuitBreakerFailureThreshold = *file.OpenRouter.CircuitBreaker.FailureThreshold
	}
	if value := strings.TrimSpace(file.OpenRouter.CircuitBreaker.Cooldown); value != "" {
		parsed, err := parseDuration("openrouter.circuit_breaker.cooldown", value)
		if err != nil {
			return err
		}
		cfg.OpenRouterCircuitBreakerCooldown = parsed
	}

	if value := strings.TrimSpace(file.BraveSearch.BaseURL); value != "" {
		cfg.BraveSearchBaseURL = value
	}
	if value := strings.TrimSpace(file.Lemonfox.APIKey); value != "" {
		cfg.LemonfoxAPIKey = value
	}
	if value := strings.TrimSpace(file.Lemonfox.BaseURL); value != "" {
		cfg.LemonfoxBaseURL = value
	}
	if value := strings.TrimSpace(file.Lemonfox.YouTubeAudioChunkDuration); value != "" {
		parsed, err := parseDuration("lemonfox.youtube_audio_chunk_duration", value)
		if err != nil {
			return err
		}
		cfg.YouTubeAudioChunkDuration = parsed
	}
	if value := strings.TrimSpace(file.Lemonfox.YouTubeClipMinDuration); value != "" {
		parsed, err := parseDuration("lemonfox.youtube_clip_min_duration", value)
		if err != nil {
			return err
		}
		cfg.YouTubeClipMinDuration = parsed
	}
	if value := strings.TrimSpace(file.Lemonfox.YouTubeClipMaxDuration); value != "" {
		parsed, err := parseDuration("lemonfox.youtube_clip_max_duration", value)
		if err != nil {
			return err
		}
		cfg.YouTubeClipMaxDuration = parsed
	}
	if file.Lemonfox.YouTubeClipMaxBytes != nil {
		cfg.YouTubeClipMaxBytes = *file.Lemonfox.YouTubeClipMaxBytes
	}
	if file.Lemonfox.YouTubeThumbnailMaxCount != nil {
		cfg.YouTubeClipThumbnailMaxCount = *file.Lemonfox.YouTubeThumbnailMaxCount
	}
	if file.Lemonfox.YouTubeThumbnailMaxEdge != nil {
		cfg.YouTubeClipThumbnailMaxEdge = *file.Lemonfox.YouTubeThumbnailMaxEdge
	}
	if value := strings.TrimSpace(file.Lemonfox.YouTubeVerticalResolution); value != "" {
		cfg.YouTubeClipVerticalResolution = value
	}
	if value := strings.TrimSpace(file.Lemonfox.YouTubeLandscapeResolution); value != "" {
		cfg.YouTubeClipLandscapeResolution = value
	}
	if value := strings.TrimSpace(file.Billing.PublicURL); value != "" {
		cfg.PublicAppURL = value
	}
	if file.Billing.AllowedOrigins != nil {
		cfg.BillingAllowedOrigins = normalizeList(file.Billing.AllowedOrigins)
	}
	if value := strings.TrimSpace(file.Billing.SolanaRPCURL); value != "" {
		cfg.SolanaRPCURL = value
	}
	if value := strings.TrimSpace(file.Billing.SolanaCluster); value != "" {
		cfg.SolanaCluster = value
	}
	if value := strings.TrimSpace(file.Billing.SolanaTreasuryWallet); value != "" {
		cfg.SolanaTreasuryWallet = value
	}
	if value := strings.TrimSpace(file.Billing.SolanaConfirmation); value != "" {
		cfg.SolanaConfirmation = strings.ToLower(value)
	}
	if value := strings.TrimSpace(file.Billing.SolanaOrderExpiration); value != "" {
		parsed, err := parseDuration("billing.solana_order_expiration", value)
		if err != nil {
			return err
		}
		cfg.SolanaOrderExpiration = parsed
	}
	if value := strings.TrimSpace(file.Billing.SolanaActivationTTL); value != "" {
		parsed, err := parseDuration("billing.solana_activation_key_ttl", value)
		if err != nil {
			return err
		}
		cfg.SolanaActivationKeyTTL = parsed
	}
	if file.Billing.SolanaPlanLamports != nil {
		cfg.SolanaPlanLamports = normalizeLamportsMap(file.Billing.SolanaPlanLamports)
	}

	if value := strings.TrimSpace(file.Music.YTDLPPath); value != "" {
		cfg.MusicYTDLPPath = value
	}
	if value := strings.TrimSpace(file.Music.FFmpegPath); value != "" {
		cfg.MusicFFmpegPath = value
	}
	if value := strings.TrimSpace(file.Music.SidecarDir); value != "" {
		cfg.MusicSidecarDir = value
	}

	if value := strings.TrimSpace(file.Runtime.Port); value != "" {
		cfg.Port = value
	}
	if value := strings.TrimSpace(file.Runtime.Environment); value != "" {
		cfg.Environment = value
	}
	if value := strings.TrimSpace(file.Runtime.LogLevel); value != "" {
		cfg.LogLevel = value
	}
	if file.Runtime.UserRateLimit != nil {
		cfg.UserRateLimit = *file.Runtime.UserRateLimit
	}
	if value := strings.TrimSpace(file.Runtime.UserRateLimitWindow); value != "" {
		parsed, err := parseDuration("runtime.user_rate_limit_window", value)
		if err != nil {
			return err
		}
		cfg.UserRateLimitWindow = parsed
	}

	if value := strings.TrimSpace(file.Storage.DataDir); value != "" {
		cfg.DataDir = value
	}
	if value := strings.TrimSpace(file.Storage.SQLitePath); value != "" {
		cfg.SQLitePath = value
	}
	if value := strings.TrimSpace(file.Storage.R2AccountID); value != "" {
		cfg.R2AccountID = value
	}
	if value := strings.TrimSpace(file.Storage.R2Endpoint); value != "" {
		cfg.R2Endpoint = value
	}
	if value := strings.TrimSpace(file.Storage.R2AccessKeyID); value != "" {
		cfg.R2AccessKeyID = value
	}
	if value := strings.TrimSpace(file.Storage.R2SecretAccessKey); value != "" {
		cfg.R2SecretAccessKey = value
	}
	if value := strings.TrimSpace(file.Storage.R2Bucket); value != "" {
		cfg.R2Bucket = value
	}
	if value := strings.TrimSpace(file.Storage.R2PublicBaseURL); value != "" {
		cfg.R2PublicBaseURL = value
	}
	if value := strings.TrimSpace(file.Storage.R2ClipPrefix); value != "" {
		cfg.R2ClipPrefix = value
	}
	return nil
}

func applyEnv(cfg *Config) {
	applyEnvValues(cfg, os.LookupEnv)
}

func applyEnvValues(cfg *Config, lookup func(string) (string, bool)) {
	cfg.DiscordBotToken = stringFromLookup(lookup, "DISCORD_BOT_TOKEN", cfg.DiscordBotToken)
	cfg.DiscordApplicationID = nonEmptyStringFromLookup(lookup, "DISCORD_APPLICATION_ID", cfg.DiscordApplicationID)
	cfg.DiscordGuildID = nonEmptyStringFromLookup(lookup, "DISCORD_GUILD_ID", cfg.DiscordGuildID)
	cfg.DiscordPublicKey = nonEmptyStringFromLookup(lookup, "DISCORD_PUBLIC_KEY", cfg.DiscordPublicKey)
	cfg.DiscordClientSecret = stringFromLookup(lookup, "DISCORD_CLIENT_SECRET", cfg.DiscordClientSecret)
	cfg.DiscordInstallRedirectURI = nonEmptyStringFromLookup(lookup, "DISCORD_INSTALL_REDIRECT_URI", cfg.DiscordInstallRedirectURI)
	cfg.OpenRouterAPIKey = stringFromLookup(lookup, "OPENROUTER_API_KEY", cfg.OpenRouterAPIKey)
	cfg.OpenRouterBaseURL = nonEmptyStringFromLookup(lookup, "OPENROUTER_BASE_URL", cfg.OpenRouterBaseURL)
	cfg.OpenRouterModel = nonEmptyStringFromLookup(lookup, "OPENROUTER_DEFAULT_MODEL", cfg.OpenRouterModel)
	cfg.OpenRouterImageBaseURL = nonEmptyStringFromLookup(lookup, "OPENROUTER_IMAGE_BASE_URL", cfg.OpenRouterImageBaseURL)
	cfg.OpenRouterImageModel = nonEmptyStringFromLookup(lookup, "OPENROUTER_IMAGE_MODEL", cfg.OpenRouterImageModel)
	cfg.OpenRouterClipDetectionModel = nonEmptyStringFromLookup(lookup, "OPENROUTER_CLIP_DETECTION_MODEL", cfg.OpenRouterClipDetectionModel)
	cfg.OpenRouterClipDetectionTimeout = durationFromLookup(lookup, "OPENROUTER_CLIP_DETECTION_TIMEOUT", cfg.OpenRouterClipDetectionTimeout)
	cfg.OpenRouterClipDetectionMaxTokens = intFromLookup(lookup, "OPENROUTER_CLIP_DETECTION_MAX_TOKENS", cfg.OpenRouterClipDetectionMaxTokens)
	cfg.OpenRouterClipCompositionModel = nonEmptyStringFromLookup(lookup, "OPENROUTER_CLIP_COMPOSITION_MODEL", cfg.OpenRouterClipCompositionModel)
	cfg.OpenRouterClipCompositionTimeout = durationFromLookup(lookup, "OPENROUTER_CLIP_COMPOSITION_TIMEOUT", cfg.OpenRouterClipCompositionTimeout)
	cfg.OpenRouterClipCompositionMaxTokens = intFromLookup(lookup, "OPENROUTER_CLIP_COMPOSITION_MAX_TOKENS", cfg.OpenRouterClipCompositionMaxTokens)
	cfg.OpenRouterImageTimeout = durationFromLookup(lookup, "OPENROUTER_IMAGE_TIMEOUT", cfg.OpenRouterImageTimeout)
	cfg.OpenRouterImageMaxBytes = int64FromLookup(lookup, "OPENROUTER_IMAGE_MAX_BYTES", cfg.OpenRouterImageMaxBytes)
	if value, ok := csvListFromLookup(lookup, "OPENROUTER_FALLBACK_MODELS"); ok {
		cfg.OpenRouterFallbackModels = value
	}
	if value, ok := csvListFromLookup(lookup, "OPENROUTER_PROVIDER_ORDER"); ok {
		cfg.OpenRouterProviderOrder = value
	}
	cfg.OpenRouterAllowProviderFallbacks = boolFromLookup(lookup, "OPENROUTER_ALLOW_PROVIDER_FALLBACKS", cfg.OpenRouterAllowProviderFallbacks)
	cfg.OpenRouterEmbeddingModel = nonEmptyStringFromLookup(lookup, "OPENROUTER_EMBEDDING_MODEL", cfg.OpenRouterEmbeddingModel)
	cfg.OpenRouterAppURL = nonEmptyStringFromLookup(lookup, "OPENROUTER_APP_URL", cfg.OpenRouterAppURL)
	cfg.OpenRouterAppTitle = nonEmptyStringFromLookup(lookup, "OPENROUTER_APP_TITLE", cfg.OpenRouterAppTitle)
	cfg.OpenRouterCircuitBreakerFailureThreshold = intFromLookup(lookup, "OPENROUTER_CIRCUIT_FAILURE_THRESHOLD", cfg.OpenRouterCircuitBreakerFailureThreshold)
	cfg.OpenRouterCircuitBreakerCooldown = durationFromLookup(lookup, "OPENROUTER_CIRCUIT_COOLDOWN", cfg.OpenRouterCircuitBreakerCooldown)
	cfg.BraveSearchAPIKey = stringFromLookup(lookup, "BRAVE_SEARCH_API_KEY", cfg.BraveSearchAPIKey)
	cfg.BraveSearchBaseURL = nonEmptyStringFromLookup(lookup, "BRAVE_SEARCH_BASE_URL", cfg.BraveSearchBaseURL)
	cfg.LemonfoxAPIKey = stringFromLookup(lookup, "LEMONFOX_API_KEY", cfg.LemonfoxAPIKey)
	cfg.LemonfoxBaseURL = nonEmptyStringFromLookup(lookup, "LEMONFOX_BASE_URL", cfg.LemonfoxBaseURL)
	cfg.YouTubeAudioChunkDuration = durationFromLookup(lookup, "YOUTUBE_AUDIO_CHUNK_DURATION", cfg.YouTubeAudioChunkDuration)
	cfg.YouTubeClipMinDuration = durationFromLookup(lookup, "YOUTUBE_CLIP_MIN_DURATION", cfg.YouTubeClipMinDuration)
	cfg.YouTubeClipMaxDuration = durationFromLookup(lookup, "YOUTUBE_CLIP_MAX_DURATION", cfg.YouTubeClipMaxDuration)
	cfg.YouTubeClipMaxBytes = int64FromLookup(lookup, "YOUTUBE_CLIP_MAX_BYTES", cfg.YouTubeClipMaxBytes)
	cfg.YouTubeClipThumbnailMaxCount = intFromLookup(lookup, "YOUTUBE_CLIP_THUMBNAIL_MAX_COUNT", cfg.YouTubeClipThumbnailMaxCount)
	cfg.YouTubeClipThumbnailMaxEdge = intFromLookup(lookup, "YOUTUBE_CLIP_THUMBNAIL_MAX_EDGE", cfg.YouTubeClipThumbnailMaxEdge)
	cfg.YouTubeClipVerticalResolution = nonEmptyStringFromLookup(lookup, "YOUTUBE_CLIP_VERTICAL_RESOLUTION", cfg.YouTubeClipVerticalResolution)
	cfg.YouTubeClipLandscapeResolution = nonEmptyStringFromLookup(lookup, "YOUTUBE_CLIP_LANDSCAPE_RESOLUTION", cfg.YouTubeClipLandscapeResolution)
	cfg.PublicAppURL = nonEmptyStringFromLookup(lookup, "PUBLIC_APP_URL", cfg.PublicAppURL)
	if value, ok := csvListFromLookup(lookup, "BILLING_ALLOWED_ORIGINS"); ok {
		cfg.BillingAllowedOrigins = value
	}
	cfg.SolanaRPCURL = nonEmptyStringFromLookup(lookup, "SOLANA_RPC_URL", cfg.SolanaRPCURL)
	cfg.SolanaCluster = nonEmptyStringFromLookup(lookup, "SOLANA_CLUSTER", cfg.SolanaCluster)
	cfg.SolanaTreasuryWallet = nonEmptyStringFromLookup(lookup, "SOLANA_TREASURY_WALLET", cfg.SolanaTreasuryWallet)
	cfg.SolanaConfirmation = strings.ToLower(nonEmptyStringFromLookup(lookup, "SOLANA_CONFIRMATION", cfg.SolanaConfirmation))
	cfg.SolanaOrderExpiration = durationFromLookup(lookup, "SOLANA_ORDER_EXPIRATION", cfg.SolanaOrderExpiration)
	cfg.SolanaActivationKeyTTL = durationFromLookup(lookup, "SOLANA_ACTIVATION_KEY_TTL", cfg.SolanaActivationKeyTTL)
	if value, ok := lamportsMapFromLookup(lookup, "SOLANA_PLAN_LAMPORTS"); ok {
		cfg.SolanaPlanLamports = value
	}
	applyPlanLamportsEnv(cfg.SolanaPlanLamports, lookup, "SOLANA_STARTER_LAMPORTS", "starter")
	applyPlanLamportsEnv(cfg.SolanaPlanLamports, lookup, "SOLANA_PLUS_LAMPORTS", "plus")
	applyPlanLamportsEnv(cfg.SolanaPlanLamports, lookup, "SOLANA_PRO_LAMPORTS", "pro")
	applyPlanLamportsEnv(cfg.SolanaPlanLamports, lookup, "SOLANA_BUSINESS_LAMPORTS", "business")
	cfg.MusicYTDLPPath = nonEmptyStringFromLookup(lookup, "YTDLP_PATH", cfg.MusicYTDLPPath)
	cfg.MusicFFmpegPath = nonEmptyStringFromLookup(lookup, "FFMPEG_PATH", cfg.MusicFFmpegPath)
	cfg.MusicSidecarDir = nonEmptyStringFromLookup(lookup, "MUSIC_SIDECAR_DIR", cfg.MusicSidecarDir)
	cfg.R2AccountID = nonEmptyStringFromLookup(lookup, "R2_ACCOUNT_ID", cfg.R2AccountID)
	cfg.R2Endpoint = nonEmptyStringFromLookup(lookup, "R2_ENDPOINT", cfg.R2Endpoint)
	cfg.R2AccessKeyID = nonEmptyStringFromLookup(lookup, "R2_ACCESS_KEY_ID", cfg.R2AccessKeyID)
	cfg.R2SecretAccessKey = stringFromLookup(lookup, "R2_SECRET_ACCESS_KEY", cfg.R2SecretAccessKey)
	cfg.R2Bucket = nonEmptyStringFromLookup(lookup, "R2_BUCKET", cfg.R2Bucket)
	cfg.R2PublicBaseURL = nonEmptyStringFromLookup(lookup, "R2_PUBLIC_BASE_URL", cfg.R2PublicBaseURL)
	cfg.R2ClipPrefix = nonEmptyStringFromLookup(lookup, "R2_CLIP_PREFIX", cfg.R2ClipPrefix)
	cfg.Port = nonEmptyStringFromLookup(lookup, "PORT", cfg.Port)
	cfg.Environment = nonEmptyStringFromLookup(lookup, "ENVIRONMENT", cfg.Environment)
	cfg.LogLevel = nonEmptyStringFromLookup(lookup, "LOG_LEVEL", cfg.LogLevel)
	if value, ok := csvSetFromLookup(lookup, "OWNER_USER_IDS"); ok {
		cfg.OwnerUserIDs = value
	}
	cfg.UserRateLimit = intFromLookup(lookup, "USER_RATE_LIMIT", cfg.UserRateLimit)
	cfg.UserRateLimitWindow = durationFromLookup(lookup, "USER_RATE_LIMIT_WINDOW", cfg.UserRateLimitWindow)
	cfg.DataDir = stringFromLookup(lookup, "DATA_DIR", cfg.DataDir)
	cfg.SQLitePath = stringFromLookup(lookup, "SQLITE_PATH", cfg.SQLitePath)
}

func finalize(cfg *Config) {
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir(cfg.Environment)
	}
	if cfg.SQLitePath == "" {
		cfg.SQLitePath = cfg.DataDir + "/panda.db"
	}
	if cfg.MusicSidecarDir == "" {
		cfg.MusicSidecarDir = cfg.DataDir + "/music-bin"
	}
	if cfg.OpenRouterImageBaseURL == "" {
		cfg.OpenRouterImageBaseURL = cfg.OpenRouterBaseURL
	}
}

func defaultEnvironment() string {
	if strings.TrimSpace(os.Getenv("FLY_APP_NAME")) != "" {
		return "production"
	}
	return "development"
}

func defaultDataDir(environment string) string {
	if strings.EqualFold(environment, "production") {
		return defaultProdDataDir
	}
	return defaultDevDataDir
}

func stringFromLookup(lookup func(string) (string, bool), name string, current string) string {
	value, ok := lookup(name)
	if !ok {
		return current
	}
	return strings.TrimSpace(value)
}

func nonEmptyStringFromLookup(lookup func(string) (string, bool), name string, current string) string {
	value := stringFromLookup(lookup, name, current)
	if value == "" {
		return current
	}
	return value
}

func csvSetFromLookup(lookup func(string) (string, bool), name string) (map[string]struct{}, bool) {
	value, ok := lookup(name)
	if !ok {
		return nil, false
	}
	return parseCSVSet(value), true
}

func csvListFromLookup(lookup func(string) (string, bool), name string) ([]string, bool) {
	value, ok := lookup(name)
	if !ok {
		return nil, false
	}
	return parseCSVList(value), true
}

func lamportsMapFromLookup(lookup func(string) (string, bool), name string) (map[string]int64, bool) {
	value, ok := lookup(name)
	if !ok {
		return nil, false
	}
	return parseLamportsMap(value), true
}

func applyPlanLamportsEnv(target map[string]int64, lookup func(string) (string, bool), name string, plan string) {
	if target == nil {
		return
	}
	value, ok := lookup(name)
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return
	}
	target[strings.ToLower(strings.TrimSpace(plan))] = parsed
}

func intFromLookup(lookup func(string) (string, bool), name string, fallback int) int {
	value, ok := lookup(name)
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func int64FromLookup(lookup func(string) (string, bool), name string, fallback int64) int64 {
	value, ok := lookup(name)
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func boolFromLookup(lookup func(string) (string, bool), name string, fallback bool) bool {
	value, ok := lookup(name)
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationFromLookup(lookup func(string) (string, bool), name string, fallback time.Duration) time.Duration {
	value, ok := lookup(name)
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseCSVSet(value string) map[string]struct{} {
	return listToSet(strings.Split(value, ","))
}

func parseCSVList(value string) []string {
	return normalizeList(strings.Split(value, ","))
}

func listToSet(values []string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, value := range normalizeList(values) {
		result[value] = struct{}{}
	}
	return result
}

func normalizeList(values []string) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeLamportsMap(values map[string]int64) map[string]int64 {
	result := map[string]int64{}
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		result[strings.ToLower(key)] = value
	}
	return result
}

func parseLamportsMap(value string) map[string]int64 {
	result := map[string]int64{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		plan, lamports, ok := strings.Cut(part, ":")
		if !ok {
			plan, lamports, ok = strings.Cut(part, "=")
		}
		if !ok {
			continue
		}
		plan = strings.ToLower(strings.TrimSpace(plan))
		parsed, err := strconv.ParseInt(strings.TrimSpace(lamports), 10, 64)
		if plan == "" || err != nil {
			continue
		}
		result[plan] = parsed
	}
	return result
}

func parseDuration(name, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	return parsed, nil
}

func validateSolanaPlanLamports(values map[string]int64) error {
	for plan, lamports := range values {
		if !isPaidPlanName(plan) {
			return fmt.Errorf("billing.solana_plan_lamports includes unknown paid plan %q", plan)
		}
		if lamports <= 0 {
			return fmt.Errorf("billing.solana_plan_lamports.%s must be greater than zero", plan)
		}
	}
	return nil
}

func missingSolanaPaidPlans(values map[string]int64) []string {
	var missing []string
	for _, plan := range paidPlanNames {
		if values == nil || values[plan] <= 0 {
			missing = append(missing, plan)
		}
	}
	return missing
}

func isPaidPlanName(plan string) bool {
	plan = strings.ToLower(strings.TrimSpace(plan))
	for _, paidPlan := range paidPlanNames {
		if plan == paidPlan {
			return true
		}
	}
	return false
}
