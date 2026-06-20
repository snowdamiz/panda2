package http

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
	"github.com/sn0w/panda2/internal/store"
)

type Server struct {
	app            *fiber.App
	cfg            config.Config
	store          *store.Store
	discordWebhook DiscordWebhookHandler
}

type DiscordWebhookHandler interface {
	HandleWebhookEvent(ctx context.Context, event discordbot.WebhookEvent) error
}

type healthResponse struct {
	Status     string                     `json:"status"`
	Checks     map[string]componentStatus `json:"checks"`
	SQLitePath string                     `json:"sqlite_path"`
}

type componentStatus struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func New(cfg config.Config, store *store.Store) *Server {
	server := &Server{
		app: fiber.New(fiber.Config{
			AppName:     "panda-assistant",
			ReadTimeout: 5 * time.Second,
		}),
		cfg:   cfg,
		store: store,
	}
	server.routes()
	return server
}

func (s *Server) WithDiscordWebhookHandler(handler DiscordWebhookHandler) *Server {
	s.discordWebhook = handler
	return s
}

func (s *Server) Listen(addr string) error {
	return s.app.Listen(addr)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.app.ShutdownWithContext(ctx)
}

func (s *Server) Test(req *stdhttp.Request, timeout ...int) (*stdhttp.Response, error) {
	return s.app.Test(req, timeout...)
}

func (s *Server) routes() {
	s.app.Get("/healthz", s.health)
	s.app.Get("/readyz", s.ready)
	s.app.Get("/livez", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	s.app.Get("/metrics", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextPlainCharsetUTF8)
		return c.SendString(s.metrics(c.Context()))
	})
	s.app.Post("/discord/webhook-events", s.discordWebhookEvents)
}

func (s *Server) health(c *fiber.Ctx) error {
	response, statusCode := s.healthPayload(c.Context())
	return c.Status(statusCode).JSON(response)
}

func (s *Server) ready(c *fiber.Ctx) error {
	response, statusCode := s.healthPayload(c.Context())
	if statusCode == fiber.StatusOK && response.Checks["sqlite"].Status != "ok" {
		statusCode = fiber.StatusServiceUnavailable
		response.Status = "degraded"
	}
	return c.Status(statusCode).JSON(response)
}

func (s *Server) healthPayload(ctx context.Context) (healthResponse, int) {
	checks := map[string]componentStatus{
		"config":  {Status: "ok"},
		"fiber":   {Status: "ok"},
		"discord": configuredStatus(s.cfg.DiscordConfigured(), "credentials missing; gateway disabled"),
		"discord_webhook": configuredStatus(s.cfg.DiscordWebhookConfigured(),
			"public key missing; owner-only install webhooks disabled"),
		"openrouter":    configuredStatus(s.cfg.OpenRouterConfigured(), "api key missing; natural-language assistant disabled"),
		"brave_search":  configuredStatus(s.cfg.BraveSearchConfigured(), "api key missing; web search disabled"),
		"local_storage": localStorageStatus(s.cfg.DataDir),
	}

	if checks["local_storage"].Status == "error" {
		return healthResponse{Status: "degraded", Checks: checks, SQLitePath: s.cfg.SQLitePath}, fiber.StatusServiceUnavailable
	}

	if err := s.store.Ping(ctx); err != nil {
		checks["sqlite"] = componentStatus{Status: "error", Message: err.Error()}
		return healthResponse{Status: "degraded", Checks: checks, SQLitePath: s.cfg.SQLitePath}, fiber.StatusServiceUnavailable
	}
	checks["sqlite"] = componentStatus{Status: "ok"}

	return healthResponse{Status: "ok", Checks: checks, SQLitePath: s.cfg.SQLitePath}, fiber.StatusOK
}

func configuredStatus(ok bool, message string) componentStatus {
	if ok {
		return componentStatus{Status: "configured"}
	}
	return componentStatus{Status: "missing", Message: message}
}

