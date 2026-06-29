package http

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	stdhttp "net/http"
	stdurl "net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/runtimecontrol"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/urlutil"
)

type Server struct {
	app            *fiber.App
	cfg            config.Config
	store          *store.Store
	discordWebhook DiscordWebhookHandler
	install        InstallHandler
	billing        *billing.Service
	guilds         *repository.GuildRepository
	knowledge      *repository.KnowledgeRepository
	runtime        *runtimecontrol.Service
	paymentLimiter *ratelimit.Limiter
	adminAuth      adminAuthStore
}

type adminAuthStore struct {
	mu         sync.Mutex
	challenges map[string]adminChallenge
	sessions   map[string]adminSession
}

type adminChallenge struct {
	ID        string
	Wallet    string
	Message   string
	ExpiresAt time.Time
}

type adminSession struct {
	Wallet    string
	ExpiresAt time.Time
}

const (
	adminChallengeTTL = 5 * time.Minute
	adminSessionTTL   = 12 * time.Hour
)

type DiscordWebhookHandler interface {
	HandleWebhookEvent(ctx context.Context, event discordbot.WebhookEvent) error
}

type InstallHandler interface {
	CreateInstallIntent(ctx context.Context, request discordbot.CreateInstallIntentRequest) (discordbot.CreateInstallIntentResult, error)
	HandleOAuthCallback(ctx context.Context, request discordbot.InstallCallbackRequest) (discordbot.InstallCallbackResult, error)
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
		cfg:            cfg,
		store:          store,
		paymentLimiter: ratelimit.New(cfg.UserRateLimit, cfg.UserRateLimitWindow),
		adminAuth: adminAuthStore{
			challenges: make(map[string]adminChallenge),
			sessions:   make(map[string]adminSession),
		},
	}
	if store != nil {
		server.knowledge = repository.NewKnowledgeRepository(store.DB)
	}
	server.routes()
	return server
}

func (s *Server) WithDiscordWebhookHandler(handler DiscordWebhookHandler) *Server {
	s.discordWebhook = handler
	return s
}

func (s *Server) WithInstallHandler(handler InstallHandler) *Server {
	s.install = handler
	return s
}

func (s *Server) WithBillingService(service *billing.Service) *Server {
	s.billing = service
	return s
}

func (s *Server) WithGuildRepository(guilds *repository.GuildRepository) *Server {
	s.guilds = guilds
	return s
}

func (s *Server) WithRuntimeStatus(service *runtimecontrol.Service) *Server {
	s.runtime = service
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
	if origins := s.cfg.PaymentAllowedOrigins(); len(origins) > 0 {
		s.app.Use("/billing", cors.New(cors.Config{
			AllowOrigins: strings.Join(origins, ","),
			AllowMethods: strings.Join([]string{
				fiber.MethodGet,
				fiber.MethodPost,
				fiber.MethodOptions,
			}, ","),
			AllowHeaders: fiber.HeaderContentType,
			MaxAge:       300,
		}))
		s.app.Use("/admin", cors.New(cors.Config{
			AllowOrigins: strings.Join(origins, ","),
			AllowMethods: strings.Join([]string{
				fiber.MethodGet,
				fiber.MethodPost,
				fiber.MethodOptions,
			}, ","),
			AllowHeaders: strings.Join([]string{
				fiber.HeaderAuthorization,
				fiber.HeaderContentType,
			}, ","),
			MaxAge: 300,
		}))
	}
	if origins := s.cfg.InstallAllowedOrigins(); len(origins) > 0 {
		s.app.Use("/install", cors.New(cors.Config{
			AllowOrigins: strings.Join(origins, ","),
			AllowMethods: strings.Join([]string{
				fiber.MethodGet,
				fiber.MethodPost,
				fiber.MethodOptions,
			}, ","),
			AllowHeaders: fiber.HeaderContentType,
			MaxAge:       300,
		}))
	}

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
	s.app.Get("/install/features", s.installFeatures)
	s.app.Post("/install/intents", s.createInstallIntent)
	s.app.Get("/discord/install/callback", s.discordInstallCallback)
	s.app.Post("/admin/auth/challenge", s.createAdminAuthChallenge)
	s.app.Post("/admin/auth/sessions", s.createAdminSession)
	s.app.Get("/admin/runtime", s.getAdminRuntimeStatus)
	s.app.Post("/admin/runtime", s.updateAdminRuntimeStatus)
	s.app.Get("/admin/coupons", s.listAdminCoupons)
	s.app.Post("/admin/coupons", s.createAdminCoupon)
	s.app.Post("/admin/coupons/:coupon/revoke", s.revokeAdminCoupon)
	s.app.Get("/admin/guilds", s.listAdminGuilds)
	s.app.Post("/admin/guilds/:guild_id/credit-account", s.updateAdminGuildCreditAccount)
	s.app.Get("/billing/entitlements/:guild_id", s.getBillingEntitlement)
	s.app.Post("/billing/sol/orders", s.createSolPaymentOrder)
	s.app.Get("/billing/sol/orders/:order_id", s.getSolPaymentOrder)
	s.app.Post("/billing/sol/orders/:order_id/transaction", s.prepareSolPaymentTransaction)
	s.app.Post("/billing/sol/orders/:order_id/submit", s.submitSolPaymentTransaction)
	s.app.Post("/billing/sol/orders/:order_id/verify", s.verifySolPaymentOrder)
	s.app.Post("/billing/sol/orders/:order_id/activation-key", s.revealSolActivationKey)
}

type createInstallIntentRequest struct {
	FeatureIDs  []string       `json:"feature_ids"`
	Source      string         `json:"source"`
	DesiredPlan string         `json:"desired_plan"`
	Referrer    string         `json:"referrer"`
	Campaign    string         `json:"campaign"`
	Metadata    map[string]any `json:"metadata"`
}

func (s *Server) installFeatures(c *fiber.Ctx) error {
	defaultSelection, err := features.Calculate(features.DefaultInstallPreset(), true)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "feature_catalog_invalid"})
	}
	return c.JSON(map[string]any{
		"features":               features.PublicCatalog(),
		"default_feature_ids":    features.DefaultInstallPreset(),
		"default_selection":      defaultSelection,
		"default_install_scopes": features.DefaultInstallScopes(),
	})
}

