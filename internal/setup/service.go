package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type AdminApplier interface {
	SetPrompt(ctx context.Context, guildID, actorID, prompt string) (store.GuildConfig, error)
	ApplyRoleProfile(ctx context.Context, guildID, actorID, roleID, profile string) ([]store.GuildRole, error)
	SetChannelRule(ctx context.Context, guildID, actorID, channelID, rule string) (store.GuildChannelRule, error)
	AddToolRole(ctx context.Context, guildID, actorID, toolName, roleID string) (store.GuildToolRole, error)
	DenyToolRole(ctx context.Context, guildID, actorID, toolName, roleID string) (store.GuildToolRole, error)
	SetBudgetLimit(ctx context.Context, guildID, actorID string, limit store.BudgetLimit) (store.BudgetLimit, error)
}

type FeatureApplier interface {
	SetGuildFeatures(ctx context.Context, guildID string, featureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error
}

type JobQueue interface {
	Enqueue(ctx context.Context, job store.Job) (store.Job, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, event store.AuditEvent) error
}

type Service struct {
	repo         *repository.SetupRepository
	adapter      DiscordAdapter
	admin        AdminApplier
	features     FeatureApplier
	jobs         JobQueue
	audit        AuditRecorder
	planner      Planner
	setupLimits  *ratelimit.Limiter
	ticketLimits *ratelimit.Limiter
	now          func() time.Time
}

func NewService(repo *repository.SetupRepository, adapter DiscordAdapter) *Service {
	return &Service{
		repo:         repo,
		adapter:      adapter,
		planner:      NewPlanner(),
		setupLimits:  ratelimit.New(3, time.Hour),
		ticketLimits: ratelimit.New(5, 10*time.Minute),
		now:          time.Now,
	}
}

func (s *Service) WithDiscordAdapter(adapter DiscordAdapter) *Service {
	s.adapter = adapter
	return s
}

func (s *Service) WithAdminApplier(admin AdminApplier) *Service {
	s.admin = admin
	return s
}

func (s *Service) WithFeatureApplier(features FeatureApplier) *Service {
	s.features = features
	return s
}

func (s *Service) WithJobQueue(jobs JobQueue) *Service {
	s.jobs = jobs
	return s
}

func (s *Service) WithAuditRecorder(audit AuditRecorder) *Service {
	s.audit = audit
	return s
}

func (s *Service) Catalog(ctx context.Context) ([]Template, error) {
	if err := s.SyncBuiltInTemplates(ctx); err != nil {
		return nil, err
	}
	templates := BuiltInTemplates()
	return templates, nil
}

func (s *Service) SyncBuiltInTemplates(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	for _, template := range BuiltInTemplates() {
		if err := s.planner.ValidateTemplate(template); err != nil {
			return err
		}
		templateJSON, err := json.Marshal(template)
		if err != nil {
			return err
		}
		defaultVariables, err := json.Marshal(template.DefaultVariables)
		if err != nil {
			return err
		}
		if _, err := s.repo.UpsertTemplate(ctx, store.SetupTemplate{
			ID:               template.ID,
			SchemaVersion:    template.SchemaVersion,
			TemplateVersion:  template.TemplateVersion,
			Name:             template.Name,
			Description:      template.Description,
			ReleaseState:     template.ReleaseState,
			DefaultVariables: string(defaultVariables),
			TemplateJSON:     string(templateJSON),
			BuiltIn:          true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Preview(ctx context.Context, request SetupRequest) (store.GuildSetupProject, Preview, error) {
	if s == nil || s.repo == nil {
		return store.GuildSetupProject{}, Preview{}, errors.New("setup service is not configured")
	}
	if !allowSetupActor(request) {
		return store.GuildSetupProject{}, Preview{}, errors.New("setup requires a guild admin or Panda admin")
	}
	if ok, retry := s.setupLimits.Allow(strings.TrimSpace(request.GuildID)); !ok {
		return store.GuildSetupProject{}, Preview{}, fmt.Errorf("setup rate limit exceeded; retry in %s", retry.Round(time.Second))
	}
	if err := s.SyncBuiltInTemplates(ctx); err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	template, err := s.template(ctx, request.TemplateID)
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	snapshot := GuildSnapshot{}
	if s.adapter != nil && strings.TrimSpace(request.GuildID) != "" {
		snapshot, err = s.adapter.Snapshot(ctx, request.GuildID)
		if err != nil {
			return store.GuildSetupProject{}, Preview{}, err
		}
	}
	resources, err := s.repo.ListResources(ctx, request.GuildID)
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	preview, variables, err := s.planner.Plan(template, request.Variables, snapshot, resources)
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	projectID, err := randomID("sp")
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	preview.ProjectID = projectID
	variablesJSON, _ := json.Marshal(variables)
	previewJSON, _ := json.Marshal(preview)
	planJSON, _ := json.Marshal(preview.Plan)
	status := ProjectStatusPreviewed
	project, err := s.repo.CreateProject(ctx, store.GuildSetupProject{
		ID:                  projectID,
		GuildID:             strings.TrimSpace(request.GuildID),
		TemplateID:          template.ID,
		TemplateVersion:     template.TemplateVersion,
		SchemaVersion:       template.SchemaVersion,
		VariablesJSON:       string(variablesJSON),
		PreviewJSON:         string(previewJSON),
		ApplyPlanJSON:       string(planJSON),
		Status:              status,
		ActorID:             strings.TrimSpace(request.ActorID),
		SourceInstallIntent: strings.TrimSpace(request.SourceInstallIntent),
	})
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	s.recordAudit(ctx, request.GuildID, request.ActorID, "setup.preview_created", "setup_project", project.ID, map[string]any{
		"template_id": template.ID,
		"blocked":     preview.Blocked,
		"summary":     preview.Summary,
	})
	return project, preview, nil
}

func (s *Service) Confirm(ctx context.Context, projectID, actorID string, enqueue bool) (store.GuildSetupProject, error) {
	project, ok, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return store.GuildSetupProject{}, err
	}
	if !ok {
		return store.GuildSetupProject{}, repository.ErrNotFound
	}
	if actor := strings.TrimSpace(actorID); actor != "" && project.ActorID != "" && actor != project.ActorID {
		return store.GuildSetupProject{}, errors.New("setup confirmation must be made by the previewing admin")
	}
	var preview Preview
	if err := json.Unmarshal([]byte(project.PreviewJSON), &preview); err == nil && preview.Blocked {
		return store.GuildSetupProject{}, ErrPreviewBlocked
	}
	now := s.now().UTC()
	status := ProjectStatusConfirmed
	if enqueue && s.jobs != nil {
		status = ProjectStatusQueued
	}
	confirmed, err := s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"status":       status,
		"confirmed_at": now,
	})
	if err != nil {
		return store.GuildSetupProject{}, err
	}
	if enqueue && s.jobs != nil {
		payload, _ := json.Marshal(map[string]string{"project_id": project.ID})
		if _, err := s.jobs.Enqueue(ctx, store.Job{
			Kind:        JobKindApplySetup,
			Status:      "queued",
			GuildID:     project.GuildID,
			Payload:     string(payload),
			MaxAttempts: 2,
			RunAfter:    now,
		}); err != nil {
			return store.GuildSetupProject{}, err
		}
	}
	s.recordAudit(ctx, project.GuildID, firstNonEmpty(actorID, project.ActorID), "setup.confirmed", "setup_project", project.ID, map[string]any{"queued": enqueue})
	return confirmed, nil
}

func (s *Service) HandleApplyJob(ctx context.Context, job store.Job) error {
	var payload struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.ProjectID) == "" {
		return errors.New("setup apply job missing project_id")
	}
	_, err := s.ApplyProject(ctx, payload.ProjectID)
	return err
}

func (s *Service) ApplyProject(ctx context.Context, projectID string) (ApplyResult, error) {
	if s == nil || s.repo == nil || s.adapter == nil {
		return ApplyResult{}, errors.New("setup apply requires repository and Discord adapter")
	}
	project, ok, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return ApplyResult{}, err
	}
	if !ok {
		return ApplyResult{}, repository.ErrNotFound
	}
	var steps []PlanStep
	if err := json.Unmarshal([]byte(project.ApplyPlanJSON), &steps); err != nil {
		return ApplyResult{}, err
	}
	startedAt := s.now().UTC()
	project, err = s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"status":     ProjectStatusApplying,
		"started_at": startedAt,
	})
	if err != nil {
		return ApplyResult{}, err
	}
	if s.features != nil {
		if template, templateErr := s.template(ctx, project.TemplateID); templateErr == nil && len(template.FeatureIDs) > 0 {
			if err := s.features.SetGuildFeatures(ctx, project.GuildID, template.FeatureIDs, project.SourceInstallIntent, project.ActorID, startedAt); err != nil {
				recoveryJSON, _ := json.Marshal(map[string]any{
					"message":           "The setup stopped before Discord resources were changed because Panda feature enablement failed. Fix the feature store issue, then confirm or rerun the project.",
					"resume_project_id": project.ID,
				})
				_, _ = s.repo.UpdateProject(ctx, project.ID, map[string]any{
					"status":        ProjectStatusFailed,
					"last_error":    err.Error(),
					"recovery_json": string(recoveryJSON),
					"finished_at":   s.now().UTC(),
				})
				s.recordAudit(ctx, project.GuildID, project.ActorID, "setup.apply_failed", "setup_project", project.ID, map[string]any{"phase": "features", "error": err.Error()})
				return ApplyResult{}, err
			}
		}
	}
	resources, err := s.repo.ListResources(ctx, project.GuildID)
	if err != nil {
		return ApplyResult{}, err
	}
	resourceIDs := map[string]string{}
	for _, resource := range resources {
		resourceIDs[resource.ManagedAlias] = resource.ObjectID
	}
	applied := []AppliedResource{}
	failures := []map[string]string{}
	for index, step := range steps {
		progress := ApplyProgress{Total: len(steps), Completed: index, CurrentStep: step.ID, UpdatedAt: s.now().UTC()}
		progressJSON, _ := json.Marshal(progress)
		_, _ = s.repo.UpdateProject(ctx, project.ID, map[string]any{
			"current_step":  step.ID,
			"progress_json": string(progressJSON),
		})
		resource, err := s.applyStep(ctx, project, step, resourceIDs)
		if err != nil {
			failures = append(failures, map[string]string{"step": step.ID, "error": err.Error()})
			failedJSON, _ := json.Marshal(failures)
			recoveryJSON, _ := json.Marshal(map[string]any{
				"message":           "The setup stopped after a hard failure. Fix the reported Discord permission or hierarchy issue, then rerun this setup project to resume from stored aliases.",
				"resume_project_id": project.ID,
			})
			_, _ = s.repo.UpdateProject(ctx, project.ID, map[string]any{
				"status":            ProjectStatusFailed,
				"last_error":        err.Error(),
				"failed_steps_json": string(failedJSON),
				"recovery_json":     string(recoveryJSON),
				"finished_at":       s.now().UTC(),
			})
			s.recordAudit(ctx, project.GuildID, project.ActorID, "setup.apply_failed", "setup_project", project.ID, map[string]any{"step": step.ID, "error": err.Error()})
			return ApplyResult{ProjectID: project.ID, Status: ProjectStatusFailed, Summary: summarizePlan(steps), Resources: applied, Error: err.Error()}, err
		}
		if resource.ID != "" {
			applied = append(applied, resource)
			resourceIDs[step.Alias] = resource.ID
		}
	}
	progress := ApplyProgress{Total: len(steps), Completed: len(steps), UpdatedAt: s.now().UTC()}
	progressJSON, _ := json.Marshal(progress)
	_, err = s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"status":        ProjectStatusSucceeded,
		"current_step":  "",
		"progress_json": string(progressJSON),
		"last_error":    "",
		"finished_at":   s.now().UTC(),
	})
	if err != nil {
		return ApplyResult{}, err
	}
	s.recordAudit(ctx, project.GuildID, project.ActorID, "setup.apply_succeeded", "setup_project", project.ID, map[string]any{"summary": summarizePlan(steps)})
	return ApplyResult{ProjectID: project.ID, Status: ProjectStatusSucceeded, Summary: summarizePlan(steps), Resources: applied}, nil
}

