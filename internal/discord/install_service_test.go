package discord

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestInstallServiceAcceptsServerOwnerInstall(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB))
	if err := service.HandleWebhookEvent(ctx, authorizedEvent(t, "owner-1", "owner-1")); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	guild, ok, err := guilds.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get guild: ok=%t err=%v", ok, err)
	}
	if guild.InstallStatus != repository.GuildInstallStatusActive || guild.InstalledByUserID != "owner-1" || guild.OwnerUserID != "owner-1" {
		t.Fatalf("unexpected accepted guild: %+v", guild)
	}
	assertAuditAction(t, db, "discord.install.authorized")
}

func TestInstallServiceAcceptsNonOwnerInstallerAsPandaOwner(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB))
	if err := service.HandleWebhookEvent(ctx, authorizedEvent(t, "installer-1", "owner-1")); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	guild, ok, err := guilds.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get guild: ok=%t err=%v", ok, err)
	}
	if guild.InstallStatus != repository.GuildInstallStatusActive || guild.InstalledByUserID != "installer-1" || guild.OwnerUserID != "owner-1" || guild.LeftAt != nil {
		t.Fatalf("unexpected accepted guild: %+v", guild)
	}
	assertAuditAction(t, db, "discord.install.authorized")
}

func TestInstallServiceDoesNotRestrictChannelsByDefault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	access := repository.NewAccessRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB))
	if err := service.HandleWebhookEvent(ctx, authorizedEvent(t, "owner-1", "owner-1")); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	rules, err := access.ListChannelRules(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListChannelRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected no default channel restrictions, got %+v", rules)
	}
	assertAuditAction(t, db, "discord.install.authorized")
}

func authorizedEvent(t *testing.T, installerID, ownerID string) WebhookEvent {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"integration_type": 0,
		"scopes":           []string{"bot", "applications.commands"},
		"user": map[string]any{
			"id":       installerID,
			"username": "installer",
		},
		"guild": map[string]any{
			"id":               "guild-1",
			"name":             "Test Guild",
			"owner_id":         ownerID,
			"preferred_locale": "en-US",
		},
	})
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	return WebhookEvent{
		Type:      webhookEventApplicationAuthorized,
		Timestamp: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Data:      data,
	}
}

func assertAuditAction(t *testing.T, db *store.Store, action string) {
	t.Helper()
	var count int64
	if err := db.DB.Model(&store.AuditEvent{}).Where("action = ?", action).Count(&count).Error; err != nil {
		t.Fatalf("count audit action %s: %v", action, err)
	}
	if count != 1 {
		t.Fatalf("expected one %s audit event, got %d", action, count)
	}
}
