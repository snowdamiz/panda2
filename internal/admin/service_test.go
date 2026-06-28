package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

func TestConfigureBehaviorPersistsRuntimeSettings(t *testing.T) {
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
	)

	config, err := service.ConfigureBehavior(ctx, "guild-1", "admin", BehaviorSettings{
		Temperature:     0.75,
		TemperatureSet:  true,
		AnswerLength:    "detailed",
		AnswerLengthSet: true,
		ToolPolicy:      "read_only",
		ToolPolicySet:   true,
	})
	if err != nil {
		t.Fatalf("ConfigureBehavior: %v", err)
	}
	if config.Temperature != 0.75 || config.MaxResponseTokens != 1600 || config.ToolPolicy != "read_only" {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestConfigureBehaviorRejectsInvalidRuntimeSettings(t *testing.T) {
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
	)
	if _, err := service.ConfigureBehavior(ctx, "guild-1", "admin", BehaviorSettings{Temperature: 2.5, TemperatureSet: true}); err == nil {
		t.Fatal("expected invalid temperature to be rejected")
	}
	if _, err := service.ConfigureBehavior(ctx, "guild-1", "admin", BehaviorSettings{MaxResponseTokens: 12, MaxResponseTokensSet: true}); err == nil {
		t.Fatal("expected invalid max response tokens to be rejected")
	}
	if _, err := service.ConfigureBehavior(ctx, "guild-1", "admin", BehaviorSettings{ToolPolicy: "execute_anything", ToolPolicySet: true}); err == nil {
		t.Fatal("expected invalid tool policy to be rejected")
	}
	if _, err := service.ConfigureBehavior(ctx, "guild-1", "admin", BehaviorSettings{ToolPolicy: "off", ToolPolicySet: true}); err == nil {
		t.Fatal("expected legacy off tool policy to be rejected")
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
	)
	if _, err := service.AddRolePermission(ctx, "guild-1", "admin", "role-1", "anything.goes"); err == nil {
		t.Fatal("expected unknown permission to be rejected")
	}
}

func TestImageGenerationPermissionIsAllowed(t *testing.T) {
	if !IsPermissionNameAllowed(PermissionAssistantImageGeneration) {
		t.Fatal("assistant image generation permission should be assignable")
	}
	if !containsString(AllPermissionNames(), PermissionAssistantImageGeneration) {
		t.Fatal("assistant image generation permission should be listed")
	}
}

