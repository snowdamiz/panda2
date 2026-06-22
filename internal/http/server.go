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
	"html"
	"math/big"
	stdhttp "net/http"
	stdurl "net/url"
	"os"
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
	"github.com/sn0w/panda2/internal/store"
)

type Server struct {
	app            *fiber.App
	cfg            config.Config
	store          *store.Store
	discordWebhook DiscordWebhookHandler
	install        InstallHandler
	billing        *billing.Service
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
	s.app.Get("/install/success", s.installSuccessResult)
	s.app.Get("/install/failed", s.installFailedResult)
	s.app.Get("/install/features", s.installFeatures)
	s.app.Post("/install/intents", s.createInstallIntent)
	s.app.Get("/discord/install/callback", s.discordInstallCallback)
	s.app.Post("/admin/auth/challenge", s.createAdminAuthChallenge)
	s.app.Post("/admin/auth/sessions", s.createAdminSession)
	s.app.Get("/admin/coupons", s.listAdminCoupons)
	s.app.Post("/admin/coupons", s.createAdminCoupon)
	s.app.Post("/admin/coupons/:coupon/revoke", s.revokeAdminCoupon)
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
			return c.Redirect(redirectURL, fiber.StatusFound)
		}
		return c.Redirect(installFailureResultPath(err), fiber.StatusFound)
	}
	if result.RedirectURL != "" {
		return c.Redirect(result.RedirectURL, fiber.StatusFound)
	}
	return c.JSON(map[string]any{
		"status":            "success",
		"guild_id":          result.GuildID,
		"installer_user_id": result.InstallerUserID,
		"intent_id":         result.IntentID,
		"feature_ids":       result.FeatureIDs,
	})
}

func (s *Server) installSuccessResult(c *fiber.Ctx) error {
	guildID := strings.TrimSpace(c.Query("guild_id"))
	discordHref := "https://discord.com/channels/@me"
	if guildID != "" {
		discordHref = "https://discord.com/channels/" + stdurl.PathEscape(guildID)
	}
	return c.Type(fiber.MIMETextHTMLCharsetUTF8).SendString(installResultHTML(
		"Panda is installed",
		"Install complete",
		"Panda is installed.",
		"Open Discord and configure Panda from your server.",
		discordHref,
		"Open Discord",
	))
}

func (s *Server) installFailedResult(c *fiber.Ctx) error {
	errorCode := strings.TrimSpace(c.Query("error"))
	if errorCode == "" {
		errorCode = "install_failed"
	}
	installHref := strings.TrimRight(strings.TrimSpace(s.cfg.PublicAppURL), "/")
	if installHref == "" {
		installHref = "https://pandaclanker.xyz"
	}
	return c.Type(fiber.MIMETextHTMLCharsetUTF8).SendString(installResultHTML(
		"Panda install needs attention",
		"Install failed",
		"Panda could not finish the install.",
		"Return to Panda and start a fresh Discord install link.",
		installHref+"/#install",
		"Try again",
		"Error: "+errorCode,
	))
}

func installResultHTML(title, eyebrow, heading, body, actionHref, actionLabel string, details ...string) string {
	var detailHTML string
	if len(details) > 0 && strings.TrimSpace(details[0]) != "" {
		detailHTML = `<p class="details">` + html.EscapeString(details[0]) + `</p>`
	}
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + `</title>
<style>
:root{color-scheme:dark;--bg:#09090b;--fg:#f7f7f8;--muted:#a1a1aa;--line:#27272a;--accent:#f4c95d}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(circle at 20% 20%,#1f2937,transparent 32rem),var(--bg);color:var(--fg);font:16px/1.5 Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
main{width:min(92vw,42rem);padding:3rem;border:1px solid var(--line);background:rgba(24,24,27,.88)}
span{display:block;margin-bottom:1rem;color:var(--accent);font-size:.78rem;font-weight:800;letter-spacing:.12em;text-transform:uppercase}
h1{margin:0 0 1rem;font-size:clamp(2rem,7vw,4.5rem);line-height:.95;letter-spacing:0}
p{margin:0 0 1.5rem;color:var(--muted);font-size:1.05rem}.details{font-size:.9rem}
a{display:inline-flex;align-items:center;min-height:2.75rem;padding:0 1.1rem;background:var(--fg);color:#09090b;text-decoration:none;font-weight:800}
</style>
</head>
<body>
<main>
<span>` + html.EscapeString(eyebrow) + `</span>
<h1>` + html.EscapeString(heading) + `</h1>
<p>` + html.EscapeString(body) + `</p>
` + detailHTML + `
<a href="` + html.EscapeString(actionHref) + `">` + html.EscapeString(actionLabel) + `</a>
</main>
</body>
</html>`
}

func installLocalDevelopmentSuccessRedirect(publicURL, environment, guildID string) string {
	if strings.EqualFold(strings.TrimSpace(environment), "production") || !isLocalAppURL(publicURL) {
		return ""
	}
	return installSuccessRedirect(publicURL, guildID)
}

