package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	configPathEnv      = "PANDA_CONFIG"
	defaultConfigPath  = "panda.config.json"
	defaultDevDataDir  = "data"
	defaultProdDataDir = "/data"
)

type Config struct {
	DiscordBotToken                          string
	DiscordApplicationID                     string
	DiscordGuildID                           string
	OpenRouterAPIKey                         string
	OpenRouterBaseURL                        string
	OpenRouterModel                          string
	OpenRouterFallbackModels                 []string
	OpenRouterEmbeddingModel                 string
	OpenRouterAppURL                         string
	OpenRouterAppTitle                       string
	OpenRouterCircuitBreakerFailureThreshold int
	OpenRouterCircuitBreakerCooldown         time.Duration
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
	Discord    fileDiscordConfig    `json:"discord"`
	OpenRouter fileOpenRouterConfig `json:"openrouter"`
	Runtime    fileRuntimeConfig    `json:"runtime"`
	Storage    fileStorageConfig    `json:"storage"`
}

type fileDiscordConfig struct {
	ApplicationID string   `json:"application_id"`
	GuildID       string   `json:"guild_id"`
	OwnerUserIDs  []string `json:"owner_user_ids"`
}

type fileOpenRouterConfig struct {
	BaseURL        string                   `json:"base_url"`
	DefaultModel   string                   `json:"default_model"`
	FallbackModels []string                 `json:"fallback_models"`
	EmbeddingModel string                   `json:"embedding_model"`
	AppURL         string                   `json:"app_url"`
	AppTitle       string                   `json:"app_title"`
	CircuitBreaker fileCircuitBreakerConfig `json:"circuit_breaker"`
}

type fileCircuitBreakerConfig struct {
	FailureThreshold *int   `json:"failure_threshold"`
	Cooldown         string `json:"cooldown"`
}

type fileRuntimeConfig struct {
	Port                string `json:"port"`
	Environment         string `json:"environment"`
	LogLevel            string `json:"log_level"`
	UserRateLimit       *int   `json:"user_rate_limit"`
	UserRateLimitWindow string `json:"user_rate_limit_window"`
}

type fileStorageConfig struct {
	DataDir    string `json:"data_dir"`
	SQLitePath string `json:"sqlite_path"`
}