func (s *Service) RollbackProject(ctx context.Context, projectID, actorID string) (ApplyResult, error) {
	if s == nil || s.repo == nil || s.adapter == nil {
		return ApplyResult{}, errors.New("setup rollback requires repository and Discord adapter")
	}
	project, ok, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return ApplyResult{}, err
	}
	if !ok {
		return ApplyResult{}, repository.ErrNotFound
	}
	steps, err := projectPlan(project)
	if err != nil {
		return ApplyResult{}, err
	}
	resources, err := s.repo.ListResources(ctx, project.GuildID)
	if err != nil {
		return ApplyResult{}, err
	}
	resourcesByAlias := map[string]store.GuildSetupResource{}
	for _, resource := range resources {
		if resource.ProjectID == project.ID {
			resourcesByAlias[resource.ManagedAlias] = resource
		}
	}
	if len(resourcesByAlias) == 0 {
		now := s.now().UTC()
		rolledBack, err := s.repo.UpdateProject(ctx, project.ID, map[string]any{
			"status":        ProjectStatusRolledBack,
			"recovery_json": mustJSON(map[string]any{"message": "Rollback completed. No managed resources were still attached to this setup project."}),
			"finished_at":   now,
		})
		if err != nil {
			return ApplyResult{}, err
		}
		s.recordAudit(ctx, rolledBack.GuildID, firstNonEmpty(actorID, rolledBack.ActorID), "setup.rolled_back", "setup_project", rolledBack.ID, map[string]any{"resources": 0})
		return ApplyResult{ProjectID: rolledBack.ID, Status: ProjectStatusRolledBack, Summary: summarizePlan(steps)}, nil
	}
	applied := []AppliedResource{}
	failures := []map[string]string{}
	warnings := []string{}
	for index := len(steps) - 1; index >= 0; index-- {
		step := steps[index]
		resource, ok := resourcesByAlias[step.Alias]
		if !ok {
			continue
		}
		if step.Action != PlanActionCreate {
			warnings = append(warnings, fmt.Sprintf("Kept %s %q because the setup plan reused or updated an existing resource.", step.ResourceType, step.Alias))
			applied = append(applied, AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: resource.ObjectID, Name: resource.DisplayName, Action: PlanActionSkip})
			continue
		}
		item, err := s.rollbackResource(ctx, project, step, resource)
		if err != nil {
			failures = append(failures, map[string]string{"alias": step.Alias, "type": step.ResourceType, "error": err.Error()})
			continue
		}
		if item.Action == PlanActionSkip {
			warnings = append(warnings, fmt.Sprintf("Kept %s %q because it needs manual cleanup.", step.ResourceType, step.Alias))
		}
		applied = append(applied, item)
	}
	now := s.now().UTC()
	if len(failures) > 0 {
		failedJSON, _ := json.Marshal(failures)
		recoveryJSON, _ := json.Marshal(map[string]any{
			"message":  "Rollback attempted cleanup but some resources could not be removed. Fix the reported Discord permission or hierarchy issue, then run rollback again.",
			"failures": failures,
			"warnings": warnings,
		})
		_, _ = s.repo.UpdateProject(ctx, project.ID, map[string]any{
			"status":            ProjectStatusFailed,
			"last_error":        "setup rollback failed",
			"failed_steps_json": string(failedJSON),
			"recovery_json":     string(recoveryJSON),
			"finished_at":       now,
		})
		s.recordAudit(ctx, project.GuildID, firstNonEmpty(actorID, project.ActorID), "setup.rollback_failed", "setup_project", project.ID, map[string]any{"failures": failures})
		return ApplyResult{ProjectID: project.ID, Status: ProjectStatusFailed, Summary: summarizePlan(steps), Resources: applied, Warnings: warnings, Error: "setup rollback failed"}, errors.New("setup rollback failed")
	}
	recoveryJSON, _ := json.Marshal(map[string]any{
		"message":  "Rollback completed. Resources Panda safely owned were removed or disabled.",
		"warnings": warnings,
	})
	rolledBack, err := s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"status":        ProjectStatusRolledBack,
		"last_error":    "",
		"recovery_json": string(recoveryJSON),
		"finished_at":   now,
	})
	if err != nil {
		return ApplyResult{}, err
	}
	s.recordAudit(ctx, rolledBack.GuildID, firstNonEmpty(actorID, rolledBack.ActorID), "setup.rolled_back", "setup_project", rolledBack.ID, map[string]any{"resources": len(applied), "warnings": warnings})
	return ApplyResult{ProjectID: rolledBack.ID, Status: ProjectStatusRolledBack, Summary: summarizePlan(steps), Resources: applied, Warnings: warnings}, nil
}