func isLocalAppURL(value string) bool {
	u, err := stdhttp.NewRequest(stdhttp.MethodGet, strings.TrimSpace(value), nil)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.URL.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func installSuccessRedirect(publicURL, guildID string) string {
	publicURL = strings.TrimSpace(publicURL)
	if publicURL == "" {
		return ""
	}
	u, parseErr := stdhttp.NewRequest(stdhttp.MethodGet, strings.TrimRight(publicURL, "/")+"/install/success", nil)
	if parseErr != nil {
		return ""
	}
	q := u.URL.Query()
	q.Set("status", "success")
	if guildID = strings.TrimSpace(guildID); guildID != "" {
		q.Set("guild_id", guildID)
	}
	u.URL.RawQuery = q.Encode()
	return u.URL.String()
}

func installFailureResultPath(err error) string {
	u := stdurl.URL{Path: "/install/failed"}
	q := u.Query()
	q.Set("status", "failed")
	q.Set("error", installErrorCode(err))
	u.RawQuery = q.Encode()
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
		"ai_service":    configuredStatus(s.cfg.OpenRouterConfigured(), "AI service key missing; natural-language assistant disabled"),
		"brave_search":  configuredStatus(s.cfg.BraveSearchConfigured(), "api key missing; web search disabled"),
		"sol_payments":  configuredStatus(s.cfg.SolanaPaymentsConfigured(), "SOL payment settings incomplete; paid purchases disabled"),
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

type createAdminCouponRequest struct {
	Plan             string `json:"plan"`
	DiscountLamports int64  `json:"discount_lamports"`
	CouponCode       string `json:"coupon_code"`
	MaxRedemptions   int    `json:"max_redemptions"`
	ExpiresAt        string `json:"expires_at"`
	Note             string `json:"note"`
}

type adminCouponListResponse struct {
	Coupons      []billing.CouponView `json:"coupons"`
	PlanLamports map[string]int64     `json:"plan_lamports"`
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
	return c.JSON(adminCouponListResponse{
		Coupons:      coupons,
		PlanLamports: cloneLamports(s.cfg.SolanaPlanLamports),
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
		"Sign this message to manage Panda billing coupons.",
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
	Plan               string                                   `json:"plan"`
	DisplayName        string                                   `json:"display_name"`
	Status             string                                   `json:"status"`
	GraceState         string                                   `json:"grace_state"`
	PaymentProvider    string                                   `json:"payment_provider"`
	PeriodStart        time.Time                                `json:"period_start"`
	PeriodEnd          time.Time                                `json:"period_end"`
	TrialEndsAt        *time.Time                               `json:"trial_ends_at,omitempty"`
	CanUsePaidFeatures bool                                     `json:"can_use_paid_features"`
	ReadOnly           bool                                     `json:"read_only"`
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
	return billingEntitlementResponse{
		GuildID:            entitlement.GuildID,
		Plan:               entitlement.Plan.Plan,
		DisplayName:        entitlement.Plan.DisplayName,
		Status:             entitlement.Status,
		GraceState:         entitlement.GraceState,
		PaymentProvider:    entitlement.PaymentProvider,
		PeriodStart:        entitlement.PeriodStart,
		PeriodEnd:          entitlement.PeriodEnd,
		TrialEndsAt:        entitlement.TrialEndsAt,
		CanUsePaidFeatures: entitlement.CanUsePaidFeatures,
		ReadOnly:           entitlement.ReadOnly,
		Usage: map[string]billingEntitlementUsageMetric{
			"ai_responses": billingUsageMetric(
				billing.MetricAIResponse,
				entitlement.Usage.AIResponsesConsumed,
				entitlement.Usage.AIResponsesReserved,
				billing.IncludedLimit(entitlement.Plan, billing.MetricAIResponse),
			),
			"web_searches": billingUsageMetric(
				billing.MetricWebSearch,
				entitlement.Usage.WebSearchesConsumed,
				entitlement.Usage.WebSearchesReserved,
				billing.IncludedLimit(entitlement.Plan, billing.MetricWebSearch),
			),
			"knowledge_storage": billingUsageMetric(
				billing.MetricKnowledgeStorageByte,
				entitlement.Usage.KnowledgeStorageBytesConsumed,
				entitlement.Usage.KnowledgeStorageBytesReserved,
				billing.IncludedLimit(entitlement.Plan, billing.MetricKnowledgeStorageByte),
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
	case errors.Is(err, billing.ErrUnknownPlan):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "unknown_plan"})
	case errors.Is(err, billing.ErrCouponInvalid):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "coupon_invalid"})
	case errors.Is(err, billing.ErrCouponPlanMismatch):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "coupon_wrong_plan"})
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
	case errors.Is(err, billing.ErrNoSubscription):
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "subscription_not_found"})
	case errors.Is(err, billing.ErrUnknownPlan):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "unknown_plan"})
	default:
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "bad_request"})
	}
}

func writeAdminCouponError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, billing.ErrBillingAccess):
		return c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "admin_unauthorized"})
	case errors.Is(err, billing.ErrUnknownPlan):
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "unknown_plan"})
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