func (s *Server) createInstallIntent(c *fiber.Ctx) error {
	if s.install == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	var request createInstallIntentRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	result, err := s.install.CreateInstallIntent(c.Context(), discordbot.CreateInstallIntentRequest{
		FeatureIDs:  request.FeatureIDs,
		Source:      request.Source,
		DesiredPlan: request.DesiredPlan,
		Referrer:    request.Referrer,
		Campaign:    request.Campaign,
		Metadata:    request.Metadata,
	})
	if err != nil {
		return writeInstallError(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(map[string]any{
		"intent_id":                   result.IntentID,
		"authorize_url":               result.AuthorizeURL,
		"expires_at":                  result.ExpiresAt.UTC().Format(time.RFC3339),
		"selected_feature_ids":        result.Selection.SelectedFeatureIDs,
		"expanded_feature_ids":        result.Selection.ExpandedFeatureIDs,
		"discord_permission_names":    result.Selection.DiscordPermissionNames,
		"discord_permission_bitfield": result.Selection.DiscordPermissionBitfield,
		"scopes":                      result.Selection.Scopes,
	})
}

func (s *Server) discordInstallCallback(c *fiber.Ctx) error {
	if s.install == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	result, err := s.install.HandleOAuthCallback(c.Context(), discordbot.InstallCallbackRequest{
		State:              c.Query("state"),
		Code:               c.Query("code"),
		GuildID:            c.Query("guild_id"),
		PermissionBitfield: c.Query("permissions"),
	})
	if err != nil {
		if redirectURL := installLocalDevelopmentSuccessRedirect(s.cfg.PublicAppURL, s.cfg.Environment, c.Query("guild_id")); redirectURL != "" {
			redirectURL = installRedirectLocation(redirectURL)
			slog.Warn("discord install callback redirect", "status", "local_development_success", "redirect_url", redirectURL, "guild_id", c.Query("guild_id"), "error_code", installErrorCode(err))
			return c.Redirect(redirectURL, fiber.StatusFound)
		}
		if redirectURL := installFailureRedirect(s.cfg.PublicAppURL, err); redirectURL != "" {
			redirectURL = installRedirectLocation(redirectURL)
			slog.Warn("discord install callback redirect", "status", "failed", "redirect_url", redirectURL, "guild_id", c.Query("guild_id"), "error_code", installErrorCode(err))
			return c.Redirect(redirectURL, fiber.StatusFound)
		}
		return c.JSON(map[string]any{
			"status": "failed",
			"error":  installErrorCode(err),
		})
	}
	if result.RedirectURL != "" {
		redirectURL := installRedirectLocation(result.RedirectURL)
		slog.Info("discord install callback redirect", "status", "success", "redirect_url", redirectURL, "guild_id", result.GuildID, "intent_id", result.IntentID)
		return c.Redirect(redirectURL, fiber.StatusFound)
	}
	return c.JSON(map[string]any{
		"status":            "success",
		"guild_id":          result.GuildID,
		"installer_user_id": result.InstallerUserID,
		"intent_id":         result.IntentID,
		"feature_ids":       result.FeatureIDs,
	})
}

func installLocalDevelopmentSuccessRedirect(publicURL, environment, guildID string) string {
	if strings.EqualFold(strings.TrimSpace(environment), "production") || !isLocalAppURL(publicURL) {
		return ""
	}
	return installSuccessRedirect(publicURL, guildID)
}

func isLocalAppURL(value string) bool {
	u, err := stdurl.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func installSuccessRedirect(publicURL, guildID string) string {
	return installResultRedirect(publicURL, "/install/success/", map[string]string{
		"guild_id": guildID,
		"status":   "success",
	})
}

func installFailureRedirect(publicURL string, err error) string {
	return installResultRedirect(publicURL, "/install/failed/", map[string]string{
		"error":  installErrorCode(err),
		"status": "failed",
	})
}

func installResultRedirect(publicURL, path string, values map[string]string) string {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u, err := stdurl.Parse(publicURL + path)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	q := u.Query()
	for key, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	urlutil.StripNonLocalPort(u)
	urlutil.EnsurePathTrailingSlash(u, "/install/success", "/install/failed")
	return u.String()
}

func installRedirectLocation(raw string) string {
	raw = urlutil.WithNonLocalPortStripped(raw)
	u, err := stdurl.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	urlutil.EnsurePathTrailingSlash(u, "/install/success", "/install/failed")
	return u.String()
}

func writeInstallError(c *fiber.Ctx, err error) error {
	code := installErrorCode(err)
	status := fiber.StatusBadRequest
	switch code {
	case "install_service_unavailable", "feature_store_unavailable":
		status = fiber.StatusServiceUnavailable
	case "install_intent_not_found":
		status = fiber.StatusNotFound
	case "install_intent_expired":
		status = fiber.StatusGone
	case "install_intent_unavailable":
		status = fiber.StatusConflict
	}
	return c.Status(status).JSON(map[string]string{"error": code})
}

func installErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, features.ErrUnknownFeature):
		return "unknown_feature"
	case errors.Is(err, features.ErrInternalFeature):
		return "internal_feature"
	case errors.Is(err, repository.ErrNotFound):
		return "install_intent_not_found"
	case errors.Is(err, repository.ErrInstallIntentExpired):
		return "install_intent_expired"
	case errors.Is(err, repository.ErrInstallIntentUnavailable):
		return "install_intent_unavailable"
	default:
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "feature repository") || strings.Contains(message, "feature store") {
			return "feature_store_unavailable"
		}
		if strings.Contains(message, "oauth client") || strings.Contains(message, "not configured") {
			return "install_service_unavailable"
		}
		return "install_failed"
	}
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
		"ai_service":       configuredStatus(s.cfg.OpenRouterConfigured(), "AI service key missing; natural-language assistant disabled"),
		"image_generation": configuredStatus(s.cfg.OpenRouterImagesConfigured(), "image model/API key missing; generated image responses disabled"),
		"brave_search":     configuredStatus(s.cfg.BraveSearchConfigured(), "api key missing; web search disabled"),
		"sol_payments":     configuredStatus(s.cfg.SolanaPaymentsConfigured(), "SOL payment settings incomplete; paid purchases disabled"),
		"local_storage":    localStorageStatus(s.cfg.DataDir),
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
	writeGauge(&builder, "panda_ai_service_configured", "AI service key configured", boolInt(s.cfg.OpenRouterConfigured()))
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

	s.writeComposedMetrics(&builder)

	var discordEventsTotal int64
	_ = s.store.DB.Table("discord_events").Count(&discordEventsTotal).Error
	writeGauge(&builder, "panda_discord_events_total", "Recorded Discord gateway events", discordEventsTotal)

	var discordEventCacheSize int64
	_ = s.store.DB.Table("discord_events").Where("expires_at IS NULL OR expires_at > ?", time.Now().UTC()).Count(&discordEventCacheSize).Error
	writeGauge(&builder, "panda_discord_event_cache_size", "Non-expired Discord gateway events retained locally", discordEventCacheSize)

	var expiredDiscordEvents int64
	_ = s.store.DB.Table("discord_events").Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now().UTC()).Count(&expiredDiscordEvents).Error
	writeGauge(&builder, "panda_discord_event_cache_expired", "Expired Discord gateway events awaiting cleanup", expiredDiscordEvents)

	var latestDiscordEventRow struct {
		CreatedAt time.Time
	}
	_ = s.store.DB.Table("discord_events").
		Select("created_at").
		Order("created_at DESC").
		Limit(1).
		Scan(&latestDiscordEventRow).Error
	latestDiscordEvent := latestDiscordEventRow.CreatedAt
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