func (s *Service) rollbackResource(ctx context.Context, project store.GuildSetupProject, step PlanStep, resource store.GuildSetupResource) (AppliedResource, error) {
	reason := "Panda server setup rollback " + project.ID
	item := AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: resource.ObjectID, Name: firstNonEmpty(resource.DisplayName, step.Name), Action: PlanActionDelete}
	switch step.ResourceType {
	case ResourceTypeRole:
		if err := s.adapter.DeleteRole(ctx, project.GuildID, resource.ObjectID, reason); err != nil {
			return AppliedResource{}, err
		}
	case ResourceTypeCategory, ResourceTypeChannel:
		if err := s.adapter.DeleteChannel(ctx, resource.ObjectID, reason); err != nil {
			return AppliedResource{}, err
		}
	case ResourceTypeTicketPanel:
		panel, ok, err := s.repo.GetTicketPanel(ctx, resource.ObjectID)
		if err != nil {
			return AppliedResource{}, err
		}
		if ok {
			panel.Enabled = false
			if _, err := s.repo.UpsertTicketPanel(ctx, panel); err != nil {
				return AppliedResource{}, err
			}
		}
		item.Action = "disable"
	case ResourceTypeOnboardingFlow:
		flow, ok, err := s.repo.GetOnboardingFlow(ctx, resource.ObjectID)
		if err != nil {
			return AppliedResource{}, err
		}
		if ok {
			flow.Enabled = false
			flow.Paused = true
			if _, err := s.repo.UpsertOnboardingFlow(ctx, flow); err != nil {
				return AppliedResource{}, err
			}
		}
		item.Action = "disable"
	default:
		item.Action = PlanActionSkip
		return item, nil
	}
	if err := s.repo.DeleteResource(ctx, project.GuildID, step.Alias); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return AppliedResource{}, err
	}
	return item, nil
}

func (s *Service) applyStep(ctx context.Context, project store.GuildSetupProject, step PlanStep, resourceIDs map[string]string) (AppliedResource, error) {
	reason := "Panda server setup " + project.ID
	if step.Action == PlanActionSkip {
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: step.ObjectID, Name: step.Name, Action: step.Action}, nil
	}
	switch step.ResourceType {
	case ResourceTypeRole:
		var role RoleTemplate
		if err := decodeStepPayload(step, "role", &role); err != nil {
			return AppliedResource{}, err
		}
		color, err := parseHexColor(role.Color)
		if err != nil {
			return AppliedResource{}, err
		}
		request := RoleApplyRequest{
			GuildID:     project.GuildID,
			RoleID:      firstNonEmpty(step.ObjectID, resourceIDs[step.Alias]),
			Name:        role.Name,
			Color:       color,
			Hoist:       role.Hoist,
			Mentionable: role.Mentionable,
			Permissions: role.Permissions,
			Position:    role.Position,
			Reason:      reason,
		}
		resource, err := s.applyRole(ctx, step.Action, request)
		if err != nil {
			return AppliedResource{}, err
		}
		if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, resource.ID, resource.Name)); err != nil {
			return AppliedResource{}, err
		}
		if s.admin != nil && strings.TrimSpace(role.Profile) != "" {
			if _, err := s.admin.ApplyRoleProfile(ctx, project.GuildID, project.ActorID, resource.ID, role.Profile); err != nil {
				return AppliedResource{}, err
			}
		}
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: resource.ID, Name: resource.Name, Action: step.Action}, nil
	case ResourceTypeCategory:
		var category CategoryTemplate
		if err := decodeStepPayload(step, "category", &category); err != nil {
			return AppliedResource{}, err
		}
		resource, err := s.applyChannel(ctx, step.Action, ChannelApplyRequest{
			GuildID:    project.GuildID,
			ChannelID:  firstNonEmpty(step.ObjectID, resourceIDs[step.Alias]),
			Type:       "category",
			Name:       category.Name,
			Position:   category.Position,
			Overwrites: resolveOverwrites(project.GuildID, category.Overwrites, resourceIDs),
			Reason:     reason,
		})
		if err != nil {
			return AppliedResource{}, err
		}
		if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, resource.ID, resource.Name)); err != nil {
			return AppliedResource{}, err
		}
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: resource.ID, Name: resource.Name, Action: step.Action}, nil
	case ResourceTypeChannel:
		var channel ChannelTemplate
		if err := decodeStepPayload(step, "channel", &channel); err != nil {
			return AppliedResource{}, err
		}
		resource, err := s.applyChannel(ctx, step.Action, ChannelApplyRequest{
			GuildID:         project.GuildID,
			ChannelID:       firstNonEmpty(step.ObjectID, resourceIDs[step.Alias]),
			Type:            normalizeChannelType(channel.Type),
			Name:            channel.Name,
			Topic:           channel.Topic,
			ParentID:        resourceIDs[channel.ParentAlias],
			Position:        channel.Position,
			NSFW:            channel.NSFW,
			SlowmodeSeconds: channel.SlowmodeSeconds,
			Bitrate:         channel.Bitrate,
			UserLimit:       channel.UserLimit,
			Overwrites:      resolveOverwrites(project.GuildID, channel.Overwrites, resourceIDs),
			Reason:          reason,
		})
		if err != nil {
			return AppliedResource{}, err
		}
		if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, resource.ID, resource.Name)); err != nil {
			return AppliedResource{}, err
		}
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: resource.ID, Name: resource.Name, Action: step.Action}, nil
	case ResourceTypeStarterMessage:
		var payload struct {
			ChannelAlias string         `json:"channel_alias"`
			Message      StarterMessage `json:"message"`
		}
		if err := decodeStepPayload(step, "", &payload); err != nil {
			return AppliedResource{}, err
		}
		if existingID := firstNonEmpty(step.ObjectID, resourceIDs[step.Alias]); existingID != "" {
			return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: existingID, Name: step.Name, Action: PlanActionReuse}, nil
		}
		channelID := resourceIDs[payload.ChannelAlias]
		if channelID == "" {
			return AppliedResource{}, fmt.Errorf("starter message channel alias %s is unresolved", payload.ChannelAlias)
		}
		resource, err := s.adapter.SendMessage(ctx, MessageApplyRequest{ChannelID: channelID, Content: payload.Message.Content, Reason: reason})
		if err != nil {
			return AppliedResource{}, err
		}
		if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, resource.ID, resource.Name)); err != nil {
			return AppliedResource{}, err
		}
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: resource.ID, Name: step.Name, Action: step.Action}, nil
	case ResourceTypePandaConfig:
		var config PandaConfigTemplate
		if err := decodeStepPayload(step, "panda", &config); err != nil {
			return AppliedResource{}, err
		}
		if err := s.applyPandaConfig(ctx, project, config, resourceIDs); err != nil {
			return AppliedResource{}, err
		}
		if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, project.GuildID, "Panda config")); err != nil {
			return AppliedResource{}, err
		}
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: project.GuildID, Name: step.Name, Action: step.Action}, nil
	case ResourceTypeTicketPanel:
		var panel TicketPanelTemplate
		if err := decodeStepPayload(step, "ticket_panel", &panel); err != nil {
			return AppliedResource{}, err
		}
		stored, err := s.applyTicketPanel(ctx, project, step, panel, resourceIDs)
		if err != nil {
			return AppliedResource{}, err
		}
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: stored.ID, Name: stored.Title, Action: step.Action}, nil
	case ResourceTypeOnboardingFlow:
		var flow OnboardingFlowTemplate
		if err := decodeStepPayload(step, "onboarding_flow", &flow); err != nil {
			return AppliedResource{}, err
		}
		stored, err := s.applyOnboardingFlow(ctx, project, step, flow, resourceIDs)
		if err != nil {
			return AppliedResource{}, err
		}
		return AppliedResource{Alias: step.Alias, Type: step.ResourceType, ID: stored.ID, Name: stored.VerificationMode, Action: step.Action}, nil
	default:
		return AppliedResource{}, fmt.Errorf("unsupported setup step type %s", step.ResourceType)
	}
}

