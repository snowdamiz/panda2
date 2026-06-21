package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	configPathEnv          = "PANDA_CONFIG"
	envFilePathEnv         = "PANDA_ENV_FILE"
	defaultConfigPath      = "panda.config.json"
	defaultEnvFilePath     = ".env"
	defaultDevDataDir      = "data"
	defaultProdDataDir     = "/data"
	defaultOpenRouterModel = "deepseek/deepseek-v4-flash"
)

type Config struct {
	DiscordBotToken                          string
	DiscordApplicationID                     string
	DiscordGuildID                           string
	DiscordPublicKey                         string
	OpenRouterAPIKey                         string
	OpenRouterBaseURL                        string
	OpenRouterModel                          string
	OpenRouterFallbackModels                 []string
	OpenRouterEmbeddingModel                 string
	OpenRouterAppURL                         string
	OpenRouterAppTitle                       string
	OpenRouterCircuitBreakerFailureThreshold int
	OpenRouterCircuitBreakerCooldown         time.Duration
	BraveSearchAPIKey                        string
	BraveSearchBaseURL                       string
	MusicYTDLPPath                           string
	MusicFFmpegPath                          string
	MusicSidecarDir                          string
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
	Music       fileMusicConfig       `json:"music"`
	Runtime     fileRuntimeConfig     `json:"runtime"`
	Storage     fileStorageConfig     `json:"storage"`
}

type fileDiscordConfig struct {
	ApplicationID string   `json:"application_id"`
	GuildID       string   `json:"guild_id"`
	PublicKey     string   `json:"public_key"`
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

type fileBraveSearchConfig struct {
	BaseURL string `json:"base_url"`
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
	DataDir    string `json:"data_dir"`
	SQLitePath string `json:"sqlite_path"`
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
	if c.BraveSearchBaseURL == "" {
		return nil, errors.New("brave_search.base_url (BRAVE_SEARCH_BASE_URL) must not be empty")
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

	if !c.DiscordConfigured() {
		warnings = append(warnings, "Discord credentials are not fully configured; gateway and command registration will be skipped")
	}
	if c.DiscordPublicKey == "" {
		warnings = append(warnings, "DISCORD_PUBLIC_KEY is not configured; Discord owner-only install webhooks are disabled")
	}
	if c.DiscordPublicKey != "" && c.DiscordBotToken == "" {
		warnings = append(warnings, "DISCORD_PUBLIC_KEY is configured but DISCORD_BOT_TOKEN is missing; denied installs cannot be removed from guilds")
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

func (c Config) DiscordWebhookConfigured() bool {
	return c.DiscordPublicKey != ""
}

func (c Config) OpenRouterConfigured() bool {
	return c.OpenRouterAPIKey != ""
}

func (c Config) BraveSearchConfigured() bool {
	return c.BraveSearchAPIKey != ""
}

func (c Config) IsOwner(userID string) bool {
	_, ok := c.OwnerUserIDs[userID]
	return ok
}

func defaultConfig() Config {
	return Config{
		OpenRouterBaseURL:                        "https://openrouter.ai/api/v1",
		OpenRouterModel:                          defaultOpenRouterModel,
		OpenRouterAppTitle:                       "Panda Assistant",
		OpenRouterCircuitBreakerFailureThreshold: 5,
		OpenRouterCircuitBreakerCooldown:         30 * time.Second,
		BraveSearchBaseURL:                       "https://api.search.brave.com/res/v1",
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

	if value := strings.TrimSpace(file.BraveSearch.BaseURL); value != "" {
		cfg.BraveSearchBaseURL = value
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
	cfg.OpenRouterAPIKey = stringFromLookup(lookup, "OPENROUTER_API_KEY", cfg.OpenRouterAPIKey)
	cfg.OpenRouterBaseURL = nonEmptyStringFromLookup(lookup, "OPENROUTER_BASE_URL", cfg.OpenRouterBaseURL)
	cfg.OpenRouterModel = nonEmptyStringFromLookup(lookup, "OPENROUTER_DEFAULT_MODEL", cfg.OpenRouterModel)
	if value, ok := csvListFromLookup(lookup, "OPENROUTER_FALLBACK_MODELS"); ok {
		cfg.OpenRouterFallbackModels = value
	}
	cfg.OpenRouterEmbeddingModel = nonEmptyStringFromLookup(lookup, "OPENROUTER_EMBEDDING_MODEL", cfg.OpenRouterEmbeddingModel)
	cfg.OpenRouterAppURL = nonEmptyStringFromLookup(lookup, "OPENROUTER_APP_URL", cfg.OpenRouterAppURL)
	cfg.OpenRouterAppTitle = nonEmptyStringFromLookup(lookup, "OPENROUTER_APP_TITLE", cfg.OpenRouterAppTitle)
	cfg.OpenRouterCircuitBreakerFailureThreshold = intFromLookup(lookup, "OPENROUTER_CIRCUIT_FAILURE_THRESHOLD", cfg.OpenRouterCircuitBreakerFailureThreshold)
	cfg.OpenRouterCircuitBreakerCooldown = durationFromLookup(lookup, "OPENROUTER_CIRCUIT_COOLDOWN", cfg.OpenRouterCircuitBreakerCooldown)
	cfg.BraveSearchAPIKey = stringFromLookup(lookup, "BRAVE_SEARCH_API_KEY", cfg.BraveSearchAPIKey)
	cfg.BraveSearchBaseURL = nonEmptyStringFromLookup(lookup, "BRAVE_SEARCH_BASE_URL", cfg.BraveSearchBaseURL)
	cfg.MusicYTDLPPath = nonEmptyStringFromLookup(lookup, "YTDLP_PATH", cfg.MusicYTDLPPath)
	cfg.MusicFFmpegPath = nonEmptyStringFromLookup(lookup, "FFMPEG_PATH", cfg.MusicFFmpegPath)
	cfg.MusicSidecarDir = nonEmptyStringFromLookup(lookup, "MUSIC_SIDECAR_DIR", cfg.MusicSidecarDir)
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

func parseDuration(name, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	return parsed, nil
}
