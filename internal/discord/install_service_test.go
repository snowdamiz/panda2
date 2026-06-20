package discord

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type fakeGuildLeaver struct {
	leftGuildIDs []string
}

func (f *fakeGuildLeaver) LeaveGuild(_ context.Context, guildID string) error {
	f.leftGuildIDs = append(f.leftGuildIDs, guildID)
	return nil
}

func TestInstallServiceAcceptsServerOwnerInstall(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	leaver := &fakeGuildLeaver{}
	guilds := repository.NewGuildRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB), leaver)
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
	if len(leaver.leftGuildIDs) != 0 {
		t.Fatalf("owner install should not leave guilds, left=%v", leaver.leftGuildIDs)
	}
	assertAuditAction(t, db, "discord.install.authorized")
}

func TestInstallServiceRejectsNonOwnerInstallAndLeavesGuild(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	leaver := &fakeGuildLeaver{}
	guilds := repository.NewGuildRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB), leaver)
	if err := service.HandleWebhookEvent(ctx, authorizedEvent(t, "admin-1", "owner-1")); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	guild, ok, err := guilds.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get guild: ok=%t err=%v", ok, err)
	}
	if guild.InstallStatus != repository.GuildInstallStatusLeft || guild.InstalledByUserID != "admin-1" || guild.OwnerUserID != "owner-1" || guild.LeftAt == nil {
		t.Fatalf("unexpected denied guild: %+v", guild)
	}
	if len(leaver.leftGuildIDs) != 1 || leaver.leftGuildIDs[0] != "guild-1" {
		t.Fatalf("expected denied guild to be left, got %v", leaver.leftGuildIDs)
	}
	assertAuditAction(t, db, "discord.install.denied")
	assertAuditAction(t, db, "discord.install.left")
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