func (s *Service) applyRole(ctx context.Context, action string, request RoleApplyRequest) (DiscordResource, error) {
	if strings.TrimSpace(request.RoleID) != "" {
		return s.adapter.UpdateRole(ctx, request)
	}
	return s.adapter.CreateRole(ctx, request)
}

func (s *Service) applyChannel(ctx context.Context, action string, request ChannelApplyRequest) (DiscordResource, error) {
	if strings.TrimSpace(request.ChannelID) != "" {
		return s.adapter.UpdateChannel(ctx, request)
	}
	return s.adapter.CreateChannel(ctx, request)
}

func (s *Service) applyPandaConfig(ctx context.Context, project store.GuildSetupProject, config PandaConfigTemplate, resourceIDs map[string]string) error {
	if s.admin == nil {
		return nil
	}
	if strings.TrimSpace(config.PromptOverlay) != "" {
		if _, err := s.admin.SetPrompt(ctx, project.GuildID, project.ActorID, config.PromptOverlay); err != nil {
			return err
		}
	}
	for alias, profile := range config.RoleProfiles {
		roleID := resourceIDs[alias]
		if roleID == "" || strings.TrimSpace(profile) == "" {
			continue
		}
		if _, err := s.admin.ApplyRoleProfile(ctx, project.GuildID, project.ActorID, roleID, profile); err != nil {
			return err
		}
	}
	for alias, rule := range config.ChannelRules {
		channelID := resourceIDs[alias]
		if channelID == "" || strings.TrimSpace(rule) == "" {
			continue
		}
		if _, err := s.admin.SetChannelRule(ctx, project.GuildID, project.ActorID, channelID, rule); err != nil {
			return err
		}
	}
	for _, access := range config.ToolAccess {
		roleID := resourceIDs[access.RoleAlias]
		if roleID == "" || strings.TrimSpace(access.ToolName) == "" {
			continue
		}
		if strings.EqualFold(access.Rule, "deny") {
			if _, err := s.admin.DenyToolRole(ctx, project.GuildID, project.ActorID, access.ToolName, roleID); err != nil {
				return err
			}
			continue
		}
		if _, err := s.admin.AddToolRole(ctx, project.GuildID, project.ActorID, access.ToolName, roleID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyTicketPanel(ctx context.Context, project store.GuildSetupProject, step PlanStep, panel TicketPanelTemplate, resourceIDs map[string]string) (store.TicketPanel, error) {
	panelID := stablePanelID(project.GuildID, panel.Alias)
	existing, existed, err := s.repo.GetTicketPanel(ctx, panelID)
	if err != nil {
		return store.TicketPanel{}, err
	}
	departments := make([]TicketDepartment, 0, len(panel.Departments))
	for _, department := range panel.Departments {
		staffRoles := resolveAliases(department.StaffRoleAliases, resourceIDs)
		if len(staffRoles) == 0 {
			staffRoles = resolveAliases(panel.StaffRoleAliases, resourceIDs)
		}
		departments = append(departments, TicketDepartment{
			ID:              department.ID,
			Label:           department.Label,
			Description:     department.Description,
			StaffRoleIDs:    staffRoles,
			InitialPriority: firstNonEmpty(department.InitialPriority, "normal"),
		})
	}
	departmentsJSON, _ := json.Marshal(departments)
	staffRoleIDs := resolveAliases(panel.StaffRoleAliases, resourceIDs)
	staffRoleIDsJSON, _ := json.Marshal(staffRoleIDs)
	stored, err := s.repo.UpsertTicketPanel(ctx, store.TicketPanel{
		ID:               panelID,
		GuildID:          project.GuildID,
		ProjectID:        project.ID,
		ManagedAlias:     panel.Alias,
		PanelChannelID:   resourceIDs[panel.PanelChannelAlias],
		Title:            panel.Title,
		Body:             panel.Body,
		DepartmentsJSON:  string(departmentsJSON),
		StaffRoleIDsJSON: string(staffRoleIDsJSON),
		TargetCategoryID: resourceIDs[panel.TargetCategoryAlias],
		ThreadMode:       panel.ThreadMode,
		Enabled:          true,
		CreatedBy:        project.ActorID,
		PanelMessageID:   existing.PanelMessageID,
	})
	if err != nil {
		return store.TicketPanel{}, err
	}
	if existed && strings.TrimSpace(existing.PanelMessageID) != "" {
		if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, stored.ID, stored.Title)); err != nil {
			return store.TicketPanel{}, err
		}
		return stored, nil
	}
	components := make([]MessageComponent, 0, len(departments))
	for _, department := range departments {
		components = append(components, MessageComponent{
			Type:     "button",
			Label:    department.Label,
			CustomID: TicketOpenCustomID(stored.ID, department.ID),
			Style:    "primary",
		})
	}
	message, err := s.adapter.SendMessage(ctx, MessageApplyRequest{
		ChannelID:  stored.PanelChannelID,
		Content:    "**" + panel.Title + "**\n" + panel.Body,
		Reason:     "Panda ticket panel " + project.ID,
		Components: components,
	})
	if err != nil {
		return store.TicketPanel{}, err
	}
	stored.PanelMessageID = message.ID
	stored, err = s.repo.UpsertTicketPanel(ctx, stored)
	if err != nil {
		return store.TicketPanel{}, err
	}
	if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, stored.ID, stored.Title)); err != nil {
		return store.TicketPanel{}, err
	}
	return stored, nil
}

