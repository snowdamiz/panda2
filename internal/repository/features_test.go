package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/store"
)

func TestFeatureRepositoryConsumesInstallIntentOnce(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewFeatureRepository(db.DB)
	expiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := repo.CreateInstallIntent(ctx, store.InstallIntent{
		IntentID:                    "intent-1",
		StateHash:                   "hash-1",
		SelectedFeatureIDs:          `["assistant_chat"]`,
		ExpandedFeatureIDs:          `["assistant_chat"]`,
		RequestedDiscordPermissions: `["VIEW_CHANNEL"]`,
		RequestedPermissionBitfield: "1024",
		ExpiresAt:                   expiresAt,
	}); err != nil {
		t.Fatalf("CreateInstallIntent: %v", err)
	}

	consumed, err := repo.ConsumeInstallIntent(ctx, "hash-1", "guild-1", "user-1", `["VIEW_CHANNEL"]`, `["bot"]`, time.Now().UTC())
	if err != nil {
		t.Fatalf("ConsumeInstallIntent: %v", err)
	}
	if consumed.Status != InstallIntentStatusConsumed || consumed.GuildID != "guild-1" || consumed.InstallerUserID != "user-1" || consumed.ConsumedAt == nil {
		t.Fatalf("unexpected consumed intent: %+v", consumed)
	}
	if _, err := repo.ConsumeInstallIntent(ctx, "hash-1", "guild-2", "user-2", `[]`, `[]`, time.Now().UTC()); !errors.Is(err, ErrInstallIntentUnavailable) {
		t.Fatalf("expected unavailable replay, got %v", err)
	}
}

func TestFeatureRepositoryExpiresInstallIntent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewFeatureRepository(db.DB)
	if _, err := repo.CreateInstallIntent(ctx, store.InstallIntent{
		IntentID:  "intent-expired",
		StateHash: "hash-expired",
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateInstallIntent: %v", err)
	}
	if _, err := repo.ConsumeInstallIntent(ctx, "hash-expired", "guild-1", "user-1", `[]`, `[]`, time.Now().UTC()); !errors.Is(err, ErrInstallIntentExpired) {
		t.Fatalf("expected expired, got %v", err)
	}
	intent, ok, err := repo.GetInstallIntentByStateHash(ctx, "hash-expired")
	if err != nil || !ok {
		t.Fatalf("lookup expired intent: ok=%t err=%v", ok, err)
	}
	if intent.Status != InstallIntentStatusExpired {
		t.Fatalf("expected expired status, got %+v", intent)
	}
}

func TestFeatureRepositoryReplacesGuildFeatures(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewFeatureRepository(db.DB)
	now := time.Now().UTC()
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{"assistant_chat", "web_search"}, "intent-1", "user-1", now); err != nil {
		t.Fatalf("SetGuildFeatures initial: %v", err)
	}
	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{"assistant_chat", "polls"}, "intent-2", "user-2", now.Add(time.Minute)); err != nil {
		t.Fatalf("SetGuildFeatures replace: %v", err)
	}
	enabled, err := repo.EnabledFeatureSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledFeatureSet: %v", err)
	}
	if _, ok := enabled["assistant_chat"]; !ok {
		t.Fatalf("assistant_chat should remain enabled: %+v", enabled)
	}
	if _, ok := enabled["polls"]; !ok {
		t.Fatalf("polls should be enabled: %+v", enabled)
	}
	if _, ok := enabled["web_search"]; ok {
		t.Fatalf("web_search should be disabled after replacement: %+v", enabled)
	}
	states, err := repo.ListGuildFeatures(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListGuildFeatures: %v", err)
	}
	if len(states) != 3 {
		t.Fatalf("expected enabled and disabled rows, got %+v", states)
	}
}
