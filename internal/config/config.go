package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
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
	AttachmentCacheTTL                       time.Duration
	Port                                     string
	MetricsAddr                              string
	Environment                              string
	LogLevel                                 string
	OwnerUserIDs                             map[string]struct{}
	PublicBaseURL                            string
	UserRateLimit                            int
	UserRateLimitWindow                      time.Duration
}

func Load() (Config, []string, error) {
	cfg := Config{
		DiscordBotToken:                          strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN")),
		DiscordApplicationID:                     strings.TrimSpace(os.Getenv("DISCORD_APPLICATION_ID")),
		DiscordGuildID:                           strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID")),
		OpenRouterAPIKey:                         strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		OpenRouterBaseURL:                        firstNonEmpty(os.Getenv("OPENROUTER_BASE_URL"), "https://openrouter.ai/api/v1"),
		OpenRouterModel:                          firstNonEmpty(os.Getenv("OPENROUTER_DEFAULT_MODEL"), "openrouter/auto"),
		OpenRouterFallbackModels:                 parseCSVList(os.Getenv("OPENROUTER_FALLBACK_MODELS")),
		OpenRouterEmbeddingModel:                 strings.TrimSpace(os.Getenv("OPENROUTER_EMBEDDING_MODEL")),
		OpenRouterAppURL:                         strings.TrimSpace(os.Getenv("OPENROUTER_APP_URL")),
		OpenRouterAppTitle:                       firstNonEmpty(os.Getenv("OPENROUTER_APP_TITLE"), "Panda Assistant"),
		OpenRouterCircuitBreakerFailureThreshold: intFromEnv("OPENROUTER_CIRCUIT_FAILURE_THRESHOLD", 5),
		OpenRouterCircuitBreakerCooldown:         durationFromEnv("OPENROUTER_CIRCUIT_COOLDOWN", 30*time.Second),
		Port:                                     firstNonEmpty(os.Getenv("PORT"), "8080"),
		MetricsAddr:                              strings.TrimSpace(os.Getenv("METRICS_ADDR")),
		Environment:                              firstNonEmpty(os.Getenv("ENVIRONMENT"), "development"),
		LogLevel:                                 firstNonEmpty(os.Getenv("LOG_LEVEL"), "info"),
		OwnerUserIDs:                             parseCSVSet(os.Getenv("OWNER_USER_IDS")),
		PublicBaseURL:                            strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")),
		UserRateLimit:                            intFromEnv("USER_RATE_LIMIT", 5),
		UserRateLimitWindow:                      durationFromEnv("USER_RATE_LIMIT_WINDOW", time.Minute),
		AttachmentCacheTTL:                       durationFromEnv("ATTACHMENT_CACHE_TTL", 24*time.Hour),
	}

	cfg.DataDir = firstNonEmpty(os.Getenv("DATA_DIR"), defaultDataDir(cfg.Environment))
	cfg.SQLitePath = firstNonEmpty(os.Getenv("SQLITE_PATH"), cfg.DataDir+"/panda.db")

	warnings, err := cfg.Validate()
	return cfg, warnings, err
}

func (c Config) Validate() ([]string, error) {
	var warnings []string
	if c.Port == "" {
		return nil, errors.New("PORT must not be empty")
	}
	if _, err := strconv.Atoi(c.Port); err != nil {
		return nil, fmt.Errorf("PORT must be numeric: %w", err)
	}
	if c.SQLitePath == "" {
		return nil, errors.New("SQLITE_PATH must not be empty")
	}
	if c.OpenRouterBaseURL == "" {
		return nil, errors.New("OPENROUTER_BASE_URL must not be empty")
	}
	if c.OpenRouterModel == "" {
		return nil, errors.New("OPENROUTER_DEFAULT_MODEL must not be empty")
	}
	if c.OpenRouterCircuitBreakerFailureThreshold < 0 {
		return nil, errors.New("OPENROUTER_CIRCUIT_FAILURE_THRESHOLD must not be negative")
	}
	if c.OpenRouterCircuitBreakerFailureThreshold > 0 && c.OpenRouterCircuitBreakerCooldown <= 0 {
		return nil, errors.New("OPENROUTER_CIRCUIT_COOLDOWN must be greater than zero")
	}
	if c.UserRateLimit <= 0 {
		return nil, errors.New("USER_RATE_LIMIT must be greater than zero")
	}
	if c.UserRateLimitWindow <= 0 {
		return nil, errors.New("USER_RATE_LIMIT_WINDOW must be greater than zero")
	}
	if c.AttachmentCacheTTL <= 0 {
		return nil, errors.New("ATTACHMENT_CACHE_TTL must be greater than zero")
	}

	if !c.DiscordConfigured() {
		warnings = append(warnings, "Discord credentials are not fully configured; gateway and command registration will be skipped")
	}
	if !c.OpenRouterConfigured() {
		warnings = append(warnings, "OPENROUTER_API_KEY is not configured; /ask will return a setup message")
	}

	if strings.EqualFold(c.Environment, "production") {
		if !c.DiscordConfigured() {
			return warnings, errors.New("production requires DISCORD_BOT_TOKEN and DISCORD_APPLICATION_ID")
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

func defaultDataDir(environment string) string {
	if strings.EqualFold(environment, "production") || os.Getenv("FLY_APP_NAME") != "" {
		return defaultProdDataDir
	}
	return defaultDevDataDir
}

func firstNonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func parseCSVSet(value string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, part := range parseCSVList(value) {
		result[part] = struct{}{}
	}
	return result
}

func parseCSVList(value string) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}

func intFromEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
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
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