func (s *Service) applyOnboardingFlow(ctx context.Context, project store.GuildSetupProject, step PlanStep, flow OnboardingFlowTemplate, resourceIDs map[string]string) (store.OnboardingFlow, error) {
	resolvedSteps := resolveOnboardingSteps(flow.Steps, resourceIDs)
	stepsJSON, _ := json.Marshal(resolvedSteps)
	flowID := stableFlowID(project.GuildID, flow.Alias)
	_, existed, err := s.repo.GetOnboardingFlow(ctx, flowID)
	if err != nil {
		return store.OnboardingFlow{}, err
	}
	stored, err := s.repo.UpsertOnboardingFlow(ctx, store.OnboardingFlow{
		ID:                flowID,
		GuildID:           project.GuildID,
		ProjectID:         project.ID,
		ManagedAlias:      flow.Alias,
		WelcomeChannelID:  resourceIDs[flow.WelcomeChannelAlias],
		RulesChannelID:    resourceIDs[flow.RulesChannelAlias],
		VerifiedRoleID:    resourceIDs[flow.VerifiedRoleAlias],
		NewcomerRoleID:    resourceIDs[flow.NewcomerRoleAlias],
		VerificationMode:  firstNonEmpty(flow.VerificationMode, "rules"),
		StepsJSON:         string(stepsJSON),
		Enabled:           true,
		Paused:            false,
		CompletionMessage: firstNonEmpty(flow.CompletionMessage, "You are verified. Welcome in."),
		IntroPrompt:       flow.IntroPrompt,
		CreatedBy:         project.ActorID,
	})
	if err != nil {
		return store.OnboardingFlow{}, err
	}
	if !existed && stored.WelcomeChannelID != "" {
		components := []MessageComponent{}
		if onboardingRequiresRules(stored.VerificationMode, flow.Steps) {
			components = append(components, MessageComponent{
				Type:     "button",
				Label:    "Acknowledge rules",
				CustomID: OnboardingAcknowledgeCustomID(stored.ID),
				Style:    "success",
			})
		}
		if len(components) > 0 {
			_, err = s.adapter.SendMessage(ctx, MessageApplyRequest{
				ChannelID:  stored.WelcomeChannelID,
				Content:    firstNonEmpty(stored.IntroPrompt, "Welcome. Acknowledge the rules to finish onboarding."),
				Reason:     "Panda onboarding flow " + project.ID,
				Components: components,
			})
			if err != nil {
				return store.OnboardingFlow{}, err
			}
		}
		if onboardingRequiresRoleSelection(stored.VerificationMode, resolvedSteps) {
			for _, step := range flow.Steps {
				if !isRoleSelectionStep(step) {
					continue
				}
				component, ok := roleSelectionComponent(stored.ID, step, resourceIDs)
				if !ok {
					continue
				}
				_, err = s.adapter.SendMessage(ctx, MessageApplyRequest{
					ChannelID:  stored.WelcomeChannelID,
					Content:    firstNonEmpty(step.Prompt, "Choose your optional roles."),
					Reason:     "Panda onboarding role selection " + project.ID,
					Components: []MessageComponent{component},
				})
				if err != nil {
					return store.OnboardingFlow{}, err
				}
			}
		}
		if !onboardingRequiresRules(stored.VerificationMode, flow.Steps) && !onboardingRequiresRoleSelection(stored.VerificationMode, flow.Steps) {
			_, err = s.adapter.SendMessage(ctx, MessageApplyRequest{
				ChannelID: stored.WelcomeChannelID,
				Content:   firstNonEmpty(stored.IntroPrompt, "Welcome. Acknowledge the rules to finish onboarding."),
				Reason:    "Panda onboarding flow " + project.ID,
				Components: []MessageComponent{{
					Type:     "button",
					Label:    "Acknowledge rules",
					CustomID: OnboardingAcknowledgeCustomID(stored.ID),
					Style:    "success",
				}},
			})
			if err != nil {
				return store.OnboardingFlow{}, err
			}
		}
	}
	if _, err := s.repo.UpsertResource(ctx, setupResource(project, step, stored.ID, stored.VerificationMode)); err != nil {
		return store.OnboardingFlow{}, err
	}
	return stored, nil
}

