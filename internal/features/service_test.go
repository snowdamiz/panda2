package features

import (
	"context"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestServiceDefaultsYouTubeClippingOnUntilExplicitlyDisabled(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := repository.NewFeatureRepository(db.DB)
	service := NewService(repo)

	if err := repo.SetGuildFeatures(ctx, "guild-1", []string{AssistantChat}, "intent-1", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("SetGuildFeatures: %v", err)
	}
	enabled, err := service.EnabledSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledSet: %v", err)
	}
	if !Has(enabled, YouTubeClipping) {
		t.Fatalf("youtube clipping should default on when no explicit row exists: %+v", enabled)
	}

	states, err := service.Status(ctx, "guild-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !featureStateEnabled(states, YouTubeClipping) {
		t.Fatalf("status should show youtube clipping as enabled by default: %+v", states)
	}

	if err := service.ReplaceGuildFeatures(ctx, "guild-1", []string{AssistantChat}, "admin_feature_change", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("ReplaceGuildFeatures disable default: %v", err)
	}
	enabled, err = service.EnabledSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledSet after disable: %v", err)
	}
	if Has(enabled, YouTubeClipping) {
		t.Fatalf("youtube clipping should stay off after explicit disable: %+v", enabled)
	}
	states, err = service.Status(ctx, "guild-1")
	if err != nil {
		t.Fatalf("Status after disable: %v", err)
	}
	if !featureStateExplicitlyDisabled(states, YouTubeClipping) {
		t.Fatalf("status should include explicit disabled youtube clipping row: %+v", states)
	}

	if err := service.ReplaceGuildFeatures(ctx, "guild-1", []string{AssistantChat, YouTubeClipping}, "admin_feature_change", "admin", time.Now().UTC()); err != nil {
		t.Fatalf("ReplaceGuildFeatures re-enable: %v", err)
	}
	enabled, err = service.EnabledSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledSet after re-enable: %v", err)
	}
	if !Has(enabled, YouTubeClipping) {
		t.Fatalf("youtube clipping should re-enable when selected: %+v", enabled)
	}
}

func featureStateEnabled(states []repository.GuildFeatureState, featureID string) bool {
	for _, state := range states {
		if state.FeatureID == featureID {
			return state.Enabled
		}
	}
	return false
}

func featureStateExplicitlyDisabled(states []repository.GuildFeatureState, featureID string) bool {
	for _, state := range states {
		if state.FeatureID == featureID {
			return !state.Enabled
		}
	}
	return false
}