func TestYouTubeClippingPermissionIsAllowed(t *testing.T) {
	if !IsPermissionNameAllowed(PermissionAssistantYouTubeClipping) {
		t.Fatal("assistant youtube clipping permission should be assignable")
	}
	if !containsString(AllPermissionNames(), PermissionAssistantYouTubeClipping) {
		t.Fatal("assistant youtube clipping permission should be listed")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func TestAdminUserHasGuildControl(t *testing.T) {
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
	)
	if _, err := service.ApplyUserProfile(ctx, "guild-1", "owner", "user-admin", "admin"); err != nil {
		t.Fatalf("ApplyUserProfile admin: %v", err)
	}
	if _, err := service.ApplyUserProfile(ctx, "guild-1", "owner", "user-other-admin", "admin"); err != nil {
		t.Fatalf("ApplyUserProfile second admin: %v", err)
	}

	request := AssistantAccessRequest{GuildID: "guild-1", UserID: "user-admin"}
	for name, check := range map[string]func(context.Context, AssistantAccessRequest) (bool, error){
		"config write":   service.CanWriteConfig,
		"moderation use": service.CanUseModeration,
		"assistant use":  service.CanUseAssistant,
	} {
		allowed, err := check(ctx, request)
		if err != nil || !allowed {
			t.Fatalf("expected admin user to allow %s, allowed=%t err=%v", name, allowed, err)
		}
	}

	allowed, err := service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", UserID: "user-regular"})
	if err != nil || allowed {
		t.Fatalf("expected regular user denial, allowed=%t err=%v", allowed, err)
	}

	if err := service.RemoveUserProfile(ctx, "guild-1", "owner", "user-admin", "admin"); err != nil {
		t.Fatalf("RemoveUserProfile admin: %v", err)
	}
	allowed, err = service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", UserID: "user-admin"})
	if err != nil || allowed {
		t.Fatalf("expected removed admin user to lose control, allowed=%t err=%v", allowed, err)
	}
	allowed, err = service.CanWriteConfig(ctx, AssistantAccessRequest{GuildID: "guild-1", UserID: "user-other-admin"})
	if err != nil || !allowed {
		t.Fatalf("expected second admin user to retain control, allowed=%t err=%v", allowed, err)
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
	)
	if _, err := service.AddToolRole(ctx, "guild-1", "admin", "Web.Search", "role-search"); err != nil {
		t.Fatalf("AddToolRole: %v", err)
	}
	if _, err := service.AddToolRole(ctx, "guild-1", "admin", "member_welcome", "role-member"); err != nil {
		t.Fatalf("AddToolRole composed: %v", err)
	}

	access, err := service.ToolRoleAccess(ctx, "guild-1", []string{"role-search"})
	if err != nil {
		t.Fatalf("ToolRoleAccess: %v", err)
	}
	if strings.Join(access.RestrictedTools, ",") != "member_welcome,web.search" {
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

func TestToolAccessSupportsDirectUsers(t *testing.T) {
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
	)
	if _, err := service.AddToolUser(ctx, "guild-1", "admin", "panda.generate_image", "user-artist"); err != nil {
		t.Fatalf("AddToolUser: %v", err)
	}

	allowedUser, err := service.ToolUserRoleAccess(ctx, "guild-1", "user-artist", nil)
	if err != nil {
		t.Fatalf("ToolUserRoleAccess user: %v", err)
	}
	if strings.Join(allowedUser.RestrictedTools, ",") != "panda.generate_image" {
		t.Fatalf("unexpected restricted tools: %+v", allowedUser.RestrictedTools)
	}
	if len(allowedUser.AllowedTools) != 1 || allowedUser.AllowedTools[0] != "panda.generate_image" {
		t.Fatalf("expected direct user to be allowed, got %+v", allowedUser.AllowedTools)
	}

	otherUser, err := service.ToolUserRoleAccess(ctx, "guild-1", "user-other", []string{"role-artist"})
	if err != nil {
		t.Fatalf("ToolUserRoleAccess other: %v", err)
	}
	if len(otherUser.AllowedTools) != 0 {
		t.Fatalf("expected other user to be restricted, got %+v", otherUser.AllowedTools)
	}

	rules, err := service.ListToolAccess(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListToolAccess: %v", err)
	}
	if len(rules) != 1 || rules[0].SubjectType != "user" || rules[0].SubjectID != "user-artist" {
		t.Fatalf("expected user access rule, got %+v", rules)
	}

	if err := service.RemoveToolUser(ctx, "guild-1", "admin", "panda.generate_image", "user-artist"); err != nil {
		t.Fatalf("RemoveToolUser: %v", err)
	}
	afterRemove, err := service.ToolUserRoleAccess(ctx, "guild-1", "user-artist", nil)
	if err != nil {
		t.Fatalf("ToolUserRoleAccess after remove: %v", err)
	}
	if len(afterRemove.RestrictedTools) != 0 || len(afterRemove.AllowedTools) != 0 {
		t.Fatalf("expected user tool rule removal, got %+v", afterRemove)
	}

	if _, err := service.DenyToolUser(ctx, "guild-1", "admin", "panda.generate_image", "user-artist"); err != nil {
		t.Fatalf("DenyToolUser: %v", err)
	}
	deniedUser, err := service.ToolUserRoleAccess(ctx, "guild-1", "user-artist", nil)
	if err != nil {
		t.Fatalf("ToolUserRoleAccess denied user: %v", err)
	}
	if len(deniedUser.AllowedTools) != 0 || len(deniedUser.DeniedTools) != 1 || deniedUser.DeniedTools[0] != "panda.generate_image" {
		t.Fatalf("expected direct user deny, got %+v", deniedUser)
	}
	if _, err := service.AddToolUser(ctx, "guild-1", "admin", "panda.generate_image", "user-artist"); err != nil {
		t.Fatalf("AddToolUser after deny: %v", err)
	}
	allowedAgain, err := service.ToolUserRoleAccess(ctx, "guild-1", "user-artist", nil)
	if err != nil {
		t.Fatalf("ToolUserRoleAccess allowed again: %v", err)
	}
	if len(allowedAgain.DeniedTools) != 0 || len(allowedAgain.AllowedTools) != 1 || allowedAgain.AllowedTools[0] != "panda.generate_image" {
		t.Fatalf("expected allow to replace deny, got %+v", allowedAgain)
	}
}

func TestToolAccessRejectsEveryoneRoleForNewGrantsButCanRemoveLegacyRule(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	access := repository.NewAccessRepository(db.DB)
	service := NewService(
		repository.NewGuildConfigRepository(db.DB),
		repository.NewUsageRepository(db.DB),
		repository.NewAuditRepository(db.DB),
		memory.NewService(repository.NewKnowledgeRepository(db.DB)),
		access,
		repository.NewBudgetRepository(db.DB),
		nil,
	)
	if _, err := service.AddToolRole(ctx, "guild-1", "admin", "panda.generate_image", "guild-1"); !errors.Is(err, ErrToolAccessEveryoneRole) {
		t.Fatalf("expected @everyone role grant rejection, got %v", err)
	}
	if _, err := access.AddToolRole(ctx, "guild-1", "panda.generate_image", "guild-1"); err != nil {
		t.Fatalf("seed legacy tool role: %v", err)
	}
	if err := service.RemoveToolRole(ctx, "guild-1", "admin", "panda.generate_image", "guild-1"); err != nil {
		t.Fatalf("RemoveToolRole should clean legacy @everyone rule: %v", err)
	}
	rules, err := service.ListToolAccess(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListToolAccess: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected legacy @everyone rule to be removed, got %+v", rules)
	}
}

func TestClearToolAccessOpensNativeToolGates(t *testing.T) {
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
	)
	if _, err := service.AddRolePermission(ctx, "guild-1", "admin", "role-image", PermissionAssistantImageGeneration); err != nil {
		t.Fatalf("AddRolePermission: %v", err)
	}
	if _, err := service.AddUserPermission(ctx, "guild-1", "admin", "user-image", PermissionAssistantImageGeneration); err != nil {
		t.Fatalf("AddUserPermission: %v", err)
	}
	if _, err := service.AddToolRole(ctx, "guild-1", "admin", "panda.generate_image", "role-image"); err != nil {
		t.Fatalf("AddToolRole: %v", err)
	}
	if _, err := service.AddToolUser(ctx, "guild-1", "admin", "panda.generate_image", "user-image"); err != nil {
		t.Fatalf("AddToolUser: %v", err)
	}

	permissionResult, err := service.ClearPermissionAccess(ctx, "guild-1", "admin", PermissionAssistantImageGeneration)
	if err != nil {
		t.Fatalf("ClearPermissionAccess: %v", err)
	}
	if permissionResult.RemovedRoleRules != 1 || permissionResult.RemovedUserRules != 1 {
		t.Fatalf("unexpected permission clear result: %+v", permissionResult)
	}
	toolResult, err := service.ClearToolAccess(ctx, "guild-1", "admin", "panda.generate_image")
	if err != nil {
		t.Fatalf("ClearToolAccess: %v", err)
	}
	if toolResult.RemovedRoleRules != 1 || toolResult.RemovedUserRules != 1 {
		t.Fatalf("unexpected tool clear result: %+v", toolResult)
	}
	rolePermissions, err := service.ListRolePermissions(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListRolePermissions: %v", err)
	}
	userPermissions, err := service.ListUserPermissions(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListUserPermissions: %v", err)
	}
	toolRules, err := service.ListToolAccess(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListToolAccess: %v", err)
	}
	if len(rolePermissions) != 0 || len(userPermissions) != 0 || len(toolRules) != 0 {
		t.Fatalf("expected access gates to be open, roles=%+v users=%+v tools=%+v", rolePermissions, userPermissions, toolRules)
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
