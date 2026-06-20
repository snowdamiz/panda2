package http

import (
	"encoding/json"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/store"
)

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
