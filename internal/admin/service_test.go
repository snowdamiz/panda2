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
		allowed    bool
	}{
		{name: "explicit soul writer", roleID: "role-soul", permission: PermissionAssistantSoulWrite, allowed: true},
		{name: "moderator", roleID: "role-mod", permission: PermissionModerationUse},
		{name: "creator", roleID: "role-creator", permission: PermissionToolComposeDraft},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.AddRolePermission(ctx, "guild-1", "admin", tc.roleID, tc.permission); err != nil {
				t.Fatalf("AddRolePermission: %v", err)
			}
			allowed, err := service.CanWriteSoul(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{tc.roleID}})
			if err != nil {
				t.Fatalf("CanWriteSoul: %v", err)
			}
			if allowed != tc.allowed {
				t.Fatalf("unexpected soul writer access, allowed=%t want=%t", allowed, tc.allowed)
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

func TestAdminRoleHasGuildControl(t *testing.T) {
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
	if _, err := service.SetAdminRole(ctx, "guild-1", "owner", "role-mod"); err != nil {
		t.Fatalf("SetAdminRole: %v", err)
	}
	if _, err := service.SetAdminRole(ctx, "guild-1", "owner", "guild-1"); err == nil {
		t.Fatal("expected @everyone admin role to be rejected")
	}

	request := AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-mod"}}
	for name, check := range map[string]func(context.Context, AssistantAccessRequest) (bool, error){
		"config write":   service.CanWriteConfig,
		"moderation use": service.CanUseModeration,
		"assistant use":  service.CanUseAssistant,
	} {
		allowed, err := check(ctx, request)
		if err != nil || !allowed {
			t.Fatalf("expected admin role to allow %s, allowed=%t err=%v", name, allowed, err)
		}
	}

	allowed, err := service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-user"}})
	if err != nil || allowed {
		t.Fatalf("expected non-admin role denial, allowed=%t err=%v", allowed, err)
	}

	if _, err := service.SetAdminRole(ctx, "guild-1", "owner", "role-admin"); err != nil {
		t.Fatalf("SetAdminRole replacement: %v", err)
	}
	allowed, err = service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-mod"}})
	if err != nil || allowed {
		t.Fatalf("expected replaced admin role to stop granting control, allowed=%t err=%v", allowed, err)
	}
	allowed, err = service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-admin"}})
	if err != nil || !allowed {
		t.Fatalf("expected new admin role to grant control, allowed=%t err=%v", allowed, err)
	}
}

func TestRoleProfilesGrantExpectedAccess(t *testing.T) {
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

	if _, err := service.ApplyRoleProfile(ctx, "guild-1", "owner", "role-pickle", "mod"); err != nil {
		t.Fatalf("ApplyRoleProfile moderator: %v", err)
	}
	allowed, err := service.CanUseModeration(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-pickle"}})
	if err != nil || !allowed {
		t.Fatalf("expected moderator profile to grant moderation, allowed=%t err=%v", allowed, err)
	}
	allowed, err = service.CanUseAssistant(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-pickle"}})
	if err != nil || !allowed {
		t.Fatalf("expected moderator profile to grant assistant use, allowed=%t err=%v", allowed, err)
	}

	if _, err := service.ApplyRoleProfile(ctx, "guild-1", "owner", "role-admin", "admin"); err != nil {
		t.Fatalf("ApplyRoleProfile admin: %v", err)
	}
	allowed, err = service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-admin"}})
	if err != nil || !allowed {
		t.Fatalf("expected admin profile to grant config write, allowed=%t err=%v", allowed, err)
	}

	if err := service.RemoveRoleProfile(ctx, "guild-1", "owner", "role-pickle", "moderator"); err != nil {
		t.Fatalf("RemoveRoleProfile moderator: %v", err)
	}
	allowed, err = service.CanUseModeration(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-pickle"}})
	if err != nil || allowed {
		t.Fatalf("expected removed moderator profile to stop granting moderation, allowed=%t err=%v", allowed, err)
	}
}

func TestSingleDiscordRoleCanHoldAdminAndModeratorProfiles(t *testing.T) {
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

	if _, err := service.ApplyRoleProfile(ctx, "guild-1", "owner", "role-staff", "moderator"); err != nil {
		t.Fatalf("ApplyRoleProfile moderator: %v", err)
	}
	if _, err := service.ApplyRoleProfile(ctx, "guild-1", "owner", "role-staff", "admin"); err != nil {
		t.Fatalf("ApplyRoleProfile admin: %v", err)
	}

	roles, err := service.ListRolePermissions(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListRolePermissions: %v", err)
	}
	for _, permission := range []string{PermissionAdminBadge, PermissionAssistantUse, PermissionModerationUse} {
		if !hasRolePermission(roles, "role-staff", permission) {
			t.Fatalf("expected role-staff to have %s in %+v", permission, roles)
		}
	}

	request := AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-staff"}}
	for name, check := range map[string]func(context.Context, AssistantAccessRequest) (bool, error){
		"config write":   service.CanWriteConfig,
		"assistant use":  service.CanUseAssistant,
		"moderation use": service.CanUseModeration,
	} {
		allowed, err := check(ctx, request)
		if err != nil || !allowed {
			t.Fatalf("expected combined role to allow %s, allowed=%t err=%v", name, allowed, err)
		}
	}

	if _, err := service.ApplyRoleProfile(ctx, "guild-1", "owner", "role-admins", "admin"); err != nil {
		t.Fatalf("ApplyRoleProfile admin replacement: %v", err)
	}
	allowed, err := service.CanWriteConfig(ctx, request)
	if err != nil || allowed {
		t.Fatalf("expected replaced admin profile to stop granting config write, allowed=%t err=%v", allowed, err)
	}
	allowed, err = service.CanUseModeration(ctx, request)
	if err != nil || !allowed {
		t.Fatalf("expected moderator profile to survive admin replacement, allowed=%t err=%v", allowed, err)
	}
}

func hasRolePermission(roles []store.GuildRole, roleID, permission string) bool {
	for _, role := range roles {
		if role.RoleID == roleID && role.Permission == permission {
			return true
		}
	}
	return false
}

func TestWebSearchAccessDefaultsOpenUntilMapped(t *testing.T) {
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

	allowed, err := service.CanUseWebSearch(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"anyone"}})
	if err != nil || !allowed {
		t.Fatalf("expected unmapped web search to be available to everyone, allowed=%t err=%v", allowed, err)
	}

	if _, err := service.AddRolePermission(ctx, "guild-1", "admin", "role-search", PermissionAssistantWebSearch); err != nil {
		t.Fatalf("AddRolePermission: %v", err)
	}
	allowed, err = service.CanUseWebSearch(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"anyone"}})
	if err != nil || allowed {
		t.Fatalf("expected explicit web search mapping to restrict other roles, allowed=%t err=%v", allowed, err)
	}
	allowed, err = service.CanUseWebSearch(ctx, AssistantAccessRequest{GuildID: "guild-1", RoleIDs: []string{"role-search"}})
	if err != nil || !allowed {
		t.Fatalf("expected mapped role to retain web search, allowed=%t err=%v", allowed, err)
	}
}