func (s *Service) OpenTicket(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	if s == nil || s.repo == nil || s.adapter == nil {
		return ComponentResponse{}, errors.New("ticketing is not configured for this runtime")
	}
	panelID, departmentID, ok := ParseTicketOpenCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid ticket component")
	}
	if ok, retry := s.ticketLimits.Allow(request.GuildID + ":" + request.UserID); !ok {
		return ComponentResponse{Content: "You are opening tickets too quickly. Try again in " + retry.Round(time.Second).String() + ".", Ephemeral: true, Title: "Ticket rate limited", Accent: "warning"}, nil
	}
	panel, ok, err := s.repo.GetTicketPanel(ctx, panelID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if !ok || !panel.Enabled || panel.GuildID != request.GuildID {
		return ComponentResponse{Content: "That ticket panel is no longer active.", Ephemeral: true, Title: "Ticket unavailable", Accent: "warning"}, nil
	}
	department, ok := ticketDepartment(panel, departmentID)
	if !ok {
		return ComponentResponse{Content: "That ticket department is no longer configured.", Ephemeral: true, Title: "Ticket unavailable", Accent: "warning"}, nil
	}
	ticketID, err := randomID("tk")
	if err != nil {
		return ComponentResponse{}, err
	}
	channelName := "ticket-" + safeTicketName(request.UserID)
	ticketChannel, err := s.adapter.CreateTicketChannel(ctx, TicketChannelRequest{
		GuildID:         panel.GuildID,
		Name:            channelName,
		CategoryID:      panel.TargetCategoryID,
		RequesterUserID: request.UserID,
		StaffRoleIDs:    uniqueStrings(append(department.StaffRoleIDs, stringSliceFromJSON(panel.StaffRoleIDsJSON)...)),
		Topic:           "Panda ticket " + ticketID + " for department " + department.Label,
		StarterMessage:  "Ticket opened by <@" + request.UserID + "> for **" + department.Label + "**.",
		Reason:          "Panda ticket " + ticketID,
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	tagsJSON, _ := json.Marshal([]string{department.ID})
	ticket, err := s.repo.CreateTicketWithEvent(ctx, store.Ticket{
		ID:              ticketID,
		GuildID:         panel.GuildID,
		PanelID:         panel.ID,
		DepartmentID:    department.ID,
		RequesterUserID: request.UserID,
		ChannelID:       ticketChannel.ID,
		Status:          TicketStatusOpen,
		Priority:        firstNonEmpty(department.InitialPriority, "normal"),
		TagsJSON:        string(tagsJSON),
	}, store.TicketEvent{
		ActorID:   request.UserID,
		EventType: "ticket.opened",
		MetadataJSON: mustJSON(map[string]any{
			"panel_id":      panel.ID,
			"department_id": department.ID,
			"channel_id":    ticketChannel.ID,
		}),
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	_, _ = s.adapter.SendMessage(ctx, MessageApplyRequest{
		ChannelID: ticket.ChannelID,
		Content:   "Staff controls for this ticket.",
		Reason:    "Panda ticket controls " + ticket.ID,
		Components: []MessageComponent{
			{Type: "button", Label: "Claim", CustomID: TicketActionCustomID("claim", ticket.ID), Style: "primary"},
			{Type: "button", Label: "Close", CustomID: TicketActionCustomID("close", ticket.ID), Style: "danger"},
		},
	})
	s.recordAudit(ctx, panel.GuildID, request.UserID, "ticket.opened", "ticket", ticket.ID, map[string]any{"department_id": department.ID, "channel_id": ticket.ChannelID})
	return ComponentResponse{Content: "Ticket opened: <#" + ticket.ChannelID + ">.", Ephemeral: true, Title: "Ticket opened", Accent: "success"}, nil
}

func (s *Service) HandleTicketAction(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	if s == nil || s.repo == nil {
		return ComponentResponse{}, errors.New("ticketing is not configured for this runtime")
	}
	action, ticketID, ok := ParseTicketActionCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid ticket action")
	}
	ticket, ok, err := s.repo.GetTicket(ctx, ticketID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if !ok || ticket.GuildID != request.GuildID {
		return ComponentResponse{Content: "That ticket no longer exists.", Ephemeral: true, Title: "Ticket unavailable", Accent: "warning"}, nil
	}
	if action == "close" {
		return ComponentResponse{
			Ephemeral: true,
			Modal: &ComponentModal{
				ID:    TicketCloseModalID(ticket.ID),
				Title: "Close Ticket",
				Fields: []ComponentModalField{{
					ID:          "reason",
					Label:       "Close reason",
					Placeholder: "Optional context for the transcript",
					Required:    false,
					Paragraph:   true,
					MaxLength:   500,
				}},
			},
		}, nil
	}
	if action != "claim" {
		return ComponentResponse{Content: "That ticket action is not supported.", Ephemeral: true, Title: "Ticket action unavailable", Accent: "warning"}, nil
	}
	updated, err := s.repo.UpdateTicketWithEvent(ctx, ticket.ID, map[string]any{
		"status":           TicketStatusClaimed,
		"assignee_user_id": request.UserID,
	}, store.TicketEvent{
		ActorID:      request.UserID,
		EventType:    "ticket.claimed",
		MetadataJSON: mustJSON(map[string]string{"assignee_user_id": request.UserID}),
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	return ComponentResponse{Content: "Ticket claimed by <@" + updated.AssigneeUserID + ">.", Ephemeral: false, Title: "Ticket claimed", Accent: "success"}, nil
}

func (s *Service) HandleTicketCloseModal(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	if s == nil || s.repo == nil {
		return ComponentResponse{}, errors.New("ticketing is not configured for this runtime")
	}
	ticketID, ok := ParseTicketCloseModalID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid ticket close modal")
	}
	ticket, ok, err := s.repo.GetTicket(ctx, ticketID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if !ok || ticket.GuildID != request.GuildID {
		return ComponentResponse{Content: "That ticket no longer exists.", Ephemeral: true, Title: "Ticket unavailable", Accent: "warning"}, nil
	}
	reason := strings.TrimSpace(request.Fields["reason"])
	transcript := map[string]any{}
	if s.adapter != nil && ticket.ChannelID != "" {
		transcript, _ = s.adapter.ExportTranscript(ctx, ticket.ChannelID, 200)
	}
	closedAt := s.now().UTC()
	_, err = s.repo.UpdateTicketWithEvent(ctx, ticket.ID, map[string]any{
		"status":          TicketStatusClosed,
		"close_reason":    reason,
		"transcript_json": mustJSON(transcript),
		"closed_at":       closedAt,
	}, store.TicketEvent{
		ActorID:      request.UserID,
		EventType:    "ticket.closed",
		MetadataJSON: mustJSON(map[string]string{"reason": reason}),
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	return ComponentResponse{Content: "Ticket closed. Transcript metadata has been retained.", Ephemeral: false, Title: "Ticket closed", Accent: "success"}, nil
}

func (s *Service) CompleteOnboarding(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	if s == nil || s.repo == nil || s.adapter == nil {
		return ComponentResponse{}, errors.New("onboarding is not configured for this runtime")
	}
	flowID, ok := ParseOnboardingAcknowledgeCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid onboarding component")
	}
	flow, ok, err := s.repo.GetOnboardingFlow(ctx, flowID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if !ok || flow.GuildID != request.GuildID || !flow.Enabled {
		return ComponentResponse{Content: "That onboarding flow is no longer active.", Ephemeral: true, Title: "Onboarding unavailable", Accent: "warning"}, nil
	}
	if flow.Paused {
		return ComponentResponse{Content: "Onboarding is currently paused by an admin.", Ephemeral: true, Title: "Onboarding paused", Accent: "warning"}, nil
	}
	steps := onboardingSteps(flow)
	session, hasSession, err := s.repo.GetOnboardingSession(ctx, flow.ID, request.UserID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if onboardingRequiresRoleSelection(flow.VerificationMode, steps) && !hasCompletedRoleSelection(session, hasSession) {
		now := s.now().UTC()
		_, err = s.repo.UpsertOnboardingSession(ctx, store.OnboardingSession{
			ID:                stableSessionID(flow.ID, request.UserID),
			FlowID:            flow.ID,
			GuildID:           flow.GuildID,
			UserID:            request.UserID,
			Status:            OnboardingStatusInProgress,
			CurrentStep:       "rules_acknowledged",
			LastInteractionAt: now,
		})
		if err != nil {
			return ComponentResponse{}, err
		}
		return ComponentResponse{Content: "Rules acknowledged. Choose your roles from the onboarding menu to finish.", Ephemeral: true, Title: "Roles still needed", Accent: "warning"}, nil
	}
	return s.completeOnboarding(ctx, flow, request.UserID, selectedRoleIDs(session, hasSession))
}

func (s *Service) HandleOnboardingRoleSelection(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	if s == nil || s.repo == nil || s.adapter == nil {
		return ComponentResponse{}, errors.New("onboarding is not configured for this runtime")
	}
	flowID, stepID, ok := ParseOnboardingRoleSelectCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid onboarding role selection")
	}
	flow, ok, err := s.repo.GetOnboardingFlow(ctx, flowID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if !ok || flow.GuildID != request.GuildID || !flow.Enabled {
		return ComponentResponse{Content: "That onboarding flow is no longer active.", Ephemeral: true, Title: "Onboarding unavailable", Accent: "warning"}, nil
	}
	if flow.Paused {
		return ComponentResponse{Content: "Onboarding is currently paused by an admin.", Ephemeral: true, Title: "Onboarding paused", Accent: "warning"}, nil
	}
	step, ok := onboardingStepByID(onboardingSteps(flow), stepID)
	if !ok || !isRoleSelectionStep(step) {
		return ComponentResponse{Content: "That role menu is no longer configured.", Ephemeral: true, Title: "Onboarding unavailable", Accent: "warning"}, nil
	}
	allowed := allowedRoleSelectionValues(step)
	selected, err := validateRoleSelection(request.Values, allowed, step)
	if err != nil {
		return ComponentResponse{Content: err.Error(), Ephemeral: true, Title: "Role selection unavailable", Accent: "warning"}, nil
	}
	assigned := []string{}
	for _, roleID := range selected {
		if err := s.adapter.AddMemberRole(ctx, flow.GuildID, request.UserID, roleID, "Panda onboarding role selection"); err != nil {
			return ComponentResponse{}, err
		}
		assigned = append(assigned, roleID)
	}
	existing, hasSession, err := s.repo.GetOnboardingSession(ctx, flow.ID, request.UserID)
	if err != nil {
		return ComponentResponse{}, err
	}
	rulesAcknowledged := hasSession && (existing.CurrentStep == "rules_acknowledged" || existing.CurrentStep == "complete" || existing.Status == OnboardingStatusCompleted)
	now := s.now().UTC()
	assignedJSON, _ := json.Marshal(assigned)
	selectedJSON, _ := json.Marshal(selected)
	currentStep := "roles_selected"
	if rulesAcknowledged {
		currentStep = "rules_acknowledged"
	}
	_, err = s.repo.UpsertOnboardingSession(ctx, store.OnboardingSession{
		ID:                  stableSessionID(flow.ID, request.UserID),
		FlowID:              flow.ID,
		GuildID:             flow.GuildID,
		UserID:              request.UserID,
		Status:              OnboardingStatusInProgress,
		CurrentStep:         currentStep,
		SelectedRoleIDsJSON: string(selectedJSON),
		AssignedRoleIDsJSON: string(assignedJSON),
		LastInteractionAt:   now,
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	if onboardingRequiresRules(flow.VerificationMode, onboardingSteps(flow)) && !rulesAcknowledged {
		return ComponentResponse{Content: "Roles saved. Acknowledge the rules to finish onboarding.", Ephemeral: true, Title: "Roles saved", Accent: "success"}, nil
	}
	return s.completeOnboarding(ctx, flow, request.UserID, selected)
}

func (s *Service) completeOnboarding(ctx context.Context, flow store.OnboardingFlow, userID string, selectedRoleIDs []string) (ComponentResponse, error) {
	assigned := []string{}
	if flow.VerifiedRoleID != "" {
		if err := s.adapter.AddMemberRole(ctx, flow.GuildID, userID, flow.VerifiedRoleID, "Panda onboarding completion"); err != nil {
			return ComponentResponse{}, err
		}
		assigned = append(assigned, flow.VerifiedRoleID)
	}
	if flow.NewcomerRoleID != "" {
		_ = s.adapter.RemoveMemberRole(ctx, flow.GuildID, userID, flow.NewcomerRoleID, "Panda onboarding completion")
	}
	assigned = uniqueStrings(append(assigned, selectedRoleIDs...))
	assignedJSON, _ := json.Marshal(assigned)
	selectedJSON, _ := json.Marshal(selectedRoleIDs)
	completedAt := s.now().UTC()
	_, err := s.repo.UpsertOnboardingSession(ctx, store.OnboardingSession{
		ID:                  stableSessionID(flow.ID, userID),
		FlowID:              flow.ID,
		GuildID:             flow.GuildID,
		UserID:              userID,
		Status:              OnboardingStatusCompleted,
		CurrentStep:         "complete",
		SelectedRoleIDsJSON: string(selectedJSON),
		AssignedRoleIDsJSON: string(assignedJSON),
		CompletedAt:         &completedAt,
		LastInteractionAt:   completedAt,
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	s.recordAudit(ctx, flow.GuildID, userID, "onboarding.completed", "onboarding_flow", flow.ID, map[string]any{"assigned_role_ids": assigned, "selected_role_ids": selectedRoleIDs})
	return ComponentResponse{Content: firstNonEmpty(flow.CompletionMessage, "You are verified. Welcome in."), Ephemeral: true, Title: "Onboarding complete", Accent: "success"}, nil
}

func (s *Service) template(ctx context.Context, id string) (Template, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "minimal_community"
	}
	if template, ok := BuiltInTemplateByID(id); ok {
		return template, nil
	}
	stored, ok, err := s.repo.GetTemplate(ctx, id)
	if err != nil {
		return Template{}, err
	}
	if !ok {
		return Template{}, fmt.Errorf("%w: %s", ErrUnknownTemplate, id)
	}
	var template Template
	if err := json.Unmarshal([]byte(stored.TemplateJSON), &template); err != nil {
		return Template{}, err
	}
	return template, nil
}

func projectPlan(project store.GuildSetupProject) ([]PlanStep, error) {
	var steps []PlanStep
	if err := json.Unmarshal([]byte(project.ApplyPlanJSON), &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

func setupResource(project store.GuildSetupProject, step PlanStep, objectID, displayName string) store.GuildSetupResource {
	return store.GuildSetupResource{
		GuildID:         project.GuildID,
		ProjectID:       project.ID,
		ManagedAlias:    step.Alias,
		ObjectType:      step.ResourceType,
		ObjectID:        objectID,
		TemplateID:      project.TemplateID,
		TemplateVersion: project.TemplateVersion,
		LastAppliedHash: step.Hash,
		DisplayName:     displayName,
	}
}

func decodeStepPayload(step PlanStep, key string, target any) error {
	payload := step.Payload
	if strings.TrimSpace(key) != "" {
		payload = map[string]any{key: step.Payload[key]}
		raw, err := json.Marshal(payload[key])
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, target)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func resolveOverwrites(guildID string, overwrites []OverwriteTemplate, resourceIDs map[string]string) []ResolvedOverwrite {
	result := make([]ResolvedOverwrite, 0, len(overwrites))
	for _, overwrite := range overwrites {
		targetID := overwrite.TargetAlias
		if overwrite.TargetAlias == "@everyone" {
			targetID = guildID
		} else if resolved := resourceIDs[overwrite.TargetAlias]; resolved != "" {
			targetID = resolved
		}
		result = append(result, ResolvedOverwrite{
			TargetID:   targetID,
			TargetType: firstNonEmpty(overwrite.TargetType, "role"),
			Allow:      append([]string(nil), overwrite.Allow...),
			Deny:       append([]string(nil), overwrite.Deny...),
		})
	}
	return result
}

func resolveAliases(aliases []string, resourceIDs map[string]string) []string {
	result := []string{}
	for _, alias := range aliases {
		if id := resourceIDs[alias]; id != "" {
			result = append(result, id)
		}
	}
	return uniqueStrings(result)
}

func resolveOnboardingSteps(steps []OnboardingStepTemplate, resourceIDs map[string]string) []OnboardingStepTemplate {
	resolved := make([]OnboardingStepTemplate, 0, len(steps))
	for _, step := range steps {
		next := step
		if len(step.RoleAliases) > 0 {
			next.RoleAliases = make([]string, 0, len(step.RoleAliases))
			for _, alias := range step.RoleAliases {
				if id := resourceIDs[alias]; id != "" {
					next.RoleAliases = append(next.RoleAliases, id)
					continue
				}
				next.RoleAliases = append(next.RoleAliases, alias)
			}
			next.RoleAliases = uniqueStrings(next.RoleAliases)
		}
		resolved = append(resolved, next)
	}
	return resolved
}

func onboardingSteps(flow store.OnboardingFlow) []OnboardingStepTemplate {
	var steps []OnboardingStepTemplate
	_ = json.Unmarshal([]byte(flow.StepsJSON), &steps)
	return steps
}

func onboardingStepByID(steps []OnboardingStepTemplate, id string) (OnboardingStepTemplate, bool) {
	id = strings.TrimSpace(id)
	for _, step := range steps {
		if strings.TrimSpace(step.ID) == id {
			return step, true
		}
	}
	return OnboardingStepTemplate{}, false
}

func onboardingRequiresRules(mode string, steps []OnboardingStepTemplate) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "role_selection":
		return false
	case "rules", "rules_and_roles", "":
		return true
	}
	for _, step := range steps {
		if strings.EqualFold(strings.TrimSpace(step.Type), "rules_ack") {
			return true
		}
	}
	return false
}

func onboardingRequiresRoleSelection(mode string, steps []OnboardingStepTemplate) bool {
	if strings.EqualFold(strings.TrimSpace(mode), "rules") {
		return false
	}
	for _, step := range steps {
		if isRoleSelectionStep(step) && len(step.RoleAliases) > 0 {
			return true
		}
	}
	return strings.EqualFold(strings.TrimSpace(mode), "role_selection") || strings.EqualFold(strings.TrimSpace(mode), "rules_and_roles")
}

func isRoleSelectionStep(step OnboardingStepTemplate) bool {
	return strings.EqualFold(strings.TrimSpace(step.Type), "role_selection")
}

func roleSelectionComponent(flowID string, step OnboardingStepTemplate, resourceIDs map[string]string) (MessageComponent, bool) {
	options := make([]MessageComponentOption, 0, len(step.RoleAliases))
	for _, alias := range step.RoleAliases {
		roleID := firstNonEmpty(resourceIDs[alias], alias)
		if strings.TrimSpace(roleID) == "" {
			continue
		}
		options = append(options, MessageComponentOption{
			Label:       humanizeRoleAlias(alias),
			Value:       roleID,
			Description: "Add or keep this role after onboarding.",
		})
		if len(options) == 25 {
			break
		}
	}
	if len(options) == 0 {
		return MessageComponent{}, false
	}
	minValues := step.MinSelections
	if minValues < 0 {
		minValues = 0
	}
	if step.Required && minValues == 0 {
		minValues = 1
	}
	maxValues := step.MaxSelections
	if maxValues <= 0 || maxValues > len(options) {
		maxValues = len(options)
	}
	if maxValues < minValues {
		maxValues = minValues
	}
	return MessageComponent{
		Type:        "select",
		CustomID:    OnboardingRoleSelectCustomID(flowID, step.ID),
		Placeholder: "Choose roles",
		MinValues:   minValues,
		MaxValues:   maxValues,
		Options:     options,
	}, true
}

func allowedRoleSelectionValues(step OnboardingStepTemplate) map[string]struct{} {
	allowed := map[string]struct{}{}
	for _, roleID := range step.RoleAliases {
		roleID = strings.TrimSpace(roleID)
		if roleID != "" {
			allowed[roleID] = struct{}{}
		}
	}
	return allowed
}

func validateRoleSelection(values []string, allowed map[string]struct{}, step OnboardingStepTemplate) ([]string, error) {
	selected := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := allowed[value]; !ok {
			return nil, errors.New("That role selection is no longer available.")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		selected = append(selected, value)
	}
	minValues := step.MinSelections
	if minValues < 0 {
		minValues = 0
	}
	if step.Required && minValues == 0 {
		minValues = 1
	}
	maxValues := step.MaxSelections
	if maxValues <= 0 || maxValues > len(allowed) {
		maxValues = len(allowed)
	}
	if len(selected) < minValues {
		return nil, fmt.Errorf("Choose at least %d role(s) to continue.", minValues)
	}
	if len(selected) > maxValues {
		return nil, fmt.Errorf("Choose no more than %d role(s).", maxValues)
	}
	return selected, nil
}

func hasCompletedRoleSelection(session store.OnboardingSession, ok bool) bool {
	if !ok {
		return false
	}
	return session.Status == OnboardingStatusCompleted || session.CurrentStep == "roles_selected" || len(stringSliceFromJSON(session.SelectedRoleIDsJSON)) > 0
}

func selectedRoleIDs(session store.OnboardingSession, ok bool) []string {
	if !ok {
		return nil
	}
	return stringSliceFromJSON(session.SelectedRoleIDsJSON)
}

func humanizeRoleAlias(alias string) string {
	alias = strings.TrimSpace(alias)
	alias = strings.TrimPrefix(alias, "role_")
	alias = strings.NewReplacer("_", " ", "-", " ").Replace(alias)
	words := strings.Fields(alias)
	for index, word := range words {
		if len(word) == 0 {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	if len(words) == 0 {
		return "Role"
	}
	return strings.Join(words, " ")
}

func ticketDepartment(panel store.TicketPanel, departmentID string) (TicketDepartment, bool) {
	var departments []TicketDepartment
	if err := json.Unmarshal([]byte(panel.DepartmentsJSON), &departments); err != nil {
		return TicketDepartment{}, false
	}
	for _, department := range departments {
		if department.ID == departmentID {
			return department, true
		}
	}
	return TicketDepartment{}, false
}

func stringSliceFromJSON(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}

func safeTicketName(userID string) string {
	userID = strings.TrimSpace(userID)
	if len(userID) > 8 {
		userID = userID[len(userID)-8:]
	}
	if userID == "" {
		return "member"
	}
	return userID
}

func randomID(prefix string) (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return strings.TrimSpace(prefix) + "_" + hex.EncodeToString(bytes[:]), nil
}

func stablePanelID(guildID, alias string) string {
	return "tp_" + stableHash(map[string]string{"guild": guildID, "alias": alias})[:24]
}

func stableFlowID(guildID, alias string) string {
	return "of_" + stableHash(map[string]string{"guild": guildID, "alias": alias})[:24]
}

func stableSessionID(flowID, userID string) string {
	return "os_" + stableHash(map[string]string{"flow": flowID, "user": userID})[:24]
}

func allowSetupActor(request SetupRequest) bool {
	return strings.TrimSpace(request.GuildID) != "" && strings.TrimSpace(request.ActorID) != ""
}

func firstNonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil || string(raw) == "null" {
		return "{}"
	}
	return string(raw)
}

func (s *Service) recordAudit(ctx context.Context, guildID, actorID, action, targetType, targetID string, metadata map[string]any) {
	if s == nil || s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    strings.TrimSpace(guildID),
		ActorID:    strings.TrimSpace(actorID),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Metadata:   mustJSON(metadata),
		CreatedAt:  s.now().UTC(),
	})
}
