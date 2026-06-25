package repository

import (
	"context"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/store"
)

func TestGuildRepositoryRecordsInstallOwnership(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewGuildRepository(db.DB)
	authorizedAt := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	guild, err := repo.RecordAuthorizedInstall(ctx, GuildInstall{
		GuildID:           "guild-1",
		Name:              "Test Guild",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "owner-1",
		Locale:            "en-US",
		AuthorizedAt:      authorizedAt,
	})
	if err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}
	if guild.InstallStatus != GuildInstallStatusActive || guild.OwnerUserID != "owner-1" || guild.InstalledByUserID != "owner-1" || guild.LeftAt != nil {
		t.Fatalf("unexpected guild install record: %+v", guild)
	}

	guild, ok, err := repo.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%t err=%v", ok, err)
	}
	if guild.Name != "Test Guild" || !guild.JoinedAt.Equal(authorizedAt) {
		t.Fatalf("unexpected stored guild: %+v", guild)
	}
}

func TestGuildRepositoryObservedInstallPreservesAuthorizedInstaller(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewGuildRepository(db.DB)
	if _, err := repo.RecordAuthorizedInstall(ctx, GuildInstall{
		GuildID:           "guild-1",
		Name:              "Original Guild",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "installer-1",
		Locale:            "en-US",
		AuthorizedAt:      time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}
	guild, err := repo.RecordObservedInstall(ctx, GuildInstall{
		GuildID:           "guild-1",
		Name:              "Renamed Guild",
		OwnerUserID:       "owner-2",
		InstalledByUserID: "owner-2",
		Locale:            "en-GB",
		AuthorizedAt:      time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordObservedInstall: %v", err)
	}
	if guild.Name != "Renamed Guild" || guild.OwnerUserID != "owner-2" || guild.InstalledByUserID != "installer-1" {
		t.Fatalf("observed install should refresh guild metadata without replacing installer: %+v", guild)
	}
}

func TestGuildRepositoryListSearchesAndPaginates(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewGuildRepository(db.DB)
	seed := []GuildInstall{
		{GuildID: "guild-alpha", Name: "Alpha Server", OwnerUserID: "owner-1", InstalledByUserID: "owner-1", AuthorizedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)},
		{GuildID: "guild-beta", Name: "Beta Server", OwnerUserID: "owner-2", InstalledByUserID: "owner-2", AuthorizedAt: time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)},
		{GuildID: "guild-gamma", Name: "Gamma Server", OwnerUserID: "owner-3", InstalledByUserID: "owner-3", AuthorizedAt: time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)},
	}
	for _, install := range seed {
		if _, err := repo.RecordAuthorizedInstall(ctx, install); err != nil {
			t.Fatalf("RecordAuthorizedInstall %s: %v", install.GuildID, err)
		}
	}

	guilds, total, err := repo.List(ctx, GuildListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(guilds) != 3 {
		t.Fatalf("expected 3 guilds, got total=%d len=%d", total, len(guilds))
	}
	if guilds[0].GuildID != "guild-gamma" {
		t.Fatalf("expected most recently joined first, got %s", guilds[0].GuildID)
	}

	guilds, total, err = repo.List(ctx, GuildListFilter{Search: "beta"})
	if err != nil {
		t.Fatalf("List search: %v", err)
	}
	if total != 1 || len(guilds) != 1 || guilds[0].GuildID != "guild-beta" {
		t.Fatalf("unexpected search result: total=%d guilds=%+v", total, guilds)
	}

	guilds, total, err = repo.List(ctx, GuildListFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("List paginate: %v", err)
	}
	if total != 3 || len(guilds) != 1 || guilds[0].GuildID != "guild-beta" {
		t.Fatalf("unexpected pagination result: total=%d guilds=%+v", total, guilds)
	}
}
