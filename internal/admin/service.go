package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type Service struct {
	configs      *repository.GuildConfigRepository
	usage        *repository.UsageRepository
	audit        *repository.AuditRepository
	memory       *memory.Service
	access       *repository.AccessRepository
	budgets      *repository.BudgetRepository
	members      *repository.MemberRepository
	models       llm.ModelLister
	defaultModel string
}

const (
	PermissionAssistantUse         = "assistant.use"
	PermissionAssistantUseThreads  = "assistant.use_threads"
	PermissionAssistantAttachments = "assistant.attachments"
	PermissionAssistantMemoryRead  = "assistant.memory.read"
	PermissionAssistantMemoryWrite = "assistant.memory.write"
	PermissionModerationUse        = "moderation.use"
	PermissionAdminConfigRead      = "admin.config.read"
	PermissionAdminConfigWrite     = "admin.config.write"
	PermissionAdminUsageRead       = "admin.usage.read"
	PermissionAdminAuditRead       = "admin.audit.read"
	PermissionAdminMemoryManage    = "admin.memory.manage"
	PermissionOwnerOps             = "owner.ops"

	maxFallbackModels      = 5
	minTemperature         = 0
	maxTemperature         = 2
	minMaxResponseTokens   = 64
	maxMaxResponseTokens   = 4000
	defaultModelToolPolicy = "off"
)

type AssistantAccessRequest struct {
	GuildID      string
	ChannelID    string
	RoleIDs      []string
	IsGuildAdmin bool
	IsOwner      bool
}

type ModelSettings struct {
	DefaultModel         string
	FallbackModels       []string
	FallbackModelsSet    bool
	Temperature          float64
	TemperatureSet       bool
	MaxResponseTokens    int
	MaxResponseTokensSet bool
	ToolPolicy           string
	ToolPolicySet        bool
}

type UsageReport struct {
	Summary   repository.UsageSummary
	Breakdown []repository.UsageBreakdownRow
	Dimension string
}

func NewService(configs *repository.GuildConfigRepository, usage *repository.UsageRepository, audit *repository.AuditRepository, memoryService *memory.Service, access *repository.AccessRepository, budgets *repository.BudgetRepository, models llm.ModelLister, defaultModel string, members ...*repository.MemberRepository) *Service {
	var memberRepo *repository.MemberRepository
	if len(members) > 0 {
		memberRepo = members[0]
	}
	return &Service{
		configs:      configs,
		usage:        usage,
		audit:        audit,
		memory:       memoryService,
		access:       access,
		budgets:      budgets,
		members:      memberRepo,
		models:       models,
		defaultModel: defaultModel,
	}
}

func (s *Service) SetupGuild(ctx context.Context, guildID, actorID string) (store.GuildConfig, error) {
	config, err := s.configs.EnsureDefault(ctx, guildID, s.defaultModel)
	if err != nil {
		return store.GuildConfig{}, err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.setup",
		TargetType: "guild_config",
		TargetID:   guildID,
		Metadata:   metadata(map[string]string{"default_model": config.DefaultModel}),
	})
	return config, nil
}

func (s *Service) SetModel(ctx context.Context, guildID, actorID, model string) (store.GuildConfig, error) {
	return s.ConfigureModel(ctx, guildID, actorID, ModelSettings{DefaultModel: model})
}

