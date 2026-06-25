package http

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	stdhttp "net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/runtimecontrol"
	"github.com/sn0w/panda2/internal/store"
)

type fakeDiscordWebhookHandler struct {
	events []discordbot.WebhookEvent
}

func (f *fakeDiscordWebhookHandler) HandleWebhookEvent(_ context.Context, event discordbot.WebhookEvent) error {
	f.events = append(f.events, event)
	return nil
}

type fakeInstallHandler struct {
	callbackRequest discordbot.InstallCallbackRequest
	callbackResult  discordbot.InstallCallbackResult
	callbackErr     error
}

func (f *fakeInstallHandler) CreateInstallIntent(context.Context, discordbot.CreateInstallIntentRequest) (discordbot.CreateInstallIntentResult, error) {
	return discordbot.CreateInstallIntentResult{}, nil
}

func (f *fakeInstallHandler) HandleOAuthCallback(_ context.Context, request discordbot.InstallCallbackRequest) (discordbot.InstallCallbackResult, error) {
	f.callbackRequest = request
	return f.callbackResult, f.callbackErr
}

func TestHealthReportsMissingOptionalIntegrations(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	server := New(config.Config{
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: 1,
	}, db)
	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/healthz", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Checks["discord"].Status != "missing" {
		t.Fatalf("expected discord missing, got %+v", body.Checks["discord"])
	}
	if body.Checks["ai_service"].Status != "missing" {
		t.Fatalf("expected AI service missing, got %+v", body.Checks["ai_service"])
	}
	if body.Checks["brave_search"].Status != "missing" {
		t.Fatalf("expected brave search missing, got %+v", body.Checks["brave_search"])
	}
	if body.Checks["sol_payments"].Status != "missing" {
		t.Fatalf("expected SOL payments missing, got %+v", body.Checks["sol_payments"])
	}
	if body.Checks["local_storage"].Status != "ok" {
		t.Fatalf("expected local storage ok, got %+v", body.Checks["local_storage"])
	}
}

