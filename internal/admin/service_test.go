package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type fakeModels struct {
	valid map[string]bool
}

func TestConfigureModelPersistsFallbacksAndRuntimeSettings(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	configs := repository.NewGuildConfigRepository(db.DB)
	service := NewService(
		configs,
		repository.NewUsageRepository(db.DB),
		repository.NewAuditRepository(db.DB),
		memory.NewService(repository.NewKnowledgeRepository(db.DB)),
		repository.NewAccessRepository(db.DB),
		repository.NewBudgetRepository(db.DB),
		fakeModels{valid: map[string]bool{
			"provider/primary":    true,
			"provider/fallback-a": true,
			"provider/fallback-b": true,
		}},
		"openrouter/auto",
	)
	if _, err := service.SetupGuild(ctx, "guild-1", "admin"); err != nil {
		t.Fatalf("SetupGuild: %v", err)
	}

	config, err := service.ConfigureModel(ctx, "guild-1", "admin", ModelSettings{
		DefaultModel:         "provider/primary",
		FallbackModels:       []string{"provider/fallback-a", "provider/fallback-b", "provider/fallback-a"},
		FallbackModelsSet:    true,
		Temperature:          0.75,
		TemperatureSet:       true,
		MaxResponseTokens:    1200,
		MaxResponseTokensSet: true,
		ToolPolicy:           "read_only",
		ToolPolicySet:        true,
	})
	if err != nil {
		t.Fatalf("ConfigureModel: %v", err)
	}
	if config.DefaultModel != "provider/primary" || config.Temperature != 0.75 || config.MaxResponseTokens != 1200 || config.ToolPolicy != "read_only" {
		t.Fatalf("unexpected config: %+v", config)
	}
	if !strings.Contains(config.FallbackModels, "provider/fallback-a") || strings.Count(config.FallbackModels, "provider/fallback-a") != 1 {
		t.Fatalf("fallback models were not normalized: %q", config.FallbackModels)
	}
}

func TestConfigureModelRejectsInvalidRuntimeSettings(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	configs := repository.NewGuildConfigRepository(db.DB)
	service := NewService(
		configs,
		repository.NewUsageRepository(db.DB),
		repository.NewAuditRepository(db.DB),
		memory.NewService(repository.NewKnowledgeRepository(db.DB)),
		repository.NewAccessRepository(db.DB),
		repository.NewBudgetRepository(db.DB),
		nil,
		"openrouter/auto",
	)
	if _, err := service.SetupGuild(ctx, "guild-1", "admin"); err != nil {
		t.Fatalf("SetupGuild: %v", err)
	}
	if _, err := service.ConfigureModel(ctx, "guild-1", "admin", ModelSettings{Temperature: 2.5, TemperatureSet: true}); err == nil {
		t.Fatal("expected invalid temperature to be rejected")
	}
	if _, err := service.ConfigureModel(ctx, "guild-1", "admin", ModelSettings{MaxResponseTokens: 12, MaxResponseTokensSet: true}); err == nil {
		t.Fatal("expected invalid max response tokens to be rejected")
	}
	if _, err := service.ConfigureModel(ctx, "guild-1", "admin", ModelSettings{ToolPolicy: "execute_anything", ToolPolicySet: true}); err == nil {
		t.Fatal("expected invalid tool policy to be rejected")
	}
}

func TestSetSoulPersistsAndSoulWritersAreDelegated(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	service := NewService(
		repository.NewGuildConfigRepository(db.DB),
		repository.NewUsageRepository(db.DB),
		repository.NewAuditRepository(db.DB),
		memory.NewService(repository.NewKnowledgeRepository(db.DB)),
		repository.NewAccessRepository(db.DB),
		repository.NewBudgetRepository(db.DB),
		nil,
		"openrouter/auto",
	)
	if _, err := service.SetupGuild(ctx, "guild-1", "admin"); err != nil {
		t.Fatalf("SetupGuild: %v", err)
	}
	config, err := service.SetSoul(ctx, "guild-1", "admin", "Be precise, warm, and a little playful.")
	if err != nil {
		t.Fatalf("SetSoul: %v", err)
	}
	if !strings.Contains(config.AgentSoul, "playful") {
		t.Fatalf("unexpected soul: %+v", config)
	}
	if _, err := service.SetSoul(ctx, "guild-1", "admin", " "); err == nil {
		t.Fatal("expected blank soul to be rejected")
	}

	for _, tc := range []struct {
		name       string
		roleID     string
		permission string
	}{
		{name: "explicit soul writer", roleID: "role-soul", permission: PermissionAssistantSoulWrite},
		{name: "moderator", roleID: "role-mod", permission: PermissionModerationUse},
		{name: "creator", roleID: "role-creator", permission: PermissionToolComposeDraft},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.AddRolePermission(ctx, "guild-1", "admin", tc.roleID, tc.permission); err != nil {
				t.Fatalf("AddRolePermission: %v", err)
			}
			allowed, err := service.CanWriteSoul(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{tc.roleID}})
			if err != nil || !allowed {
				t.Fatalf("expected soul writer access, allowed=%t err=%v", allowed, err)
			}
		})
	}
}

func TestAddRolePermissionRejectsUnknownPermission(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	service := NewService(
		repository.NewGuildConfigRepository(db.DB),
		repository.NewUsageRepository(db.DB),
		repository.NewAuditRepository(db.DB),
		memory.NewService(repository.NewKnowledgeRepository(db.DB)),
		repository.NewAccessRepository(db.DB),
		repository.NewBudgetRepository(db.DB),
		nil,
		"openrouter/auto",
	)
	if _, err := service.AddRolePermission(ctx, "guild-1", "admin", "role-1", "anything.goes"); err == nil {
		t.Fatal("expected unknown permission to be rejected")
	}
}

func (f fakeModels) ListModels(context.Context) ([]llm.Model, error) {
	return nil, nil
}

func (f fakeModels) ValidateModel(_ context.Context, slug string) (bool, error) {
	return f.valid[slug], nil
}

func TestSetModelValidatesWhenModelListerConfigured(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	configs := repository.NewGuildConfigRepository(db.DB)
	service := NewService(
		configs,
		repository.NewUsageRepository(db.DB),
		repository.NewAuditRepository(db.DB),
		memory.NewService(repository.NewKnowledgeRepository(db.DB)),
		repository.NewAccessRepository(db.DB),
		repository.NewBudgetRepository(db.DB),
		fakeModels{valid: map[string]bool{"provider/good": true}},
		"openrouter/auto",
	)
	if _, err := service.SetupGuild(ctx, "guild-1", "admin"); err != nil {
		t.Fatalf("SetupGuild: %v", err)
	}
	if _, err := service.SetModel(ctx, "guild-1", "admin", "provider/missing"); err == nil {
		t.Fatal("expected unavailable model to be rejected")
	}
	config, err := service.SetModel(ctx, "guild-1", "admin", "provider/good")
	if err != nil {
		t.Fatalf("SetModel valid: %v", err)
	}
	if config.DefaultModel != "provider/good" {
		t.Fatalf("unexpected model %q", config.DefaultModel)
	}
}