func (s *Server) writeComposedMetrics(builder *strings.Builder) {
	var composedToolsTotal int64
	_ = s.store.DB.Table("composed_tools").Count(&composedToolsTotal).Error
	writeGauge(builder, "panda_composed_tools_total", "Composed tools recorded", composedToolsTotal)

	for _, action := range []struct {
		name   string
		help   string
		action string
	}{
		{"panda_composed_drafts_total", "Composed tool draft events recorded", "composed_tool.draft_created"},
		{"panda_composed_approvals_total", "Composed tool approval events recorded", "composed_tool.version_approved"},
		{"panda_composed_auto_pauses_total", "Composed tools auto-paused after repeated failures", "composed_tool.auto_paused"},
	} {
		var count int64
		_ = s.store.DB.Table("audit_events").Where("action = ?", action.action).Count(&count).Error
		writeGauge(builder, action.name, action.help, count)
	}

	var composedRunsTotal int64
	_ = s.store.DB.Table("composed_tool_runs").Count(&composedRunsTotal).Error
	writeGauge(builder, "panda_composed_runs_total", "Composed tool runs recorded", composedRunsTotal)

	var statusRows []struct {
		Status string
		Count  int64
	}
	_ = s.store.DB.Table("composed_tool_runs").Select("status, COUNT(*) AS count").Group("status").Scan(&statusRows).Error
	if len(statusRows) > 0 {
		writeGaugeHeader(builder, "panda_composed_runs_by_status_total", "Composed tool runs by terminal status")
		for _, row := range statusRows {
			status := sanitizeMetricLabel(row.Status)
			if status == "" {
				status = "unknown"
			}
			writeGaugeSampleWithLabels(builder, "panda_composed_runs_by_status_total", map[string]string{"status": status}, row.Count)
		}
	}

	var averageRunDuration float64
	_ = s.store.DB.Table("composed_tool_runs").
		Where("started_at IS NOT NULL AND finished_at IS NOT NULL").
		Select("COALESCE(AVG((julianday(finished_at) - julianday(started_at)) * 86400.0), 0)").
		Scan(&averageRunDuration).Error
	writeGauge(builder, "panda_composed_run_duration_seconds_avg", "Average composed tool run duration in seconds", averageRunDuration)

	var blockedRows []struct {
		Error string
		Count int64
	}
	_ = s.store.DB.Table("composed_tool_runs").
		Select("error, COUNT(*) AS count").
		Where("status IN ?", []string{"blocked", "rate_limited", "deduped", "skipped"}).
		Group("error").
		Scan(&blockedRows).Error
	if len(blockedRows) > 0 {
		writeGaugeHeader(builder, "panda_composed_blocked_runs_by_reason_total", "Composed tool blocked, skipped, deduped, or rate-limited runs by reason")
		for _, row := range blockedRows {
			reason := sanitizeMetricLabel(row.Error)
			if reason == "" {
				reason = "unspecified"
			}
			writeGaugeSampleWithLabels(builder, "panda_composed_blocked_runs_by_reason_total", map[string]string{"reason": reason}, row.Count)
		}
	}

	var scheduleSkips int64
	_ = s.store.DB.Table("schedules").Where("kind = ? AND last_status = ?", "composed", "skipped").Count(&scheduleSkips).Error
	writeGauge(builder, "panda_composed_schedule_skips_total", "Composed schedules currently reporting a skipped last run", scheduleSkips)

	var eventDedupes int64
	_ = s.store.DB.Table("composed_tool_dedupes").Where("expires_at > ?", time.Now().UTC()).Count(&eventDedupes).Error
	writeGauge(builder, "panda_composed_event_dedupe_active", "Active composed event dedupe fingerprints", eventDedupes)
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

type createSolPaymentOrderRequest struct {
	GuildID            string `json:"guild_id"`
	BillingOwnerUserID string `json:"billing_owner_user_id"`
	Pack               string `json:"pack"`
	Plan               string `json:"plan"`
	SupportEmail       string `json:"support_email"`
	CouponCode         string `json:"coupon_code"`
}

type verifySolPaymentRequest struct {
	Signature string `json:"signature"`
}

type prepareSolPaymentTransactionRequest struct {
	PayerWallet string `json:"payer_wallet"`
}

type submitSolPaymentTransactionRequest struct {
	SignedTransaction string `json:"signed_transaction"`
}

type adminAuthChallengeRequest struct {
	Wallet string `json:"wallet"`
}

type adminAuthChallengeResponse struct {
	ChallengeID    string    `json:"challenge_id"`
	Message        string    `json:"message"`
	ExpiresAt      time.Time `json:"expires_at"`
	TreasuryWallet string    `json:"treasury_wallet"`
}

type adminSessionRequest struct {
	ChallengeID   string `json:"challenge_id"`
	Wallet        string `json:"wallet"`
	Signature     string `json:"signature"`
	SignedMessage string `json:"signed_message"`
}

type adminSessionResponse struct {
	SessionToken string    `json:"session_token"`
	Wallet       string    `json:"wallet"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type adminRuntimeStatusResponse struct {
	Disabled         bool      `json:"disabled"`
	Message          string    `json:"message"`
	DefaultMessage   string    `json:"default_message"`
	EffectiveMessage string    `json:"effective_message"`
	UpdatedBy        string    `json:"updated_by"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type updateAdminRuntimeStatusRequest struct {
	Disabled bool   `json:"disabled"`
	Message  string `json:"message"`
}

type createAdminCouponRequest struct {
	Pack             string `json:"pack"`
	Plan             string `json:"plan"`
	DiscountLamports int64  `json:"discount_lamports"`
	CouponCode       string `json:"coupon_code"`
	MaxRedemptions   int    `json:"max_redemptions"`
	ExpiresAt        string `json:"expires_at"`
	Note             string `json:"note"`
}

type adminCouponListResponse struct {
	Coupons      []billing.CouponView `json:"coupons"`
	PackLamports map[string]int64     `json:"pack_lamports"`
}

type adminCouponCreateResponse struct {
	Coupon billing.CouponView `json:"coupon"`
	Code   string             `json:"code"`
}

func (s *Server) createAdminAuthChallenge(c *fiber.Ctx) error {
	treasuryWallet := strings.TrimSpace(s.cfg.SolanaTreasuryWallet)
	if treasuryWallet == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "admin_wallet_not_configured"})
	}
	var request adminAuthChallengeRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	wallet := strings.TrimSpace(request.Wallet)
	if wallet == "" {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "wallet_required"})
	}
	if wallet != treasuryWallet {
		return c.Status(fiber.StatusForbidden).JSON(map[string]string{"error": "admin_wallet_forbidden"})
	}
	if _, err := decodeSolanaPublicKey(wallet); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "invalid_wallet"})
	}
	challengeID, err := randomAdminToken()
	if err != nil {
		return c.SendStatus(fiber.StatusInternalServerError)
	}
	expiresAt := time.Now().UTC().Add(adminChallengeTTL)
	message := adminAuthMessage(wallet, challengeID, expiresAt)

	s.adminAuth.mu.Lock()
	s.pruneAdminAuthLocked(time.Now().UTC())
	s.adminAuth.challenges[challengeID] = adminChallenge{
		ID:        challengeID,
		Wallet:    wallet,
		Message:   message,
		ExpiresAt: expiresAt,
	}
	s.adminAuth.mu.Unlock()

	return c.Status(fiber.StatusCreated).JSON(adminAuthChallengeResponse{
		ChallengeID:    challengeID,
		Message:        message,
		ExpiresAt:      expiresAt,
		TreasuryWallet: treasuryWallet,
	})
}