func (s *Service) ConfigureModel(ctx context.Context, guildID, actorID string, settings ModelSettings) (store.GuildConfig, error) {
	updates := map[string]any{}
	meta := map[string]string{}

	defaultModel := strings.TrimSpace(settings.DefaultModel)
	if defaultModel != "" {
		if err := s.validateModel(ctx, defaultModel); err != nil {
			return store.GuildConfig{}, err
		}
		updates["default_model"] = defaultModel
		meta["default_model"] = defaultModel
	}

	if settings.FallbackModelsSet {
		models, err := normalizeFallbackModels(settings.FallbackModels)
		if err != nil {
			return store.GuildConfig{}, err
		}
		for _, model := range models {
			if err := s.validateModel(ctx, model); err != nil {
				return store.GuildConfig{}, err
			}
		}
		data, err := json.Marshal(models)
		if err != nil {
			return store.GuildConfig{}, err
		}
		updates["fallback_models"] = string(data)
		meta["fallback_count"] = strconv.Itoa(len(models))
	}

	if settings.TemperatureSet {
		if settings.Temperature < minTemperature || settings.Temperature > maxTemperature {
			return store.GuildConfig{}, fmt.Errorf("temperature must be between %d and %d", minTemperature, maxTemperature)
		}
		updates["temperature"] = settings.Temperature
		meta["temperature"] = strconv.FormatFloat(settings.Temperature, 'f', -1, 64)
	}

	if settings.MaxResponseTokensSet {
		if settings.MaxResponseTokens < minMaxResponseTokens || settings.MaxResponseTokens > maxMaxResponseTokens {
			return store.GuildConfig{}, fmt.Errorf("max response tokens must be between %d and %d", minMaxResponseTokens, maxMaxResponseTokens)
		}
		updates["max_response_tokens"] = settings.MaxResponseTokens
		meta["max_response_tokens"] = strconv.Itoa(settings.MaxResponseTokens)
	}

	if settings.ToolPolicySet {
		policy := strings.ToLower(strings.TrimSpace(settings.ToolPolicy))
		if policy == "" {
			policy = defaultModelToolPolicy
		}
		if !allowedToolPolicy(policy) {
			return store.GuildConfig{}, fmt.Errorf("tool policy must be off, read_only, or admin_only")
		}
		updates["tool_policy"] = policy
		meta["tool_policy"] = policy
	}

	if len(updates) == 0 {
		return store.GuildConfig{}, fmt.Errorf("model setting is required")
	}

	config, err := s.configs.UpdateModelSettings(ctx, guildID, updates)
	if err != nil {
		return store.GuildConfig{}, err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.model.configure",
		TargetType: "guild_config",
		TargetID:   guildID,
		Metadata:   metadata(meta),
	})
	return config, nil
}

func (s *Service) validateModel(ctx context.Context, model string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("model is required")
	}
	if s.models == nil {
		return nil
	}
	ok, err := s.models.ValidateModel(ctx, model)
	if err != nil {
		return fmt.Errorf("validate model: %w", err)
	}
	if !ok {
		return fmt.Errorf("model %q is not available from OpenRouter", model)
	}
	return nil
}

func (s *Service) SetPrompt(ctx context.Context, guildID, actorID, prompt string) (store.GuildConfig, error) {
	config, err := s.configs.UpdatePrompt(ctx, guildID, strings.TrimSpace(prompt))
	if err != nil {
		return store.GuildConfig{}, err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.prompt.set",
		TargetType: "guild_config",
		TargetID:   guildID,
		Metadata:   metadata(map[string]string{"prompt_chars": strconv.Itoa(len(strings.TrimSpace(prompt)))}),
	})
	return config, nil
}

func (s *Service) SetAssistantEnabled(ctx context.Context, guildID, actorID string, enabled bool) (store.GuildConfig, error) {
	config, err := s.configs.SetAssistantEnabled(ctx, guildID, enabled)
	if err != nil {
		return store.GuildConfig{}, err
	}
	action := "admin.assistant.disable"
	if enabled {
		action = "admin.assistant.enable"
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     action,
		TargetType: "guild_config",
		TargetID:   guildID,
	})
	return config, nil
}

func (s *Service) SetMemoryEnabled(ctx context.Context, guildID, actorID string, enabled bool) (store.GuildConfig, error) {
	config, err := s.configs.SetMemoryEnabled(ctx, guildID, enabled)
	if err != nil {
		return store.GuildConfig{}, err
	}
	action := "admin.memory.disable"
	if enabled {
		action = "admin.memory.enable"
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     action,
		TargetType: "guild_config",
		TargetID:   guildID,
	})
	return config, nil
}

func (s *Service) UsageSummary(ctx context.Context, guildID string, window time.Duration) (repository.UsageSummary, error) {
	var since time.Time
	if window > 0 {
		since = time.Now().UTC().Add(-window)
	}
	return s.usage.SummaryByGuild(ctx, guildID, since)
}

