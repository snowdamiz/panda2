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