func (s *Server) createAdminSession(c *fiber.Ctx) error {
	treasuryWallet := strings.TrimSpace(s.cfg.SolanaTreasuryWallet)
	if treasuryWallet == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "admin_wallet_not_configured"})
	}
	var request adminSessionRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	wallet := strings.TrimSpace(request.Wallet)
	if wallet == "" || strings.TrimSpace(request.ChallengeID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "admin_challenge_required"})
	}
	if wallet != treasuryWallet {
		return c.Status(fiber.StatusForbidden).JSON(map[string]string{"error": "admin_wallet_forbidden"})
	}
	publicKey, err := decodeSolanaPublicKey(wallet)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "invalid_wallet"})
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(request.Signature))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "invalid_signature"})
	}
	signedMessage, err := base64.StdEncoding.DecodeString(strings.TrimSpace(request.SignedMessage))
	if err != nil || len(signedMessage) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "invalid_signed_message"})
	}

	now := time.Now().UTC()
	challengeID := strings.TrimSpace(request.ChallengeID)
	s.adminAuth.mu.Lock()
	s.pruneAdminAuthLocked(now)
	challenge, ok := s.adminAuth.challenges[challengeID]
	if ok {
		delete(s.adminAuth.challenges, challengeID)
	}
	s.adminAuth.mu.Unlock()
	if !ok || challenge.Wallet != wallet || now.After(challenge.ExpiresAt) {
		return c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "admin_challenge_invalid"})
	}
	if string(signedMessage) != challenge.Message {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "admin_signed_message_mismatch"})
	}
	if !ed25519.Verify(publicKey, signedMessage, signature) {
		return c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "admin_signature_invalid"})
	}

	sessionToken, err := randomAdminToken()
	if err != nil {
		return c.SendStatus(fiber.StatusInternalServerError)
	}
	expiresAt := now.Add(adminSessionTTL)
	s.adminAuth.mu.Lock()
	s.pruneAdminAuthLocked(now)
	s.adminAuth.sessions[adminSessionKey(sessionToken)] = adminSession{
		Wallet:    wallet,
		ExpiresAt: expiresAt,
	}
	s.adminAuth.mu.Unlock()

	return c.JSON(adminSessionResponse{
		SessionToken: sessionToken,
		Wallet:       wallet,
		ExpiresAt:    expiresAt,
	})
}

func (s *Server) getAdminRuntimeStatus(c *fiber.Ctx) error {
	if _, denied := s.requireAdmin(c); denied != nil {
		return denied
	}
	if s.runtime == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	status, err := s.runtime.Status(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "runtime_status_failed"})
	}
	return c.JSON(adminRuntimeStatusView(status))
}

func (s *Server) updateAdminRuntimeStatus(c *fiber.Ctx) error {
	session, denied := s.requireAdmin(c)
	if denied != nil {
		return denied
	}
	if s.runtime == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	var request updateAdminRuntimeStatusRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	status, err := s.runtime.SetStatus(c.Context(), runtimecontrol.SetStatusRequest{
		Disabled: request.Disabled,
		Message:  request.Message,
		Actor:    "treasury_wallet:" + session.Wallet,
	})
	if err != nil {
		if errors.Is(err, runtimecontrol.ErrMessageTooLong) {
			return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "maintenance_message_too_long"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "runtime_status_failed"})
	}
	return c.JSON(adminRuntimeStatusView(status))
}

func adminRuntimeStatusView(status runtimecontrol.Status) adminRuntimeStatusResponse {
	return adminRuntimeStatusResponse{
		Disabled:         status.Disabled,
		Message:          status.Message,
		DefaultMessage:   runtimecontrol.DefaultMaintenanceMessage,
		EffectiveMessage: status.EffectiveMessage,
		UpdatedBy:        status.UpdatedBy,
		UpdatedAt:        status.UpdatedAt.UTC(),
	}
}

func (s *Server) listAdminCoupons(c *fiber.Ctx) error {
	session, denied := s.requireAdmin(c)
	if denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	coupons, err := s.billing.ListCoupons(c.Context(), billing.ListCouponsRequest{
		ActorUserID:  "treasury_wallet:" + session.Wallet,
		ActorIsOwner: true,
	})
	if err != nil {
		return writeAdminCouponError(c, err)
	}
	packLamports := s.cfg.SolanaPackLamports
	if len(packLamports) == 0 {
		packLamports = s.cfg.SolanaPlanLamports
	}
	return c.JSON(adminCouponListResponse{
		Coupons:      coupons,
		PackLamports: cloneLamports(packLamports),
	})
}

