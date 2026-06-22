package http

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
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
	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
	"github.com/sn0w/panda2/internal/store"
)

type Server struct {
	app            *fiber.App
	cfg            config.Config
	store          *store.Store
	discordWebhook DiscordWebhookHandler
	billingWebhook BillingWebhookHandler
}

type DiscordWebhookHandler interface {
	HandleWebhookEvent(ctx context.Context, event discordbot.WebhookEvent) error
}

type BillingWebhookHandler interface {
	HandleStripeEvent(ctx context.Context, event billing.StripeEvent) error
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

func (s *Server) WithBillingWebhookHandler(handler BillingWebhookHandler) *Server {
	s.billingWebhook = handler
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
	s.app.Post("/billing/stripe/webhook", s.stripeWebhook)
	s.app.Get("/billing/success", s.billingSuccess)
	s.app.Get("/billing/cancel", s.billingCancel)
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

func (s *Server) billingSuccess(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return c.SendString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Panda billing updated</title></head><body><main style="font-family:system-ui,sans-serif;max-width:42rem;margin:12vh auto;padding:0 1rem;line-height:1.5"><h1>Panda billing is processing</h1><p>Stripe confirmed the checkout. Panda grants plans from the verified webhook, which usually lands within a moment.</p><p>Return to Discord and run <strong>/billing</strong> to see the updated plan.</p></main></body></html>`)
}

func (s *Server) billingCancel(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return c.SendString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Panda checkout canceled</title></head><body><main style="font-family:system-ui,sans-serif;max-width:42rem;margin:12vh auto;padding:0 1rem;line-height:1.5"><h1>Checkout canceled</h1><p>No Panda plan changes were made. Return to Discord and run <strong>/billing</strong> when you are ready to choose a plan.</p></main></body></html>`)
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

type stripeWebhookPayload struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type stripeSubscriptionObject struct {
	ID                 string            `json:"id"`
	Customer           string            `json:"customer"`
	Status             string            `json:"status"`
	Metadata           map[string]string `json:"metadata"`
	CustomerEmail      string            `json:"customer_email"`
	CurrentPeriodStart int64             `json:"current_period_start"`
	CurrentPeriodEnd   int64             `json:"current_period_end"`
	CancelAtPeriodEnd  bool              `json:"cancel_at_period_end"`
	Items              struct {
		Data []struct {
			Price struct {
				ID string `json:"id"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

type stripeCheckoutSessionObject struct {
	ID              string            `json:"id"`
	Customer        string            `json:"customer"`
	Subscription    string            `json:"subscription"`
	Mode            string            `json:"mode"`
	Metadata        map[string]string `json:"metadata"`
	AmountTotal     int64             `json:"amount_total"`
	Currency        string            `json:"currency"`
	CustomerDetails struct {
		Email string `json:"email"`
	} `json:"customer_details"`
}

type stripeInvoiceObject struct {
	ID            string            `json:"id"`
	Customer      string            `json:"customer"`
	Subscription  string            `json:"subscription"`
	Status        string            `json:"status"`
	AmountPaid    int64             `json:"amount_paid"`
	AmountDue     int64             `json:"amount_due"`
	Currency      string            `json:"currency"`
	CustomerEmail string            `json:"customer_email"`
	Metadata      map[string]string `json:"metadata"`
	Lines         struct {
		Data []struct {
			Price struct {
				ID string `json:"id"`
			} `json:"price"`
			Period struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"period"`
		} `json:"data"`
	} `json:"lines"`
	SubscriptionDetails struct {
		Metadata map[string]string `json:"metadata"`
	} `json:"subscription_details"`
	Parent struct {
		SubscriptionDetails struct {
			Metadata map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
}

func (s *Server) stripeWebhook(c *fiber.Ctx) error {
	if s.billingWebhook == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	body := c.BodyRaw()
	if err := verifyStripeSignature(s.cfg.StripeWebhookSecret, c.Get("Stripe-Signature"), body, time.Now); err != nil {
		if errors.Is(err, errStripeWebhookNotConfigured) {
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		return c.SendStatus(fiber.StatusUnauthorized)
	}
	var payload stripeWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	event, ok, err := stripeEventFromPayload(payload, body)
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if !ok {
		return c.SendStatus(fiber.StatusNoContent)
	}
	if err := s.billingWebhook.HandleStripeEvent(c.Context(), event); err != nil {
		return c.SendStatus(fiber.StatusInternalServerError)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func stripeEventFromPayload(payload stripeWebhookPayload, raw []byte) (billing.StripeEvent, bool, error) {
	switch payload.Type {
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		var object stripeSubscriptionObject
		if err := json.Unmarshal(payload.Data.Object, &object); err != nil {
			return billing.StripeEvent{}, false, err
		}
		priceID := ""
		if len(object.Items.Data) > 0 {
			priceID = object.Items.Data[0].Price.ID
		}
		return billing.StripeEvent{
			EventID:            payload.ID,
			EventType:          payload.Type,
			GuildID:            firstMetadata(object.Metadata, "guild_id"),
			Plan:               firstMetadata(object.Metadata, "plan"),
			CustomerEmail:      object.CustomerEmail,
			BillingOwnerUserID: firstMetadata(object.Metadata, "billing_owner_user_id"),
			CustomerID:         object.Customer,
			SubscriptionID:     object.ID,
			PriceID:            priceID,
			Status:             object.Status,
			CurrentPeriodStart: unixTime(object.CurrentPeriodStart),
			CurrentPeriodEnd:   unixTime(object.CurrentPeriodEnd),
			CancelAtPeriodEnd:  object.CancelAtPeriodEnd,
			RawPayload:         string(raw),
		}, true, nil
	case "checkout.session.completed":
		var object stripeCheckoutSessionObject
		if err := json.Unmarshal(payload.Data.Object, &object); err != nil {
			return billing.StripeEvent{}, false, err
		}
		return billing.StripeEvent{
			EventID:            payload.ID,
			EventType:          payload.Type,
			GuildID:            firstMetadata(object.Metadata, "guild_id"),
			Plan:               firstMetadata(object.Metadata, "plan"),
			CustomerEmail:      object.CustomerDetails.Email,
			BillingOwnerUserID: firstMetadata(object.Metadata, "billing_owner_user_id"),
			CustomerID:         object.Customer,
			CheckoutSessionID:  object.ID,
			SubscriptionID:     object.Subscription,
			Status:             "active",
			AmountCents:        object.AmountTotal,
			Currency:           object.Currency,
			RawPayload:         string(raw),
		}, true, nil
	case "invoice.payment_succeeded", "invoice.payment_failed":
		var object stripeInvoiceObject
		if err := json.Unmarshal(payload.Data.Object, &object); err != nil {
			return billing.StripeEvent{}, false, err
		}
		priceID := ""
		var periodStart, periodEnd time.Time
		if len(object.Lines.Data) > 0 {
			priceID = object.Lines.Data[0].Price.ID
			periodStart = unixTime(object.Lines.Data[0].Period.Start)
			periodEnd = unixTime(object.Lines.Data[0].Period.End)
		}
		metadata := mergedStripeMetadata(object.Metadata, object.SubscriptionDetails.Metadata, object.Parent.SubscriptionDetails.Metadata)
		status := object.Status
		if payload.Type == "invoice.payment_failed" {
			status = "past_due"
		}
		amount := object.AmountPaid
		if amount == 0 {
			amount = object.AmountDue
		}
		return billing.StripeEvent{
			EventID:            payload.ID,
			EventType:          payload.Type,
			GuildID:            firstMetadata(metadata, "guild_id"),
			Plan:               firstMetadata(metadata, "plan"),
			CustomerEmail:      object.CustomerEmail,
			BillingOwnerUserID: firstMetadata(metadata, "billing_owner_user_id"),
			CustomerID:         object.Customer,
			SubscriptionID:     object.Subscription,
			PriceID:            priceID,
			Status:             status,
			CurrentPeriodStart: periodStart,
			CurrentPeriodEnd:   periodEnd,
			AmountCents:        amount,
			Currency:           object.Currency,
			RawPayload:         string(raw),
		}, true, nil
	default:
		return billing.StripeEvent{}, false, nil
	}
}

var errStripeWebhookNotConfigured = errors.New("stripe webhook secret is not configured")

func verifyStripeSignature(secret, header string, body []byte, now func() time.Time) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errStripeWebhookNotConfigured
	}
	timestamp, signatures, err := stripeSignatureParts(header)
	if err != nil {
		return err
	}
	signedAt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("invalid stripe signature timestamp")
	}
	eventTime := time.Unix(signedAt, 0)
	if diff := now().Sub(eventTime); diff > 5*time.Minute || diff < -5*time.Minute {
		return errors.New("stale stripe signature timestamp")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, signature := range signatures {
		if subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1 {
			return nil
		}
	}
	return errors.New("invalid stripe signature")
}

func stripeSignatureParts(header string) (string, []string, error) {
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "t":
			timestamp = strings.TrimSpace(value)
		case "v1":
			if value = strings.TrimSpace(value); value != "" {
				signatures = append(signatures, value)
			}
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return "", nil, errors.New("missing stripe signature")
	}
	return timestamp, signatures, nil
}

func firstMetadata(metadata map[string]string, key string) string {
	if metadata == nil {
		return ""
	}
	return strings.TrimSpace(metadata[key])
}

func mergedStripeMetadata(values ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, metadata := range values {
		for key, value := range metadata {
			if strings.TrimSpace(value) != "" {
				merged[key] = value
			}
		}
	}
	return merged
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
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
