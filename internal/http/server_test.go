package http

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
	"github.com/sn0w/panda2/internal/store"
)

type fakeDiscordWebhookHandler struct {
	events []discordbot.WebhookEvent
}

func (f *fakeDiscordWebhookHandler) HandleWebhookEvent(_ context.Context, event discordbot.WebhookEvent) error {
	f.events = append(f.events, event)
	return nil
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
	if body.Checks["openrouter"].Status != "missing" {
		t.Fatalf("expected openrouter missing, got %+v", body.Checks["openrouter"])
	}
	if body.Checks["brave_search"].Status != "missing" {
		t.Fatalf("expected brave search missing, got %+v", body.Checks["brave_search"])
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