func (s *Server) createAdminCoupon(c *fiber.Ctx) error {
	session, denied := s.requireAdmin(c)
	if denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	var request createAdminCouponRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	expiresAt, err := parseAdminCouponExpiry(request.ExpiresAt)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "invalid_expiration"})
	}
	result, err := s.billing.CreateCoupon(c.Context(), billing.CreateCouponRequest{
		ActorUserID:      "treasury_wallet:" + session.Wallet,
		ActorIsOwner:     true,
		Pack:             request.Pack,
		Plan:             request.Plan,
		DiscountLamports: request.DiscountLamports,
		Code:             request.CouponCode,
		MaxRedemptions:   request.MaxRedemptions,
		ExpiresAt:        expiresAt,
		Note:             request.Note,
	})
	if err != nil {
		return writeAdminCouponError(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(adminCouponCreateResponse{
		Coupon: result.Coupon,
		Code:   result.Code,
	})
}

func (s *Server) revokeAdminCoupon(c *fiber.Ctx) error {
	session, denied := s.requireAdmin(c)
	if denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	identifier := strings.TrimSpace(c.Params("coupon"))
	request := billing.RevokeCouponRequest{
		ActorUserID:  "treasury_wallet:" + session.Wallet,
		ActorIsOwner: true,
	}
	if strings.HasPrefix(identifier, "cpn_") {
		request.CouponID = identifier
	} else {
		request.Prefix = identifier
	}
	coupon, err := s.billing.RevokeCoupon(c.Context(), request)
	if err != nil {
		return writeAdminCouponError(c, err)
	}
	return c.JSON(coupon)
}

type adminGuildLimitsView struct {
	Credits               int64 `json:"credits"`
	KnowledgeStorageBytes int64 `json:"knowledge_storage_bytes"`
	RetentionDays         int   `json:"retention_days"`
}

type adminGuildUsageView struct {
	AvailableCredits      int64 `json:"available_credits"`
	ReservedCredits       int64 `json:"reserved_credits"`
	Credits               int64 `json:"credits"`
	KnowledgeStorageBytes int64 `json:"knowledge_storage_bytes"`
}

type adminGuildBillingView struct {
	HasCreditAccount   bool                  `json:"has_credit_account"`
	Pack               string                `json:"pack"`
	PackDisplayName    string                `json:"pack_display_name"`
	Status             string                `json:"status"`
	StoredStatus       string                `json:"stored_status"`
	GraceState         string                `json:"grace_state"`
	PaymentProvider    string                `json:"payment_provider"`
	PeriodStart        *time.Time            `json:"period_start,omitempty"`
	PeriodEnd          *time.Time            `json:"period_end,omitempty"`
	TrialEndsAt        *time.Time            `json:"trial_ends_at,omitempty"`
	CancelAtPeriodEnd  bool                  `json:"cancel_at_period_end"`
	CanUsePaidFeatures bool                  `json:"can_use_paid_features"`
	ReadOnly           bool                  `json:"read_only"`
	BillingOwnerUserID string                `json:"billing_owner_user_id"`
	Email              string                `json:"email"`
	AvailableCredits   int64                 `json:"available_credits"`
	ReservedCredits    int64                 `json:"reserved_credits"`
	Credits            int64                 `json:"credits"`
	Limits             *adminGuildLimitsView `json:"limits,omitempty"`
	Usage              adminGuildUsageView   `json:"usage"`
}

type adminGuildView struct {
	GuildID           string                 `json:"guild_id"`
	Name              string                 `json:"name"`
	InstallStatus     string                 `json:"install_status"`
	OwnerUserID       string                 `json:"owner_user_id"`
	InstalledByUserID string                 `json:"installed_by_user_id"`
	Locale            string                 `json:"locale"`
	JoinedAt          time.Time              `json:"joined_at"`
	LeftAt            *time.Time             `json:"left_at,omitempty"`
	Billing           *adminGuildBillingView `json:"billing"`
}

type adminPackView struct {
	Pack        string `json:"pack"`
	DisplayName string `json:"display_name"`
	PriceCents  int    `json:"price_cents"`
	Credits     int64  `json:"credits"`
}

type adminGuildListResponse struct {
	Guilds      []adminGuildView `json:"guilds"`
	Total       int64            `json:"total"`
	Limit       int              `json:"limit"`
	Offset      int              `json:"offset"`
	PackCatalog []adminPackView  `json:"pack_catalog"`
	Statuses    []string         `json:"statuses"`
}

type updateAdminGuildCreditAccountRequest struct {
	Pack              string `json:"pack"`
	Plan              string `json:"plan"`
	Status            string `json:"status"`
	PeriodEnd         string `json:"period_end"`
	TrialEndsAt       string `json:"trial_ends_at"`
	ClearTrialEndsAt  bool   `json:"clear_trial_ends_at"`
	CancelAtPeriodEnd *bool  `json:"cancel_at_period_end"`
}

func adminPackCatalog() []adminPackView {
	catalog := billing.PackCatalog()
	views := make([]adminPackView, 0, len(catalog))
	for _, pack := range catalog {
		views = append(views, adminPackView{
			Pack:        pack.Pack,
			DisplayName: pack.DisplayName,
			PriceCents:  pack.PriceCents,
			Credits:     pack.Credits,
		})
	}
	return views
}

func adminGuildBillingViewFrom(overview billing.AdminGuildBilling) *adminGuildBillingView {
	view := &adminGuildBillingView{
		HasCreditAccount:   overview.HasCreditAccount,
		Pack:               overview.Pack,
		PackDisplayName:    overview.PackDisplayName,
		Status:             overview.Status,
		StoredStatus:       overview.StoredStatus,
		GraceState:         overview.GraceState,
		PaymentProvider:    overview.PaymentProvider,
		TrialEndsAt:        overview.TrialEndsAt,
		CancelAtPeriodEnd:  overview.CancelAtPeriodEnd,
		CanUsePaidFeatures: overview.CanUsePaidFeatures,
		ReadOnly:           overview.ReadOnly,
		BillingOwnerUserID: overview.BillingOwnerUserID,
		Email:              overview.Email,
		AvailableCredits:   overview.AvailableCredits,
		ReservedCredits:    overview.ReservedCredits,
		Credits:            overview.Credits,
		Usage: adminGuildUsageView{
			AvailableCredits: overview.AvailableCredits,
			ReservedCredits:  overview.ReservedCredits,
			Credits:          overview.Credits,
		},
	}
	if overview.HasCreditAccount {
		periodStart := overview.PeriodStart
		periodEnd := overview.PeriodEnd
		view.PeriodStart = &periodStart
		view.PeriodEnd = &periodEnd
		view.Limits = &adminGuildLimitsView{
			Credits:               overview.Limits.Credits,
			KnowledgeStorageBytes: overview.Limits.KnowledgeStorageBytes,
			RetentionDays:         overview.Limits.RetentionDays,
		}
	}
	return view
}

func (s *Server) adminGuildBillingView(ctx context.Context, guildID string, overview billing.AdminGuildBilling) (*adminGuildBillingView, error) {
	view := adminGuildBillingViewFrom(overview)
	if view == nil || !view.HasCreditAccount || s.knowledge == nil {
		return view, nil
	}
	storageBytes, err := s.knowledge.StorageBytes(ctx, guildID)
	if err != nil {
		return nil, err
	}
	view.Usage.KnowledgeStorageBytes = storageBytes
	return view, nil
}

func (s *Server) listAdminGuilds(c *fiber.Ctx) error {
	if _, denied := s.requireAdmin(c); denied != nil {
		return denied
	}
	if s.guilds == nil || s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}

	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	offset := 0
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}

	guilds, total, err := s.guilds.List(c.Context(), repository.GuildListFilter{
		Search: strings.TrimSpace(c.Query("q")),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "guild_list_failed"})
	}

	views := make([]adminGuildView, 0, len(guilds))
	for _, guild := range guilds {
		overview, err := s.billing.AdminOverview(c.Context(), guild.GuildID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "guild_billing_failed"})
		}
		billingView, err := s.adminGuildBillingView(c.Context(), guild.GuildID, overview)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "guild_storage_failed"})
		}
		views = append(views, adminGuildView{
			GuildID:           guild.GuildID,
			Name:              guild.Name,
			InstallStatus:     guild.InstallStatus,
			OwnerUserID:       guild.OwnerUserID,
			InstalledByUserID: guild.InstalledByUserID,
			Locale:            guild.Locale,
			JoinedAt:          guild.JoinedAt.UTC(),
			LeftAt:            guild.LeftAt,
			Billing:           billingView,
		})
	}

	return c.JSON(adminGuildListResponse{
		Guilds:      views,
		Total:       total,
		Limit:       limit,
		Offset:      offset,
		PackCatalog: adminPackCatalog(),
		Statuses:    billing.AdminStatuses(),
	})
}