func TestMetricsReportsLocalState(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	if err := db.DB.Create(&store.Job{Kind: "fixture", Status: "queued", RunAfter: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create job fixture: %v", err)
	}
	if err := db.DB.Create(&store.UsageEvent{Command: "ask", Success: false, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create usage fixture: %v", err)
	}
	if err := db.DB.Create(&store.DiscordEvent{GuildID: "guild-1", ChannelID: "channel-1", EventType: "message_create", CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create discord event fixture: %v", err)
	}

	server := New(config.Config{
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
	}, db)
	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/metrics", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("metrics request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	body := string(data)
	for _, expected := range []string{
		"panda_sqlite_up 1",
		"panda_brave_search_configured 0",
		"panda_queue_depth 1",
		"panda_usage_events_total 1",
		"panda_usage_events_failed_total 1",
		"panda_discord_events_total 1",
		"panda_discord_event_cache_size 1",
		"panda_discord_intent_guild_members_enabled 0",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected metrics body to contain %q, got:\n%s", expected, body)
		}
	}
}

func TestDiscordInstallCallbackPassesDiscordInstallHints(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	handler := &fakeInstallHandler{
		callbackResult: discordbot.InstallCallbackResult{
			RedirectURL: "https://landing.example.test/install/success?guild_id=guild-1&status=success",
		},
	}
	server := New(config.Config{
		PublicAppURL:        "https://landing.example.test",
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
	}, db).WithInstallHandler(handler)

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/discord/install/callback?state=st_1&code=code_1&guild_id=guild-1&permissions=1024", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("install callback failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "https://landing.example.test/install/success/?guild_id=guild-1&status=success" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
	if handler.callbackRequest.State != "st_1" || handler.callbackRequest.Code != "code_1" || handler.callbackRequest.GuildID != "guild-1" || handler.callbackRequest.PermissionBitfield != "1024" {
		t.Fatalf("callback hints were not passed through: %+v", handler.callbackRequest)
	}
}

func TestDiscordInstallCallbackStripsPortFromInstallResultRedirect(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	handler := &fakeInstallHandler{
		callbackResult: discordbot.InstallCallbackResult{
			GuildID:     "guild-1",
			IntentID:    "intent-1",
			RedirectURL: "https://pandaclanker.xyz:8080/install/success/?guild_id=guild-1&status=success",
		},
	}
	server := New(config.Config{
		Environment:         "production",
		PublicAppURL:        "https://pandaclanker.xyz",
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
	}, db).WithInstallHandler(handler)

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/discord/install/callback?state=st_1&code=code_1&guild_id=guild-1&permissions=1024", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("install callback failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "https://pandaclanker.xyz/install/success/?guild_id=guild-1&status=success" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

func TestDiscordInstallCallbackKeepsFailureRedirectInProduction(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	handler := &fakeInstallHandler{callbackErr: errors.New("discord oauth callback could not verify the installed guild")}
	server := New(config.Config{
		Environment:         "production",
		PublicAppURL:        "https://landing.example.test",
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
	}, db).WithInstallHandler(handler)

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/discord/install/callback?state=st_1&code=code_1&guild_id=guild-1&permissions=1024", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("install callback failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "https://landing.example.test/install/failed/?error=install_failed&status=failed" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

func TestInstallFailureRedirectStripsNonLocalPort(t *testing.T) {
	if got := installFailureRedirect("https://pandaclanker.xyz:8080", errors.New("install failed")); got != "https://pandaclanker.xyz/install/failed/?error=install_failed&status=failed" {
		t.Fatalf("unexpected failure redirect: %q", got)
	}
}

func TestDiscordInstallCallbackAssumesSuccessForLocalDevelopment(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	handler := &fakeInstallHandler{callbackErr: errors.New("discord oauth callback could not verify the installed guild")}
	server := New(config.Config{
		Environment:         "development",
		PublicAppURL:        "http://localhost:4321",
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
	}, db).WithInstallHandler(handler)

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/discord/install/callback?state=st_1&code=code_1&guild_id=guild-1&permissions=1024", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("install callback failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "http://localhost:4321/install/success/?guild_id=guild-1&status=success" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

func TestDiscordInstallCallbackKeepsFailureRedirectForRemoteDevelopmentURL(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	handler := &fakeInstallHandler{callbackErr: errors.New("discord oauth callback could not verify the installed guild")}
	server := New(config.Config{
		Environment:         "development",
		PublicAppURL:        "https://landing.example.test",
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
	}, db).WithInstallHandler(handler)

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/discord/install/callback?state=st_1&code=code_1&guild_id=guild-1&permissions=1024", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("install callback failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); location != "https://landing.example.test/install/failed/?error=install_failed&status=failed" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

func TestDiscordWebhookPingRequiresValidSignature(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	handler := &fakeDiscordWebhookHandler{}
	server := New(config.Config{
		DiscordApplicationID: "app-1",
		DiscordPublicKey:     hex.EncodeToString(publicKey),
		SQLitePath:           ":memory:",
		DataDir:              t.TempDir(),
		OpenRouterBaseURL:    "https://openrouter.ai/api/v1",
		OpenRouterModel:      "openrouter/auto",
		Port:                 "8080",
		UserRateLimit:        5,
		UserRateLimitWindow:  time.Minute,
	}, db).WithDiscordWebhookHandler(handler)

	req := signedDiscordWebhookRequest(t, `{"version":1,"application_id":"app-1","type":0}`, privateKey)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("webhook ping failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if len(handler.events) != 0 {
		t.Fatalf("ping should not dispatch events, got %+v", handler.events)
	}

	req = signedDiscordWebhookRequest(t, `{"version":1,"application_id":"app-1","type":0}`, privateKey)
	req.Header.Set("X-Signature-Ed25519", strings.Repeat("0", ed25519.SignatureSize*2))
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("invalid signature request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid signature, got %d", resp.StatusCode)
	}
}

func TestDiscordWebhookDispatchesVerifiedEvent(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	handler := &fakeDiscordWebhookHandler{}
	server := New(config.Config{
		DiscordApplicationID: "app-1",
		DiscordPublicKey:     hex.EncodeToString(publicKey),
		SQLitePath:           ":memory:",
		DataDir:              t.TempDir(),
		OpenRouterBaseURL:    "https://openrouter.ai/api/v1",
		OpenRouterModel:      "openrouter/auto",
		Port:                 "8080",
		UserRateLimit:        5,
		UserRateLimitWindow:  time.Minute,
	}, db).WithDiscordWebhookHandler(handler)

	body := `{"version":1,"application_id":"app-1","type":1,"event":{"type":"APPLICATION_AUTHORIZED","timestamp":"2026-06-20T12:00:00Z","data":{"ok":true}}}`
	resp, err := server.Test(signedDiscordWebhookRequest(t, body, privateKey))
	if err != nil {
		t.Fatalf("webhook event failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if len(handler.events) != 1 || handler.events[0].Type != "APPLICATION_AUTHORIZED" || !strings.Contains(string(handler.events[0].Data), `"ok":true`) {
		t.Fatalf("unexpected dispatched events: %+v", handler.events)
	}
}

func TestSolPaymentOrderEndpointsCreateAndFetchOrders(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	server := New(solHTTPConfig(t), db).WithBillingService(solBillingService(db))
	req, _ := stdhttp.NewRequest(stdhttp.MethodOptions, "/billing/sol/orders", nil)
	req.Header.Set("Origin", "https://panda.example")
	req.Header.Set("Access-Control-Request-Method", stdhttp.MethodPost)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("payment preflight request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "https://panda.example" {
		t.Fatalf("unexpected payment preflight response: status=%d origin=%q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/billing/sol/orders", strings.NewReader(`{
		"billing_owner_user_id": "owner-1",
		"plan": "plus",
		"support_email": "owner@example.com"
	}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("create SOL order request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	var order billing.SolPaymentOrderView
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if order.OrderID == "" || order.Reference == "" || order.Status != billing.SolOrderStatusPending {
		t.Fatalf("unexpected created order identifiers: %+v", order)
	}
	if order.GuildID != "" {
		t.Fatalf("expected account-level order without guild binding, got %+v", order)
	}
	if order.ExpectedLamports != 49_000_000 || order.DestinationWallet != "treasury-wallet" || order.Cluster != "devnet" {
		t.Fatalf("unexpected created order payment fields: %+v", order)
	}
	if order.PaymentURL != "" {
		t.Fatalf("expected no client-side payment URL, got %q", order.PaymentURL)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodGet, "/billing/sol/orders/"+order.OrderID, nil)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("get SOL order request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var fetched billing.SolPaymentOrderView
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.OrderID != order.OrderID || fetched.Reference != order.Reference || fetched.Plan != billing.PlanPlus {
		t.Fatalf("unexpected fetched order: %+v", fetched)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/billing/sol/orders/"+order.OrderID+"/activation-key", nil)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("pending activation key request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 409 for pending order key reveal, got %d: %s", resp.StatusCode, body)
	}
}

func TestBillingEntitlementEndpointReportsTrialUsage(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	service := billing.NewService(repository.NewBillingRepository(db.DB), billing.Config{
		PublicURL: "https://panda.example",
	})
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.EnsureTrial(t.Context(), billing.TrialSeed{
		GuildID:            "guild-1",
		BillingOwnerUserID: "owner-1",
		AuthorizedAt:       now,
	}); err != nil {
		t.Fatalf("ensure trial: %v", err)
	}
	if err := service.SyncCurrentUsage(t.Context(), "guild-1", billing.MetricAIResponse, 42); err != nil {
		t.Fatalf("sync AI usage: %v", err)
	}
	if err := service.SyncCurrentUsage(t.Context(), "guild-1", billing.MetricWebSearch, 3); err != nil {
		t.Fatalf("sync search usage: %v", err)
	}
	if err := service.SyncCurrentUsage(t.Context(), "guild-1", billing.MetricImageGeneration, 2); err != nil {
		t.Fatalf("sync image usage: %v", err)
	}
	if err := service.SyncCurrentUsage(t.Context(), "guild-1", billing.MetricKnowledgeStorageByte, 1024); err != nil {
		t.Fatalf("sync storage usage: %v", err)
	}

	server := New(config.Config{
		SQLitePath:             ":memory:",
		DataDir:                t.TempDir(),
		OpenRouterBaseURL:      "https://openrouter.ai/api/v1",
		OpenRouterModel:        "openrouter/auto",
		Port:                   "8080",
		UserRateLimit:          50,
		UserRateLimitWindow:    time.Minute,
		BillingAllowedOrigins:  []string{"http://localhost:4321"},
		SolanaPlanLamports:     map[string]int64{},
		SolanaOrderExpiration:  time.Hour,
		SolanaActivationKeyTTL: time.Hour,
	}, db).WithBillingService(service)

	req, _ := stdhttp.NewRequest(stdhttp.MethodOptions, "/billing/entitlements/guild-1", nil)
	req.Header.Set("Origin", "http://localhost:4321")
	req.Header.Set("Access-Control-Request-Method", stdhttp.MethodGet)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("entitlement preflight request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:4321" {
		t.Fatalf("unexpected entitlement preflight response: status=%d origin=%q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodGet, "/billing/entitlements/guild-1", nil)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("entitlement request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var body billingEntitlementResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.GuildID != "guild-1" || body.Plan != billing.PlanTrial || body.DisplayName != "Trial" || body.Status != billing.StatusTrialing {
		t.Fatalf("unexpected entitlement response: %+v", body)
	}
	if body.TrialEndsAt == nil || !body.TrialEndsAt.Equal(now.Add(billing.TrialDuration)) {
		t.Fatalf("unexpected trial end: %+v", body.TrialEndsAt)
	}
	if got := body.Usage["ai_responses"]; got.Used != 42 || got.Limit != 250 || got.Remaining != 208 {
		t.Fatalf("unexpected AI usage: %+v", got)
	}
	if got := body.Usage["web_searches"]; got.Used != 3 || got.Limit != 20 || got.Remaining != 17 {
		t.Fatalf("unexpected search usage: %+v", got)
	}
	if got := body.Usage["image_generations"]; got.Used != 2 || got.Limit != 5 || got.Remaining != 3 {
		t.Fatalf("unexpected image generation usage: %+v", got)
	}
	if got := body.Usage["knowledge_storage"]; got.Used != 1024 || got.Limit != 25*1024*1024 || got.Remaining != 25*1024*1024-1024 {
		t.Fatalf("unexpected storage usage: %+v", got)
	}
}

func TestAdminCouponEndpointsManageCoupons(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	cfg := solHTTPConfig(t)
	treasuryWallet, treasuryPrivateKey := adminTestWallet(t)
	cfg.SolanaTreasuryWallet = treasuryWallet
	cfg.BillingAllowedOrigins = []string{"http://localhost:4321"}
	server := New(cfg, db).WithBillingService(solBillingService(db))

	req, _ := stdhttp.NewRequest(stdhttp.MethodOptions, "/admin/coupons", nil)
	req.Header.Set("Origin", "http://localhost:4321")
	req.Header.Set("Access-Control-Request-Method", stdhttp.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("admin preflight request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:4321" {
		t.Fatalf("unexpected admin preflight response: status=%d origin=%q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers")), "authorization") {
		t.Fatalf("expected authorization header to be allowed, got %q", resp.Header.Get("Access-Control-Allow-Headers"))
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodGet, "/admin/coupons", nil)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("unauthorized admin request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusUnauthorized {
		t.Fatalf("expected 401 without admin wallet session, got %d", resp.StatusCode)
	}
	sessionToken := createAdminSessionForTest(t, server, treasuryWallet, treasuryPrivateKey)

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/coupons", strings.NewReader(`{
		"plan": "plus",
		"discount_lamports": 49000000,
		"coupon_code": "PLUS-FREE",
		"max_redemptions": 2,
		"expires_at": "2026-12-31",
		"note": "launch"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("create coupon request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	var created adminCouponCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Code != "PLUS-FREE" || created.Coupon.Plan != billing.PlanPlus || created.Coupon.DiscountLamports != 49_000_000 || created.Coupon.MaxRedemptions != 2 {
		t.Fatalf("unexpected created coupon: %+v", created)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/coupons", strings.NewReader(`{
		"plan": "plus",
		"discount_lamports": 49000000,
		"coupon_code": "PLUS-FREE"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("duplicate coupon request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 409 duplicate, got %d: %s", resp.StatusCode, body)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodGet, "/admin/coupons", nil)
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("list coupons request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var list adminCouponListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Coupons) != 1 || list.Coupons[0].CouponID != created.Coupon.CouponID || list.Coupons[0].CodePrefix != "PLUS-FREE" {
		t.Fatalf("unexpected coupon list: %+v", list)
	}
	if list.PlanLamports[billing.PlanPlus] != 49_000_000 {
		t.Fatalf("expected plan lamports in response, got %+v", list.PlanLamports)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/coupons/"+created.Coupon.CouponID+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("revoke coupon request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 revoke, got %d: %s", resp.StatusCode, body)
	}
	var revoked billing.CouponView
	if err := json.NewDecoder(resp.Body).Decode(&revoked); err != nil {
		t.Fatalf("decode revoke response: %v", err)
	}
	if revoked.Status != billing.CouponStatusRevoked {
		t.Fatalf("expected revoked coupon, got %+v", revoked)
	}
}

func TestAdminRuntimeEndpointsManageMaintenanceMode(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	cfg := solHTTPConfig(t)
	treasuryWallet, treasuryPrivateKey := adminTestWallet(t)
	cfg.SolanaTreasuryWallet = treasuryWallet
	server := New(cfg, db).WithRuntimeStatus(runtimecontrol.NewService(repository.NewRuntimeStatusRepository(db.DB)))

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/admin/runtime", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("unauthorized runtime request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusUnauthorized {
		t.Fatalf("expected 401 without admin wallet session, got %d", resp.StatusCode)
	}

	sessionToken := createAdminSessionForTest(t, server, treasuryWallet, treasuryPrivateKey)
	req, _ = stdhttp.NewRequest(stdhttp.MethodGet, "/admin/runtime", nil)
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("get runtime request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var initial adminRuntimeStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&initial); err != nil {
		t.Fatalf("decode initial runtime response: %v", err)
	}
	if initial.Disabled || initial.EffectiveMessage != runtimecontrol.DefaultMaintenanceMessage || initial.DefaultMessage != runtimecontrol.DefaultMaintenanceMessage {
		t.Fatalf("unexpected initial runtime status: %+v", initial)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/runtime", strings.NewReader(`{
		"disabled": true,
		"message": "Panda is sleeping, maintenance in progress"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("update runtime request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 update, got %d: %s", resp.StatusCode, body)
	}
	var disabled adminRuntimeStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&disabled); err != nil {
		t.Fatalf("decode disabled runtime response: %v", err)
	}
	if !disabled.Disabled || disabled.Message != "Panda is sleeping, maintenance in progress" || disabled.EffectiveMessage != disabled.Message {
		t.Fatalf("unexpected disabled runtime status: %+v", disabled)
	}
	if !strings.Contains(disabled.UpdatedBy, treasuryWallet) {
		t.Fatalf("expected treasury wallet actor, got %+v", disabled)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/runtime", strings.NewReader(`{"disabled": false, "message": ""}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("enable runtime request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 enable, got %d: %s", resp.StatusCode, body)
	}
	var enabled adminRuntimeStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&enabled); err != nil {
		t.Fatalf("decode enabled runtime response: %v", err)
	}
	if enabled.Disabled || enabled.EffectiveMessage != runtimecontrol.DefaultMaintenanceMessage {
		t.Fatalf("unexpected enabled runtime status: %+v", enabled)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/runtime", strings.NewReader(`{"disabled": true, "message": "`+strings.Repeat("x", 501)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("long message runtime request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for long message, got %d: %s", resp.StatusCode, body)
	}
}

func TestAdminAuthRequiresConfiguredTreasuryWallet(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	cfg := solHTTPConfig(t)
	cfg.SolanaTreasuryWallet = ""
	server := New(cfg, db).WithBillingService(solBillingService(db))
	req, _ := stdhttp.NewRequest(stdhttp.MethodPost, "/admin/auth/challenge", strings.NewReader(`{"wallet":"anything"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("admin request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusServiceUnavailable {
		t.Fatalf("expected 503 without configured treasury wallet, got %d", resp.StatusCode)
	}
}

func TestInstallEndpointsAllowLocalLandingDevOrigin(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	server := New(config.Config{
		Environment:         "development",
		PublicAppURL:        "https://panda.example",
		SQLitePath:          ":memory:",
		DataDir:             t.TempDir(),
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
	}, db)
	req, _ := stdhttp.NewRequest(stdhttp.MethodOptions, "/install/features", nil)
	req.Header.Set("Origin", "http://localhost:4321")
	req.Header.Set("Access-Control-Request-Method", stdhttp.MethodGet)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("install preflight request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:4321" {
		t.Fatalf("unexpected install preflight response: status=%d origin=%q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func adminTestWallet(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate treasury key: %v", err)
	}
	return encodeBase58(publicKey), privateKey
}

func createAdminSessionForTest(t *testing.T, server *Server, wallet string, privateKey ed25519.PrivateKey) string {
	t.Helper()
	req, _ := stdhttp.NewRequest(stdhttp.MethodPost, "/admin/auth/challenge", strings.NewReader(`{"wallet":"`+wallet+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("challenge request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201 challenge, got %d: %s", resp.StatusCode, body)
	}
	var challenge adminAuthChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&challenge); err != nil {
		t.Fatalf("decode challenge response: %v", err)
	}
	signature := ed25519.Sign(privateKey, []byte(challenge.Message))
	body := fmt.Sprintf(`{
		"challenge_id": %q,
		"wallet": %q,
		"signature": %q,
		"signed_message": %q
	}`, challenge.ChallengeID, wallet, base64.StdEncoding.EncodeToString(signature), base64.StdEncoding.EncodeToString([]byte(challenge.Message)))
	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/auth/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("session request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 session, got %d: %s", resp.StatusCode, body)
	}
	var session adminSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if session.SessionToken == "" || session.Wallet != wallet {
		t.Fatalf("unexpected admin session: %+v", session)
	}
	return session.SessionToken
}

func encodeBase58(data []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	value := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var encoded []byte
	for value.Cmp(zero) > 0 {
		value.DivMod(value, base, mod)
		encoded = append(encoded, alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		encoded = append(encoded, alphabet[0])
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}

func solHTTPConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		SQLitePath:             ":memory:",
		DataDir:                t.TempDir(),
		PublicAppURL:           "https://panda.example",
		OpenRouterBaseURL:      "https://openrouter.ai/api/v1",
		OpenRouterModel:        "openrouter/auto",
		Port:                   "8080",
		UserRateLimit:          50,
		UserRateLimitWindow:    time.Minute,
		SolanaRPCURL:           "https://api.devnet.solana.com",
		SolanaCluster:          "devnet",
		SolanaTreasuryWallet:   "treasury-wallet",
		SolanaConfirmation:     "finalized",
		SolanaOrderExpiration:  time.Hour,
		SolanaActivationKeyTTL: time.Hour,
		SolanaPlanLamports:     map[string]int64{billing.PlanStarter: 19_000_000, billing.PlanPlus: 49_000_000, billing.PlanPro: 99_000_000, billing.PlanBusiness: 249_000_000},
	}
}

func solBillingService(db *store.Store) *billing.Service {
	return billing.NewService(repository.NewBillingRepository(db.DB), billing.Config{
		PublicURL:              "https://panda.example",
		SolanaRPCURL:           "https://api.devnet.solana.com",
		SolanaCluster:          "devnet",
		SolanaTreasuryWallet:   "treasury-wallet",
		SolanaConfirmation:     "finalized",
		SolanaOrderExpiration:  time.Hour,
		SolanaActivationKeyTTL: time.Hour,
		SolanaPlanLamports: map[string]int64{
			billing.PlanStarter:  19_000_000,
			billing.PlanPlus:     49_000_000,
			billing.PlanPro:      99_000_000,
			billing.PlanBusiness: 249_000_000,
		},
	})
}

func signedDiscordWebhookRequest(t *testing.T, body string, privateKey ed25519.PrivateKey) *stdhttp.Request {
	t.Helper()
	timestamp := time.Now().UTC().Unix()
	timestampText := strconv.FormatInt(timestamp, 10)
	message := append([]byte(timestampText), []byte(body)...)
	signature := ed25519.Sign(privateKey, message)
	req, err := stdhttp.NewRequest(stdhttp.MethodPost, "/discord/webhook-events", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature-Timestamp", timestampText)
	req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(signature))
	return req
}

func TestAdminGuildEndpointsListAndUpdateSubscription(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	if _, err := guilds.RecordAuthorizedInstall(t.Context(), repository.GuildInstall{
		GuildID:           "guild-1",
		Name:              "Test Guild",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "owner-1",
	}); err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}

	billingService := solBillingService(db)
	if _, err := billingService.EnsureTrial(t.Context(), billing.TrialSeed{GuildID: "guild-1", BillingOwnerUserID: "owner-1", Email: "owner@example.com"}); err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}
	if _, err := billingService.BeginUsage(t.Context(), "guild-1", billing.MetricImageGeneration, 1); err != nil {
		t.Fatalf("BeginUsage image generation: %v", err)
	}

	cfg := solHTTPConfig(t)
	treasuryWallet, treasuryPrivateKey := adminTestWallet(t)
	cfg.SolanaTreasuryWallet = treasuryWallet
	server := New(cfg, db).WithBillingService(billingService).WithGuildRepository(guilds)

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/admin/guilds", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("unauthorized guild list failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}

	sessionToken := createAdminSessionForTest(t, server, treasuryWallet, treasuryPrivateKey)

	req, _ = stdhttp.NewRequest(stdhttp.MethodGet, "/admin/guilds", nil)
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("guild list request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var list adminGuildListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode guild list: %v", err)
	}
	if list.Total != 1 || len(list.Guilds) != 1 {
		t.Fatalf("expected one guild, got %+v", list)
	}
	guild := list.Guilds[0]
	if guild.GuildID != "guild-1" || guild.Name != "Test Guild" {
		t.Fatalf("unexpected guild metadata: %+v", guild)
	}
	if guild.Billing == nil || !guild.Billing.HasSubscription || guild.Billing.Plan != billing.PlanTrial {
		t.Fatalf("expected trial billing, got %+v", guild.Billing)
	}
	if guild.Billing.Email != "owner@example.com" {
		t.Fatalf("expected billing email, got %q", guild.Billing.Email)
	}
	if guild.Billing.Limits == nil || guild.Billing.Limits.ImageGenerations != 5 {
		t.Fatalf("expected image generation limit in admin guild view, got %+v", guild.Billing.Limits)
	}
	if guild.Billing.Usage.ImageGenerations != 1 {
		t.Fatalf("expected image generation usage in admin guild view, got %+v", guild.Billing.Usage)
	}
	if len(list.PlanCatalog) == 0 || len(list.Statuses) == 0 {
		t.Fatalf("expected plan catalog and statuses, got %+v", list)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/guilds/guild-1/subscription", strings.NewReader(`{
		"plan": "pro",
		"status": "active",
		"period_end": "2026-12-31",
		"cancel_at_period_end": true
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("update subscription request failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var updated adminGuildView
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.Billing == nil || updated.Billing.Plan != billing.PlanPro || updated.Billing.StoredStatus != billing.StatusActive {
		t.Fatalf("expected pro/active subscription, got %+v", updated.Billing)
	}
	if !updated.Billing.CancelAtPeriodEnd {
		t.Fatalf("expected cancel-at-period-end, got %+v", updated.Billing)
	}

	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/admin/guilds/missing/subscription", strings.NewReader(`{"plan":"pro"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("missing guild update failed: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusNotFound {
		t.Fatalf("expected 404 for unknown guild, got %d", resp.StatusCode)
	}
}