func (s *Service) UsageReport(ctx context.Context, guildID string, window time.Duration, dimension string, limit int) (UsageReport, error) {
	dimension = firstNonEmpty(strings.ToLower(strings.TrimSpace(dimension)), "command")
	var since time.Time
	if window > 0 {
		since = time.Now().UTC().Add(-window)
	}
	summary, err := s.usage.SummaryByGuild(ctx, guildID, since)
	if err != nil {
		return UsageReport{}, err
	}
	breakdown, err := s.usage.BreakdownByGuild(ctx, guildID, since, dimension, limit)
	if err != nil {
		return UsageReport{}, err
	}
	return UsageReport{Summary: summary, Breakdown: breakdown, Dimension: dimension}, nil
}

func (s *Service) RecentAudit(ctx context.Context, guildID string, limit int) ([]store.AuditEvent, error) {
	return s.audit.Recent(ctx, guildID, limit)
}

func (s *Service) AddMemoryDocument(ctx context.Context, request memory.AddDocumentRequest) (store.KnowledgeDocument, error) {
	document, err := s.memory.AddDocument(ctx, request)
	if err != nil {
		return store.KnowledgeDocument{}, err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    request.GuildID,
		ActorID:    request.CreatedBy,
		Action:     "admin.memory.add",
		TargetType: "knowledge_document",
		TargetID:   strconv.FormatUint(uint64(document.ID), 10),
		Metadata:   metadata(map[string]string{"title": document.Title}),
	})
	return document, nil
}

func (s *Service) SearchMemory(ctx context.Context, guildID, query string, limit int) ([]repository.KnowledgeSearchResult, error) {
	return s.memory.Search(ctx, guildID, query, limit)
}

func (s *Service) DeleteMemoryDocument(ctx context.Context, guildID, actorID string, documentID uint) error {
	if err := s.memory.DeleteDocument(ctx, guildID, documentID); err != nil {
		return err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.memory.delete",
		TargetType: "knowledge_document",
		TargetID:   strconv.FormatUint(uint64(documentID), 10),
	})
	return nil
}

func (s *Service) ListMemoryDocuments(ctx context.Context, guildID string, limit int) ([]store.KnowledgeDocument, error) {
	return s.memory.ListDocuments(ctx, guildID, limit)
}

func (s *Service) MemoryEnabled(ctx context.Context, guildID string) (bool, error) {
	config, ok, err := s.configs.Get(ctx, guildID)
	if err != nil || !ok {
		return false, err
	}
	return config.MemoryEnabled, nil
}

func (s *Service) MemoryConsent(ctx context.Context, guildID, userID string) (bool, error) {
	if s.members == nil {
		return false, fmt.Errorf("member repository is not configured")
	}
	return s.members.MemoryConsent(ctx, guildID, userID)
}

func (s *Service) SetMemoryConsent(ctx context.Context, guildID, userID string, consent bool) (store.GuildMember, error) {
	if s.members == nil {
		return store.GuildMember{}, fmt.Errorf("member repository is not configured")
	}
	member, err := s.members.SetMemoryConsent(ctx, guildID, userID, consent)
	if err != nil {
		return store.GuildMember{}, err
	}
	action := "member.memory_consent.disable"
	if consent {
		action = "member.memory_consent.enable"
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    userID,
		Action:     action,
		TargetType: "guild_member",
		TargetID:   userID,
	})
	return member, nil
}

func (s *Service) AddRolePermission(ctx context.Context, guildID, actorID, roleID, permission string) (store.GuildRole, error) {
	permission = firstNonEmpty(strings.TrimSpace(permission), PermissionAssistantUse)
	if !allowedPermissionName(permission) {
		return store.GuildRole{}, fmt.Errorf("unsupported permission %q", permission)
	}
	role, err := s.access.AddRolePermission(ctx, guildID, strings.TrimSpace(roleID), permission)
	if err != nil {
		return store.GuildRole{}, err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.roles.add",
		TargetType: "guild_role",
		TargetID:   role.RoleID,
		Metadata:   metadata(map[string]string{"permission": permission}),
	})
	return role, nil
}