func (s *Server) updateAdminGuildCreditAccount(c *fiber.Ctx) error {
	session, denied := s.requireAdmin(c)
	if denied != nil {
		return denied
	}
	if s.guilds == nil || s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	guildID := strings.TrimSpace(c.Params("guild_id"))
	if guildID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "guild_id_required"})
	}
	guild, ok, err := s.guilds.Get(c.Context(), guildID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "guild_lookup_failed"})
	}
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "guild_not_found"})
	}

	var request updateAdminGuildCreditAccountRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	periodEnd, err := parseAdminCouponExpiry(request.PeriodEnd)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "invalid_period_end"})
	}
	trialEndsAt, err := parseAdminCouponExpiry(request.TrialEndsAt)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "invalid_trial_ends_at"})
	}

	pack := strings.TrimSpace(request.Pack)
	if pack == "" {
		pack = strings.TrimSpace(request.Plan)
	}
	overview, err := s.billing.AdminSetCreditAccount(c.Context(), billing.AdminSetCreditAccountRequest{
		GuildID:           guildID,
		ActorUserID:       "treasury_wallet:" + session.Wallet,
		Pack:              pack,
		Status:            request.Status,
		PeriodEnd:         periodEnd,
		TrialEndsAt:       trialEndsAt,
		ClearTrialEndsAt:  request.ClearTrialEndsAt,
		CancelAtPeriodEnd: request.CancelAtPeriodEnd,
	})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "credit_account_update_failed"})
	}
	billingView, err := s.adminGuildBillingView(c.Context(), guild.GuildID, overview)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "guild_storage_failed"})
	}

	return c.JSON(adminGuildView{
		GuildID:           guild.GuildID,
		Name:              guild.Name,
		InstallStatus:     guild.InstallStatus,
		OwnerUserID:       guild.OwnerUserID,
		InstalledByUserID: guild.InstalledByUserID,
		Locale:            guild.Locale,
		JoinedAt:          guild.JoinedAt.UTC(),
		LeftAt:            guild.LeftAt,
		Billing:           billingView,
	})
}

func (s *Server) requireAdmin(c *fiber.Ctx) (adminSession, error) {
	token := bearerToken(c)
	if token == "" {
		return adminSession{}, c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "admin_unauthorized"})
	}
	now := time.Now().UTC()
	key := adminSessionKey(token)
	s.adminAuth.mu.Lock()
	s.pruneAdminAuthLocked(now)
	session, ok := s.adminAuth.sessions[key]
	s.adminAuth.mu.Unlock()
	if !ok || now.After(session.ExpiresAt) {
		return adminSession{}, c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "admin_unauthorized"})
	}
	return session, nil
}

func bearerToken(c *fiber.Ctx) string {
	auth := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
	if len(auth) < 7 || !strings.EqualFold(auth[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(auth[7:])
}

func adminAuthMessage(wallet, challengeID string, expiresAt time.Time) string {
	return strings.Join([]string{
		"Panda admin login",
		"",
		"Sign this message to manage Panda guilds and billing.",
		"Wallet: " + wallet,
		"Challenge: " + challengeID,
		"Expires: " + expiresAt.UTC().Format(time.RFC3339),
	}, "\n")
}

func randomAdminToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func adminSessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Server) pruneAdminAuthLocked(now time.Time) {
	for id, challenge := range s.adminAuth.challenges {
		if now.After(challenge.ExpiresAt) {
			delete(s.adminAuth.challenges, id)
		}
	}
	for key, session := range s.adminAuth.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.adminAuth.sessions, key)
		}
	}
}

func decodeSolanaPublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := decodeBase58Fixed(strings.TrimSpace(value), ed25519.PublicKeySize)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(decoded), nil
}

func decodeBase58Fixed(value string, size int) ([]byte, error) {
	decoded, err := decodeBase58(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) != size {
		return nil, fmt.Errorf("expected %d decoded bytes, got %d", size, len(decoded))
	}
	return decoded, nil
}