func TestToolRoleAccessIsRoleScoped(t *testing.T) {
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
	if _, err := service.AddToolRole(ctx, "guild-1", "admin", "Web.Search", "role-search"); err != nil {
		t.Fatalf("AddToolRole: %v", err)
	}
	if _, err := service.AddToolRole(ctx, "guild-1", "admin", "builder_welcome", "role-builder"); err != nil {
		t.Fatalf("AddToolRole composed: %v", err)
	}

	access, err := service.ToolRoleAccess(ctx, "guild-1", []string{"role-search"})
	if err != nil {
		t.Fatalf("ToolRoleAccess: %v", err)
	}
	if strings.Join(access.RestrictedTools, ",") != "builder_welcome,web.search" {
		t.Fatalf("unexpected restricted tools: %+v", access.RestrictedTools)
	}
	if len(access.AllowedTools) != 1 || access.AllowedTools[0] != "web.search" {
		t.Fatalf("unexpected allowed tools: %+v", access.AllowedTools)
	}

	if err := service.RemoveToolRole(ctx, "guild-1", "admin", "web.search", "role-search"); err != nil {
		t.Fatalf("RemoveToolRole: %v", err)
	}
	access, err = service.ToolRoleAccess(ctx, "guild-1", []string{"role-search"})
	if err != nil {
		t.Fatalf("ToolRoleAccess after remove: %v", err)
	}
	if len(access.AllowedTools) != 0 {
		t.Fatalf("expected removed role to lose tool access, got %+v", access.AllowedTools)
	}
}

func TestInstalledGuildOwnerHasConfigAccess(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	if _, err := guilds.RecordAuthorizedInstall(ctx, repository.GuildInstall{
		GuildID:           "guild-1",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "installer-1",
	}); err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}
	service := NewService(
		repository.NewGuildConfigRepository(db.DB),
		repository.NewUsageRepository(db.DB),
		repository.NewAuditRepository(db.DB),
		memory.NewService(repository.NewKnowledgeRepository(db.DB)),
		repository.NewAccessRepository(db.DB),
		repository.NewBudgetRepository(db.DB),
		nil,
		"openrouter/auto",
	).WithGuildRepository(guilds)

	allowed, err := service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", UserID: "owner-1"})
	if err != nil || !allowed {
		t.Fatalf("expected guild owner config access, allowed=%t err=%v", allowed, err)
	}
	allowed, err = service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", UserID: "installer-1"})
	if err != nil || !allowed {
		t.Fatalf("expected installing user config access, allowed=%t err=%v", allowed, err)
	}
	allowed, err = service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", UserID: "user-1"})
	if err != nil || allowed {
		t.Fatalf("expected non-owner config denial, allowed=%t err=%v", allowed, err)
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