func (s *Service) RemoveRolePermission(ctx context.Context, guildID, actorID, roleID, permission string) error {
	permission = firstNonEmpty(strings.TrimSpace(permission), PermissionAssistantUse)
	if !allowedPermissionName(permission) {
		return fmt.Errorf("unsupported permission %q", permission)
	}
	if err := s.access.RemoveRolePermission(ctx, guildID, strings.TrimSpace(roleID), permission); err != nil {
		return err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.roles.remove",
		TargetType: "guild_role",
		TargetID:   roleID,
		Metadata:   metadata(map[string]string{"permission": permission}),
	})
	return nil
}

func (s *Service) ListRolePermissions(ctx context.Context, guildID string) ([]store.GuildRole, error) {
	return s.access.ListRolePermissions(ctx, guildID)
}

func (s *Service) SetChannelRule(ctx context.Context, guildID, actorID, channelID, rule string) (store.GuildChannelRule, error) {
	rule = strings.ToLower(strings.TrimSpace(rule))
	if rule != "allow" && rule != "deny" {
		return store.GuildChannelRule{}, fmt.Errorf("channel rule must be allow or deny")
	}
	channelRule, err := s.access.SetChannelRule(ctx, guildID, strings.TrimSpace(channelID), rule)
	if err != nil {
		return store.GuildChannelRule{}, err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.channels.set",
		TargetType: "channel",
		TargetID:   channelID,
		Metadata:   metadata(map[string]string{"rule": rule}),
	})
	return channelRule, nil
}

func (s *Service) RemoveChannelRule(ctx context.Context, guildID, actorID, channelID string) error {
	if err := s.access.RemoveChannelRule(ctx, guildID, strings.TrimSpace(channelID)); err != nil {
		return err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.channels.remove",
		TargetType: "channel",
		TargetID:   channelID,
	})
	return nil
}

func (s *Service) ListChannelRules(ctx context.Context, guildID string) ([]store.GuildChannelRule, error) {
	return s.access.ListChannelRules(ctx, guildID)
}

func (s *Service) SetBudgetLimit(ctx context.Context, guildID, actorID string, limit store.BudgetLimit) (store.BudgetLimit, error) {
	if limit.Scope != repository.BudgetScopeGlobal {
		limit.GuildID = guildID
	}
	if limit.Scope == repository.BudgetScopeGuild && limit.SubjectID == "" {
		limit.SubjectID = guildID
	}
	saved, err := s.budgets.SetLimit(ctx, limit)
	if err != nil {
		return store.BudgetLimit{}, err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.limits.set",
		TargetType: "budget_limit",
		TargetID:   saved.Scope + ":" + saved.SubjectID,
		Metadata: metadata(map[string]string{
			"limit":          strconv.Itoa(saved.Limit),
			"window_seconds": strconv.Itoa(saved.WindowSeconds),
		}),
	})
	return saved, nil
}

func (s *Service) RemoveBudgetLimit(ctx context.Context, guildID, actorID, scope, subjectID string) error {
	if scope == repository.BudgetScopeGuild && subjectID == "" {
		subjectID = guildID
	}
	if err := s.budgets.RemoveLimit(ctx, guildID, scope, subjectID); err != nil {
		return err
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "admin.limits.remove",
		TargetType: "budget_limit",
		TargetID:   scope + ":" + subjectID,
	})
	return nil
}

func (s *Service) ListBudgetLimits(ctx context.Context, guildID string) ([]store.BudgetLimit, error) {
	return s.budgets.ListLimits(ctx, guildID)
}

func (s *Service) ConsumeBudget(ctx context.Context, request repository.BudgetCheckRequest) (repository.BudgetDenial, bool, error) {
	return s.budgets.CheckAndConsume(ctx, request)
}