func (s *Server) metrics(ctx context.Context) string {
	var builder strings.Builder
	sqliteUp := 1
	if err := s.store.Ping(ctx); err != nil {
		sqliteUp = 0
	}
	writeGauge(&builder, "panda_sqlite_up", "SQLite ping status", sqliteUp)
	writeGauge(&builder, "panda_discord_configured", "Discord credentials configured", boolInt(s.cfg.DiscordConfigured()))
	writeGauge(&builder, "panda_discord_webhook_configured", "Discord webhook public key configured", boolInt(s.cfg.DiscordWebhookConfigured()))
	writeGauge(&builder, "panda_discord_owner_install_enforced", "Discord owner-only install webhook enforcement enabled", boolInt(s.cfg.DiscordWebhookConfigured()))
	writeGauge(&builder, "panda_openrouter_configured", "OpenRouter API key configured", boolInt(s.cfg.OpenRouterConfigured()))
	writeGauge(&builder, "panda_brave_search_configured", "Brave Search API key configured", boolInt(s.cfg.BraveSearchConfigured()))

	var migrationVersion int
	_ = s.store.DB.Raw("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&migrationVersion).Error
	writeGauge(&builder, "panda_schema_migration_version", "Latest applied schema migration", migrationVersion)

	var queueDepth int64
	_ = s.store.DB.Table("jobs").Where("status = ?", "queued").Count(&queueDepth).Error
	writeGauge(&builder, "panda_queue_depth", "Queued background jobs", queueDepth)

	var usageTotal int64
	_ = s.store.DB.Table("usage_events").Count(&usageTotal).Error
	writeGauge(&builder, "panda_usage_events_total", "Recorded assistant usage events", usageTotal)

	var usageFailures int64
	_ = s.store.DB.Table("usage_events").Where("success = ?", false).Count(&usageFailures).Error
	writeGauge(&builder, "panda_usage_events_failed_total", "Failed assistant usage events", usageFailures)

	var discordEventsTotal int64
	_ = s.store.DB.Table("discord_events").Count(&discordEventsTotal).Error
	writeGauge(&builder, "panda_discord_events_total", "Recorded Discord gateway events", discordEventsTotal)

	var discordEventCacheSize int64
	_ = s.store.DB.Table("discord_events").Where("expires_at IS NULL OR expires_at > ?", time.Now().UTC()).Count(&discordEventCacheSize).Error
	writeGauge(&builder, "panda_discord_event_cache_size", "Non-expired Discord gateway events retained locally", discordEventCacheSize)

	var expiredDiscordEvents int64
	_ = s.store.DB.Table("discord_events").Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now().UTC()).Count(&expiredDiscordEvents).Error
	writeGauge(&builder, "panda_discord_event_cache_expired", "Expired Discord gateway events awaiting cleanup", expiredDiscordEvents)

	var latestDiscordEvent time.Time
	_ = s.store.DB.Raw("SELECT COALESCE(MAX(created_at), ?) FROM discord_events", time.Time{}).Scan(&latestDiscordEvent).Error
	lagSeconds := 0
	if !latestDiscordEvent.IsZero() {
		lagSeconds = int(time.Since(latestDiscordEvent).Seconds())
		if lagSeconds < 0 {
			lagSeconds = 0
		}
	}
	writeGauge(&builder, "panda_discord_event_lag_seconds", "Seconds since the newest retained Discord gateway event", lagSeconds)
	writeGauge(&builder, "panda_discord_event_dropped_total", "Discord gateway events dropped because local recording failed", discordbot.EventDropCount())
	writeGauge(&builder, "panda_discord_intent_guild_members_enabled", "Guild Members privileged intent enabled", 0)
	writeGauge(&builder, "panda_discord_intent_presences_enabled", "Presence privileged intent enabled", 0)
	writeGauge(&builder, "panda_discord_intent_message_content_enabled", "Message Content privileged intent requested when Discord is configured", boolInt(s.cfg.DiscordConfigured()))
	return builder.String()
}

type discordWebhookPayload struct {
	Version       int                  `json:"version"`
	ApplicationID string               `json:"application_id"`
	Type          int                  `json:"type"`
	Event         *discordWebhookEvent `json:"event"`
}

type discordWebhookEvent struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

func (s *Server) discordWebhookEvents(c *fiber.Ctx) error {
	body := c.BodyRaw()
	if err := verifyDiscordWebhookSignature(s.cfg.DiscordPublicKey, c.Get("X-Signature-Ed25519"), c.Get("X-Signature-Timestamp"), body, time.Now); err != nil {
		if errors.Is(err, errDiscordWebhookNotConfigured) {
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	var payload discordWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if s.cfg.DiscordApplicationID != "" && payload.ApplicationID != s.cfg.DiscordApplicationID {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	switch payload.Type {
	case 0:
		return c.SendStatus(fiber.StatusNoContent)
	case 1:
		if payload.Event == nil {
			return c.SendStatus(fiber.StatusBadRequest)
		}
		if s.discordWebhook == nil {
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		err := s.discordWebhook.HandleWebhookEvent(c.Context(), discordbot.WebhookEvent{
			Type:      payload.Event.Type,
			Timestamp: payload.Event.Timestamp,
			Data:      payload.Event.Data,
		})
		if err != nil {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		return c.SendStatus(fiber.StatusNoContent)
	default:
		return c.SendStatus(fiber.StatusBadRequest)
	}
}

var errDiscordWebhookNotConfigured = errors.New("discord webhook public key is not configured")

func verifyDiscordWebhookSignature(publicKeyHex, signatureHex, timestamp string, body []byte, now func() time.Time) error {
	publicKeyHex = strings.TrimSpace(publicKeyHex)
	if publicKeyHex == "" {
		return errDiscordWebhookNotConfigured
	}
	publicKey, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid discord public key")
	}
	signature, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid discord signature")
	}
	if err := validateDiscordWebhookTimestamp(timestamp, now); err != nil {
		return err
	}
	message := append([]byte(strings.TrimSpace(timestamp)), body...)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), message, signature) {
		return errors.New("invalid discord signature")
	}
	return nil
}

func validateDiscordWebhookTimestamp(timestamp string, now func() time.Time) error {
	seconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return errors.New("invalid discord signature timestamp")
	}
	signedAt := time.Unix(seconds, 0)
	current := now()
	if current.IsZero() {
		current = time.Now()
	}
	if signedAt.Before(current.Add(-5*time.Minute)) || signedAt.After(current.Add(5*time.Minute)) {
		return errors.New("discord signature timestamp is outside the accepted window")
	}
	return nil
}

func writeGauge(builder *strings.Builder, name, help string, value any) {
	fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
	fmt.Fprintf(builder, "# TYPE %s gauge\n", name)
	fmt.Fprintf(builder, "%s %v\n", name, value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func localStorageStatus(dir string) componentStatus {
	if dir == "" {
		return componentStatus{Status: "missing", Message: "data directory is not configured"}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return componentStatus{Status: "error", Message: err.Error()}
	}
	if !info.IsDir() {
		return componentStatus{Status: "error", Message: "data directory is not a directory"}
	}
	file, err := os.CreateTemp(dir, ".panda-health-*")
	if err != nil {
		return componentStatus{Status: "error", Message: err.Error()}
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return componentStatus{Status: "error", Message: err.Error()}
	}
	if err := os.Remove(name); err != nil {
		return componentStatus{Status: "error", Message: err.Error()}
	}
	return componentStatus{Status: "ok"}
}

func Address(port string) string {
	return fmt.Sprintf("0.0.0.0:%s", port)
}