func Load() (Config, []string, error) {
	cfg := defaultConfig()
	if err := applyConfigFile(&cfg); err != nil {
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

	if !c.DiscordConfigured() {
		warnings = append(warnings, "Discord credentials are not fully configured; gateway and command registration will be skipped")
	}
	if !c.OpenRouterConfigured() {
		warnings = append(warnings, "OPENROUTER_API_KEY is not configured; natural-language assistant responses are disabled")
	}

	if strings.EqualFold(c.Environment, "production") {
		if !c.DiscordConfigured() {
			return warnings, errors.New("production requires DISCORD_BOT_TOKEN and a Discord application ID")
		}
		if !c.OpenRouterConfigured() {
			return warnings, errors.New("production requires OPENROUTER_API_KEY")
		}
	}

	return warnings, nil
}

func (c Config) DiscordConfigured() bool {
	return c.DiscordBotToken != "" && c.DiscordApplicationID != ""
}

func (c Config) OpenRouterConfigured() bool {
	return c.OpenRouterAPIKey != ""
}

func (c Config) IsOwner(userID string) bool {
	_, ok := c.OwnerUserIDs[userID]
	return ok
}

func defaultConfig() Config {
	return Config{
		OpenRouterBaseURL:                        "https://openrouter.ai/api/v1",
		OpenRouterModel:                          "openrouter/auto",
		OpenRouterAppTitle:                       "Panda Assistant",
		OpenRouterCircuitBreakerFailureThreshold: 5,
		OpenRouterCircuitBreakerCooldown:         30 * time.Second,
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

func applyFileConfig(cfg *Config, file fileConfig) error {
	if value := strings.TrimSpace(file.Discord.ApplicationID); value != "" {
		cfg.DiscordApplicationID = value
	}
	if value := strings.TrimSpace(file.Discord.GuildID); value != "" {
		cfg.DiscordGuildID = value
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
	if file.OpenRouter.FallbackModels != nil {
		cfg.OpenRouterFallbackModels = normalizeList(file.OpenRouter.FallbackModels)
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
	return nil
}

func applyEnv(cfg *Config) {
	cfg.DiscordBotToken = stringFromEnv("DISCORD_BOT_TOKEN", cfg.DiscordBotToken)
	cfg.DiscordApplicationID = nonEmptyStringFromEnv("DISCORD_APPLICATION_ID", cfg.DiscordApplicationID)
	cfg.DiscordGuildID = nonEmptyStringFromEnv("DISCORD_GUILD_ID", cfg.DiscordGuildID)
	cfg.OpenRouterAPIKey = stringFromEnv("OPENROUTER_API_KEY", cfg.OpenRouterAPIKey)
	cfg.OpenRouterBaseURL = nonEmptyStringFromEnv("OPENROUTER_BASE_URL", cfg.OpenRouterBaseURL)
	cfg.OpenRouterModel = nonEmptyStringFromEnv("OPENROUTER_DEFAULT_MODEL", cfg.OpenRouterModel)
	if value, ok := csvListFromEnv("OPENROUTER_FALLBACK_MODELS"); ok {
		cfg.OpenRouterFallbackModels = value
	}
	cfg.OpenRouterEmbeddingModel = nonEmptyStringFromEnv("OPENROUTER_EMBEDDING_MODEL", cfg.OpenRouterEmbeddingModel)
	cfg.OpenRouterAppURL = nonEmptyStringFromEnv("OPENROUTER_APP_URL", cfg.OpenRouterAppURL)
	cfg.OpenRouterAppTitle = nonEmptyStringFromEnv("OPENROUTER_APP_TITLE", cfg.OpenRouterAppTitle)
	cfg.OpenRouterCircuitBreakerFailureThreshold = intFromEnv("OPENROUTER_CIRCUIT_FAILURE_THRESHOLD", cfg.OpenRouterCircuitBreakerFailureThreshold)
	cfg.OpenRouterCircuitBreakerCooldown = durationFromEnv("OPENROUTER_CIRCUIT_COOLDOWN", cfg.OpenRouterCircuitBreakerCooldown)
	cfg.Port = nonEmptyStringFromEnv("PORT", cfg.Port)
	cfg.Environment = nonEmptyStringFromEnv("ENVIRONMENT", cfg.Environment)
	cfg.LogLevel = nonEmptyStringFromEnv("LOG_LEVEL", cfg.LogLevel)
	if value, ok := csvSetFromEnv("OWNER_USER_IDS"); ok {
		cfg.OwnerUserIDs = value
	}
	cfg.UserRateLimit = intFromEnv("USER_RATE_LIMIT", cfg.UserRateLimit)
	cfg.UserRateLimitWindow = durationFromEnv("USER_RATE_LIMIT_WINDOW", cfg.UserRateLimitWindow)
	cfg.DataDir = stringFromEnv("DATA_DIR", cfg.DataDir)
	cfg.SQLitePath = stringFromEnv("SQLITE_PATH", cfg.SQLitePath)
}

func finalize(cfg *Config) {
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir(cfg.Environment)
	}
	if cfg.SQLitePath == "" {
		cfg.SQLitePath = cfg.DataDir + "/panda.db"
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

func stringFromEnv(name string, current string) string {
	value, ok := os.LookupEnv(name)
	if !ok {
		return current
	}
	return strings.TrimSpace(value)
}

func nonEmptyStringFromEnv(name string, current string) string {
	value := stringFromEnv(name, current)
	if value == "" {
		return current
	}
	return value
}

func csvSetFromEnv(name string) (map[string]struct{}, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, false
	}
	return parseCSVSet(value), true
}

func csvListFromEnv(name string) ([]string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, false
	}
	return parseCSVList(value), true
}

func intFromEnv(name string, fallback int) int {
	value, ok := os.LookupEnv(name)
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

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(name)
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

func parseDuration(name, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	return parsed, nil
}