func (s *Service) CanUseAssistant(ctx context.Context, request AssistantAccessRequest) (bool, error) {
	if request.IsOwner || request.IsGuildAdmin || request.GuildID == "" {
		return true, nil
	}

	channelRule, hasRule, err := s.access.ChannelRule(ctx, request.GuildID, request.ChannelID)
	if err != nil {
		return false, err
	}
	if hasRule && channelRule.Rule == "deny" {
		return false, nil
	}
	hasAllowRules, err := s.access.HasChannelAllowRules(ctx, request.GuildID)
	if err != nil {
		return false, err
	}
	if hasAllowRules && (!hasRule || channelRule.Rule != "allow") {
		return false, nil
	}

	return s.canUsePermission(ctx, request.GuildID, request.RoleIDs, PermissionAssistantUse, true)
}

func (s *Service) CanUseModeration(ctx context.Context, request AssistantAccessRequest) (bool, error) {
	if request.IsOwner || request.IsGuildAdmin {
		return true, nil
	}
	if request.GuildID == "" {
		return false, nil
	}
	return s.canUsePermission(ctx, request.GuildID, request.RoleIDs, PermissionModerationUse, false)
}

func (s *Service) CanUseThreads(ctx context.Context, request AssistantAccessRequest) (bool, error) {
	return s.canUseOptionalAssistantPermission(ctx, request, PermissionAssistantUseThreads)
}

func (s *Service) CanUseAttachments(ctx context.Context, request AssistantAccessRequest) (bool, error) {
	return s.canUseOptionalAssistantPermission(ctx, request, PermissionAssistantAttachments)
}

func (s *Service) CanReadMemory(ctx context.Context, request AssistantAccessRequest) (bool, error) {
	return s.canUseOptionalAssistantPermission(ctx, request, PermissionAssistantMemoryRead)
}

func (s *Service) RecordModerationAudit(ctx context.Context, guildID, actorID, action, targetID string) {
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     action,
		TargetType: "moderation_context",
		TargetID:   targetID,
	})
}

func (s *Service) RecordSensitiveReadAudit(ctx context.Context, guildID, actorID, targetType, targetID string, values map[string]string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     "context.read",
		TargetType: targetType,
		TargetID:   targetID,
		Metadata:   metadata(values),
	})
}

func (s *Service) canUseOptionalAssistantPermission(ctx context.Context, request AssistantAccessRequest, permission string) (bool, error) {
	if request.IsOwner || request.IsGuildAdmin || request.GuildID == "" {
		return true, nil
	}
	return s.canUsePermission(ctx, request.GuildID, request.RoleIDs, permission, true)
}

func (s *Service) canUsePermission(ctx context.Context, guildID string, roleIDs []string, permission string, allowWhenUnmapped bool) (bool, error) {
	hasMappings, err := s.access.HasPermissionMappings(ctx, guildID, permission)
	if err != nil || !hasMappings {
		return allowWhenUnmapped && !hasMappings, err
	}
	return s.access.AnyRoleHasPermission(ctx, guildID, roleIDs, permission)
}

func normalizeFallbackModels(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	models := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			model := strings.TrimSpace(part)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
			if len(models) > maxFallbackModels {
				return nil, fmt.Errorf("at most %d fallback models are supported", maxFallbackModels)
			}
		}
	}
	return models, nil
}

func allowedToolPolicy(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "off", "read_only", "admin_only":
		return true
	default:
		return false
	}
}

func allowedPermissionName(permission string) bool {
	return IsPermissionNameAllowed(permission)
}

func IsPermissionNameAllowed(permission string) bool {
	switch strings.TrimSpace(permission) {
	case PermissionAssistantUse,
		PermissionAssistantUseThreads,
		PermissionAssistantAttachments,
		PermissionAssistantMemoryRead,
		PermissionAssistantMemoryWrite,
		PermissionModerationUse,
		PermissionAdminConfigRead,
		PermissionAdminConfigWrite,
		PermissionAdminUsageRead,
		PermissionAdminAuditRead,
		PermissionAdminMemoryManage,
		PermissionOwnerOps:
		return true
	default:
		return false
	}
}

func metadata(values map[string]string) string {
	data, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