func decodeBase58(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("empty base58 value")
	}
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	base := big.NewInt(58)
	decoded := big.NewInt(0)
	for _, char := range value {
		index := strings.IndexRune(alphabet, char)
		if index < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", char)
		}
		decoded.Mul(decoded, base)
		decoded.Add(decoded, big.NewInt(int64(index)))
	}
	leadingZeroes := 0
	for leadingZeroes < len(value) && value[leadingZeroes] == '1' {
		leadingZeroes++
	}
	result := append(make([]byte, leadingZeroes), decoded.Bytes()...)
	if len(result) == 0 {
		return []byte{0}, nil
	}
	return result, nil
}

func parseAdminCouponExpiry(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		parsed = parsed.UTC()
		return &parsed, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func cloneLamports(values map[string]int64) map[string]int64 {
	clone := make(map[string]int64, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

type billingEntitlementResponse struct {
	GuildID            string                                   `json:"guild_id"`
	Pack               string                                   `json:"pack"`
	DisplayName        string                                   `json:"display_name"`
	PackDisplayName    string                                   `json:"pack_display_name"`
	Status             string                                   `json:"status"`
	GraceState         string                                   `json:"grace_state"`
	PaymentProvider    string                                   `json:"payment_provider"`
	PeriodStart        time.Time                                `json:"period_start"`
	PeriodEnd          time.Time                                `json:"period_end"`
	TrialEndsAt        *time.Time                               `json:"trial_ends_at,omitempty"`
	CanUsePaidFeatures bool                                     `json:"can_use_paid_features"`
	ReadOnly           bool                                     `json:"read_only"`
	AvailableCredits   int64                                    `json:"available_credits"`
	ReservedCredits    int64                                    `json:"reserved_credits"`
	Credits            int64                                    `json:"credits"`
	RetentionDays      int                                      `json:"retention_days"`
	KnowledgeLimit     int64                                    `json:"knowledge_storage_bytes_limit"`
	Usage              map[string]billingEntitlementUsageMetric `json:"usage"`
}

type billingEntitlementUsageMetric struct {
	Metric    string `json:"metric"`
	Label     string `json:"label"`
	Used      int64  `json:"used"`
	Reserved  int64  `json:"reserved"`
	Limit     int64  `json:"limit"`
	Remaining int64  `json:"remaining"`
	Formatted string `json:"formatted"`
}

func (s *Server) getBillingEntitlement(c *fiber.Ctx) error {
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	guildID := strings.TrimSpace(c.Params("guild_id"))
	if guildID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "guild_id_required"})
	}
	entitlement, err := s.billing.Resolve(c.Context(), guildID)
	if err != nil {
		return writeBillingEntitlementError(c, err)
	}
	return c.JSON(entitlementResponse(entitlement))
}

func entitlementResponse(entitlement billing.Entitlement) billingEntitlementResponse {
	creditsUsed := entitlement.Pack.Credits - entitlement.AvailableCredits
	if creditsUsed < 0 {
		creditsUsed = 0
	}
	return billingEntitlementResponse{
		GuildID:            entitlement.GuildID,
		Pack:               entitlement.Pack.Pack,
		DisplayName:        entitlement.Pack.DisplayName,
		PackDisplayName:    entitlement.Pack.DisplayName,
		Status:             entitlement.Status,
		GraceState:         entitlement.GraceState,
		PaymentProvider:    entitlement.PaymentProvider,
		PeriodStart:        entitlement.PeriodStart,
		PeriodEnd:          entitlement.PeriodEnd,
		TrialEndsAt:        entitlement.TrialEndsAt,
		CanUsePaidFeatures: entitlement.CanUsePaidFeatures,
		ReadOnly:           entitlement.ReadOnly,
		AvailableCredits:   entitlement.AvailableCredits,
		ReservedCredits:    entitlement.ReservedCredits,
		Credits:            entitlement.Pack.Credits,
		RetentionDays:      entitlement.RetentionDays,
		KnowledgeLimit:     entitlement.KnowledgeStorageBytesLimit,
		Usage: map[string]billingEntitlementUsageMetric{
			"credits": billingUsageMetric(
				"credits",
				creditsUsed,
				entitlement.ReservedCredits,
				entitlement.Pack.Credits,
			),
		},
	}
}

func billingUsageMetric(metric string, used, reserved, limit int64) billingEntitlementUsageMetric {
	remaining := limit - used - reserved
	if remaining < 0 {
		remaining = 0
	}
	return billingEntitlementUsageMetric{
		Metric:    metric,
		Label:     billing.MetricLabel(metric),
		Used:      used,
		Reserved:  reserved,
		Limit:     limit,
		Remaining: remaining,
		Formatted: billing.FormatUsage(used+reserved, limit, metric),
	}
}

func (s *Server) createSolPaymentOrder(c *fiber.Ctx) error {
	if denied := s.allowPaymentWrite(c); denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	var request createSolPaymentOrderRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	order, err := s.billing.CreateSolPaymentOrder(c.Context(), billing.CreateSolPaymentOrderRequest{
		GuildID:            request.GuildID,
		BillingOwnerUserID: request.BillingOwnerUserID,
		Pack:               request.Pack,
		Plan:               request.Plan,
		SupportEmail:       request.SupportEmail,
		CouponCode:         request.CouponCode,
	})
	if err != nil {
		return writeSolBillingError(c, err)
	}
	return c.Status(fiber.StatusCreated).JSON(order)
}

func (s *Server) getSolPaymentOrder(c *fiber.Ctx) error {
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	order, err := s.billing.GetSolPaymentOrder(c.Context(), c.Params("order_id"))
	if err != nil {
		return writeSolBillingError(c, err)
	}
	return c.JSON(order)
}

func (s *Server) prepareSolPaymentTransaction(c *fiber.Ctx) error {
	if denied := s.allowPaymentWrite(c); denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	var request prepareSolPaymentTransactionRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	prepared, err := s.billing.PrepareSolPaymentTransaction(c.Context(), billing.PrepareSolPaymentTransactionRequest{
		OrderID:     c.Params("order_id"),
		PayerWallet: request.PayerWallet,
	})
	if err != nil {
		return writeSolBillingError(c, err)
	}
	return c.JSON(prepared)
}

func (s *Server) submitSolPaymentTransaction(c *fiber.Ctx) error {
	if denied := s.allowPaymentWrite(c); denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	var request submitSolPaymentTransactionRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	result, err := s.billing.SubmitSolPaymentTransaction(c.Context(), billing.SubmitSolPaymentTransactionRequest{
		OrderID:           c.Params("order_id"),
		SignedTransaction: request.SignedTransaction,
	})
	if err != nil {
		switch result.FailureCode {
		case "pending_confirmation":
			return c.Status(fiber.StatusAccepted).JSON(result)
		case "rpc_unavailable":
			return c.Status(fiber.StatusServiceUnavailable).JSON(result)
		case "verification_failed", "duplicate_or_stale":
			return c.Status(fiber.StatusUnprocessableEntity).JSON(result)
		default:
			return writeSolBillingError(c, err)
		}
	}
	return c.JSON(result)
}

func (s *Server) verifySolPaymentOrder(c *fiber.Ctx) error {
	if denied := s.allowPaymentWrite(c); denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	var request verifySolPaymentRequest
	if err := c.BodyParser(&request); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	result, err := s.billing.VerifySolPayment(c.Context(), billing.VerifySolPaymentRequest{
		OrderID:   c.Params("order_id"),
		Signature: request.Signature,
	})
	if err != nil {
		switch result.FailureCode {
		case "pending_confirmation":
			return c.Status(fiber.StatusAccepted).JSON(result)
		case "rpc_unavailable":
			return c.Status(fiber.StatusServiceUnavailable).JSON(result)
		case "verification_failed", "duplicate_or_stale":
			return c.Status(fiber.StatusUnprocessableEntity).JSON(result)
		default:
			return writeSolBillingError(c, err)
		}
	}
	return c.JSON(result)
}

func (s *Server) revealSolActivationKey(c *fiber.Ctx) error {
	if denied := s.allowPaymentWrite(c); denied != nil {
		return denied
	}
	if s.billing == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	key, err := s.billing.RevealActivationKey(c.Context(), c.Params("order_id"))
	if err != nil {
		return writeSolBillingError(c, err)
	}
	return c.JSON(key)
}

func (s *Server) allowPaymentWrite(c *fiber.Ctx) error {
	if s.paymentLimiter == nil {
		return nil
	}
	key := c.IP() + ":" + c.Method() + ":" + c.Route().Path
	ok, retryAfter := s.paymentLimiter.Allow(key)
	if ok {
		return nil
	}
	c.Set(fiber.HeaderRetryAfter, strconv.Itoa(int(retryAfter.Round(time.Second).Seconds())))
	return c.Status(fiber.StatusTooManyRequests).JSON(map[string]string{
		"error":       "rate_limited",
		"retry_after": retryAfter.Round(time.Second).String(),
	})
}

func writeSolBillingError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, billing.ErrSolPaymentsNotConfigured):
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "sol_payments_not_configured"})
	case errors.Is(err, billing.ErrUnknownPack):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "unknown_pack"})
	case errors.Is(err, billing.ErrCouponInvalid):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "coupon_invalid"})
	case errors.Is(err, billing.ErrCouponPackMismatch):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "coupon_wrong_pack"})
	case errors.Is(err, billing.ErrCouponRevoked):
		return c.Status(fiber.StatusGone).JSON(map[string]string{"error": "coupon_revoked"})
	case errors.Is(err, billing.ErrCouponExpired):
		return c.Status(fiber.StatusGone).JSON(map[string]string{"error": "coupon_expired"})
	case errors.Is(err, billing.ErrCouponExhausted):
		return c.Status(fiber.StatusConflict).JSON(map[string]string{"error": "coupon_exhausted"})
	case errors.Is(err, billing.ErrSolPaymentOrderNotFound):
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "order_not_found"})
	case errors.Is(err, billing.ErrSolPaymentOrderExpired), errors.Is(err, billing.ErrActivationKeyExpired):
		return c.Status(fiber.StatusGone).JSON(map[string]string{"error": "expired"})
	case errors.Is(err, billing.ErrSolPaymentOrderNotVerified):
		return c.Status(fiber.StatusConflict).JSON(map[string]string{"error": "order_not_verified"})
	case errors.Is(err, billing.ErrSolPaymentNotRequired):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "sol_payment_not_required"})
	case errors.Is(err, billing.ErrSolPaymentOrderAlreadyActive):
		return c.Status(fiber.StatusConflict).JSON(map[string]string{"error": "order_already_ready"})
	case errors.Is(err, billing.ErrActivationKeyAlreadyRevealed):
		return c.Status(fiber.StatusConflict).JSON(map[string]string{"error": "activation_key_already_revealed"})
	default:
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "bad_request"})
	}
}

func writeBillingEntitlementError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, billing.ErrNoCreditAccount):
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "credit_account_not_found"})
	case errors.Is(err, billing.ErrUnknownPack):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "unknown_pack"})
	default:
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "bad_request"})
	}
}

func writeAdminCouponError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, billing.ErrBillingAccess):
		return c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "admin_unauthorized"})
	case errors.Is(err, billing.ErrUnknownPack):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "unknown_pack"})
	case errors.Is(err, billing.ErrCouponDuplicate):
		return c.Status(fiber.StatusConflict).JSON(map[string]string{"error": "coupon_duplicate"})
	case errors.Is(err, billing.ErrCouponExpired):
		return c.Status(fiber.StatusGone).JSON(map[string]string{"error": "coupon_expired"})
	case errors.Is(err, billing.ErrCouponRevoked):
		return c.Status(fiber.StatusGone).JSON(map[string]string{"error": "coupon_revoked"})
	case errors.Is(err, billing.ErrCouponNotFound):
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "coupon_not_found"})
	case errors.Is(err, billing.ErrCouponAmbiguous):
		return c.Status(fiber.StatusConflict).JSON(map[string]string{"error": "coupon_ambiguous"})
	default:
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "bad_request"})
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
	writeGaugeHeader(builder, name, help)
	fmt.Fprintf(builder, "%s %v\n", name, value)
}

func writeGaugeHeader(builder *strings.Builder, name, help string) {
	fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
	fmt.Fprintf(builder, "# TYPE %s gauge\n", name)
}

func writeGaugeSampleWithLabels(builder *strings.Builder, name string, labels map[string]string, value any) {
	fmt.Fprintf(builder, "%s{%s} %v\n", name, formatMetricLabels(labels), value)
}

func formatMetricLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, key, strings.ReplaceAll(labels[key], `"`, `\"`)))
	}
	return strings.Join(parts, ",")
}

func sanitizeMetricLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
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
