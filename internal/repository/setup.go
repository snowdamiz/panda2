package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SetupRepository struct {
	db *gorm.DB
}

func NewSetupRepository(db *gorm.DB) *SetupRepository {
	return &SetupRepository{db: db}
}

func (r *SetupRepository) UpsertTemplate(ctx context.Context, template store.SetupTemplate) (store.SetupTemplate, error) {
	now := time.Now().UTC()
	template.ID = strings.TrimSpace(template.ID)
	template.Name = strings.TrimSpace(template.Name)
	template.ReleaseState = firstNonEmpty(strings.TrimSpace(template.ReleaseState), "stable")
	template.DefaultVariables = firstNonEmpty(strings.TrimSpace(template.DefaultVariables), "{}")
	template.TemplateJSON = firstNonEmpty(strings.TrimSpace(template.TemplateJSON), "{}")
	template.CreatedAt = firstTime(template.CreatedAt, now)
	template.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"schema_version":    template.SchemaVersion,
			"template_version":  template.TemplateVersion,
			"name":              template.Name,
			"description":       template.Description,
			"release_state":     template.ReleaseState,
			"default_variables": template.DefaultVariables,
			"template_json":     template.TemplateJSON,
			"built_in":          template.BuiltIn,
			"created_by":        template.CreatedBy,
			"updated_at":        template.UpdatedAt,
			"archived_at":       template.ArchivedAt,
		}),
	}).Create(&template).Error
	return template, err
}

func (r *SetupRepository) ListTemplates(ctx context.Context, includeArchived bool) ([]store.SetupTemplate, error) {
	var templates []store.SetupTemplate
	query := r.db.WithContext(ctx).Order("built_in DESC, name ASC")
	if !includeArchived {
		query = query.Where("archived_at IS NULL")
	}
	err := query.Find(&templates).Error
	return templates, err
}

func (r *SetupRepository) GetTemplate(ctx context.Context, id string) (store.SetupTemplate, bool, error) {
	var template store.SetupTemplate
	err := r.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).First(&template).Error
	if err == nil {
		return template, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.SetupTemplate{}, false, nil
	}
	return store.SetupTemplate{}, false, err
}

func (r *SetupRepository) CreateProject(ctx context.Context, project store.GuildSetupProject) (store.GuildSetupProject, error) {
	now := time.Now().UTC()
	project.ID = strings.TrimSpace(project.ID)
	project.GuildID = strings.TrimSpace(project.GuildID)
	project.TemplateID = strings.TrimSpace(project.TemplateID)
	project.Status = firstNonEmpty(strings.TrimSpace(project.Status), "draft")
	project.VariablesJSON = firstNonEmpty(strings.TrimSpace(project.VariablesJSON), "{}")
	project.PreviewJSON = firstNonEmpty(strings.TrimSpace(project.PreviewJSON), "{}")
	project.ApplyPlanJSON = firstNonEmpty(strings.TrimSpace(project.ApplyPlanJSON), "[]")
	project.ProgressJSON = firstNonEmpty(strings.TrimSpace(project.ProgressJSON), "{}")
	project.FailedStepsJSON = firstNonEmpty(strings.TrimSpace(project.FailedStepsJSON), "[]")
	project.RecoveryJSON = firstNonEmpty(strings.TrimSpace(project.RecoveryJSON), "{}")
	project.CreatedAt = firstTime(project.CreatedAt, now)
	project.UpdatedAt = firstTime(project.UpdatedAt, now)
	return project, r.db.WithContext(ctx).Create(&project).Error
}

func (r *SetupRepository) GetProject(ctx context.Context, id string) (store.GuildSetupProject, bool, error) {
	var project store.GuildSetupProject
	err := r.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).First(&project).Error
	if err == nil {
		return project, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.GuildSetupProject{}, false, nil
	}
	return store.GuildSetupProject{}, false, err
}

func (r *SetupRepository) GetLatestProjectForGuild(ctx context.Context, guildID string) (store.GuildSetupProject, bool, error) {
	var project store.GuildSetupProject
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", strings.TrimSpace(guildID)).
		Order("created_at DESC").
		First(&project).Error
	if err == nil {
		return project, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.GuildSetupProject{}, false, nil
	}
	return store.GuildSetupProject{}, false, err
}

func (r *SetupRepository) UpdateProject(ctx context.Context, id string, updates map[string]any) (store.GuildSetupProject, error) {
	if len(updates) == 0 {
		project, ok, err := r.GetProject(ctx, id)
		if err != nil {
			return store.GuildSetupProject{}, err
		}
		if !ok {
			return store.GuildSetupProject{}, ErrNotFound
		}
		return project, nil
	}
	updates["updated_at"] = time.Now().UTC()
	var project store.GuildSetupProject
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&store.GuildSetupProject{}).Where("id = ?", strings.TrimSpace(id)).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.Where("id = ?", strings.TrimSpace(id)).First(&project).Error
	})
	return project, err
}

func (r *SetupRepository) ListResources(ctx context.Context, guildID string) ([]store.GuildSetupResource, error) {
	var resources []store.GuildSetupResource
	err := r.db.WithContext(ctx).
		Where("guild_id = ?", strings.TrimSpace(guildID)).
		Order("managed_alias ASC").
		Find(&resources).Error
	return resources, err
}

func (r *SetupRepository) GetResource(ctx context.Context, guildID, alias string) (store.GuildSetupResource, bool, error) {
	var resource store.GuildSetupResource
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND managed_alias = ?", strings.TrimSpace(guildID), strings.TrimSpace(alias)).
		First(&resource).Error
	if err == nil {
		return resource, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.GuildSetupResource{}, false, nil
	}
	return store.GuildSetupResource{}, false, err
}

func (r *SetupRepository) UpsertResource(ctx context.Context, resource store.GuildSetupResource) (store.GuildSetupResource, error) {
	now := time.Now().UTC()
	resource.GuildID = strings.TrimSpace(resource.GuildID)
	resource.ManagedAlias = strings.TrimSpace(resource.ManagedAlias)
	resource.ObjectType = strings.TrimSpace(resource.ObjectType)
	resource.ObjectID = strings.TrimSpace(resource.ObjectID)
	resource.CreatedAt = firstTime(resource.CreatedAt, now)
	resource.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "guild_id"}, {Name: "managed_alias"}},
		DoUpdates: clause.Assignments(map[string]any{
			"project_id":        resource.ProjectID,
			"object_type":       resource.ObjectType,
			"object_id":         resource.ObjectID,
			"template_id":       resource.TemplateID,
			"template_version":  resource.TemplateVersion,
			"last_applied_hash": resource.LastAppliedHash,
			"display_name":      resource.DisplayName,
			"updated_at":        resource.UpdatedAt,
		}),
	}).Create(&resource).Error
	if err != nil {
		return store.GuildSetupResource{}, err
	}
	stored, _, err := r.GetResource(ctx, resource.GuildID, resource.ManagedAlias)
	return stored, err
}

func (r *SetupRepository) DeleteResource(ctx context.Context, guildID, alias string) error {
	result := r.db.WithContext(ctx).
		Where("guild_id = ? AND managed_alias = ?", strings.TrimSpace(guildID), strings.TrimSpace(alias)).
		Delete(&store.GuildSetupResource{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SetupRepository) UpsertTicketPanel(ctx context.Context, panel store.TicketPanel) (store.TicketPanel, error) {
	now := time.Now().UTC()
	panel.ID = strings.TrimSpace(panel.ID)
	panel.GuildID = strings.TrimSpace(panel.GuildID)
	panel.PanelChannelID = strings.TrimSpace(panel.PanelChannelID)
	panel.DepartmentsJSON = firstNonEmpty(strings.TrimSpace(panel.DepartmentsJSON), "[]")
	panel.StaffRoleIDsJSON = firstNonEmpty(strings.TrimSpace(panel.StaffRoleIDsJSON), "[]")
	panel.CreatedAt = firstTime(panel.CreatedAt, now)
	panel.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"guild_id":            panel.GuildID,
			"project_id":          panel.ProjectID,
			"managed_alias":       panel.ManagedAlias,
			"panel_channel_id":    panel.PanelChannelID,
			"panel_message_id":    panel.PanelMessageID,
			"title":               panel.Title,
			"body":                panel.Body,
			"departments_json":    panel.DepartmentsJSON,
			"staff_role_ids_json": panel.StaffRoleIDsJSON,
			"target_category_id":  panel.TargetCategoryID,
			"thread_mode":         panel.ThreadMode,
			"enabled":             panel.Enabled,
			"created_by":          panel.CreatedBy,
			"updated_at":          panel.UpdatedAt,
		}),
	}).Create(&panel).Error
	return panel, err
}

func (r *SetupRepository) GetTicketPanel(ctx context.Context, id string) (store.TicketPanel, bool, error) {
	var panel store.TicketPanel
	err := r.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).First(&panel).Error
	if err == nil {
		return panel, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.TicketPanel{}, false, nil
	}
	return store.TicketPanel{}, false, err
}

func (r *SetupRepository) GetTicketPanelByMessage(ctx context.Context, guildID, messageID string) (store.TicketPanel, bool, error) {
	var panel store.TicketPanel
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND panel_message_id = ?", strings.TrimSpace(guildID), strings.TrimSpace(messageID)).
		First(&panel).Error
	if err == nil {
		return panel, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.TicketPanel{}, false, nil
	}
	return store.TicketPanel{}, false, err
}

func (r *SetupRepository) ListTicketPanels(ctx context.Context, guildID string, includeDisabled bool) ([]store.TicketPanel, error) {
	var panels []store.TicketPanel
	query := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID)).Order("created_at ASC")
	if !includeDisabled {
		query = query.Where("enabled = ?", true)
	}
	err := query.Find(&panels).Error
	return panels, err
}

func (r *SetupRepository) CreateTicketWithEvent(ctx context.Context, ticket store.Ticket, event store.TicketEvent) (store.Ticket, error) {
	now := time.Now().UTC()
	ticket.ID = strings.TrimSpace(ticket.ID)
	ticket.GuildID = strings.TrimSpace(ticket.GuildID)
	ticket.Status = firstNonEmpty(strings.TrimSpace(ticket.Status), "open")
	ticket.Priority = firstNonEmpty(strings.TrimSpace(ticket.Priority), "normal")
	ticket.TagsJSON = firstNonEmpty(strings.TrimSpace(ticket.TagsJSON), "[]")
	ticket.TranscriptJSON = firstNonEmpty(strings.TrimSpace(ticket.TranscriptJSON), "{}")
	ticket.OpenedAt = firstTime(ticket.OpenedAt, now)
	ticket.CreatedAt = firstTime(ticket.CreatedAt, now)
	ticket.UpdatedAt = firstTime(ticket.UpdatedAt, now)
	event.TicketID = firstNonEmpty(strings.TrimSpace(event.TicketID), ticket.ID)
	event.GuildID = firstNonEmpty(strings.TrimSpace(event.GuildID), ticket.GuildID)
	event.EventType = firstNonEmpty(strings.TrimSpace(event.EventType), "ticket.opened")
	event.MetadataJSON = firstNonEmpty(strings.TrimSpace(event.MetadataJSON), "{}")
	event.CreatedAt = firstTime(event.CreatedAt, now)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ticket).Error; err != nil {
			return err
		}
		return tx.Create(&event).Error
	})
	return ticket, err
}

func (r *SetupRepository) GetTicket(ctx context.Context, id string) (store.Ticket, bool, error) {
	var ticket store.Ticket
	err := r.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).First(&ticket).Error
	if err == nil {
		return ticket, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.Ticket{}, false, nil
	}
	return store.Ticket{}, false, err
}

func (r *SetupRepository) UpdateTicketWithEvent(ctx context.Context, id string, updates map[string]any, event store.TicketEvent) (store.Ticket, error) {
	updates["updated_at"] = time.Now().UTC()
	var ticket store.Ticket
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&store.Ticket{}).Where("id = ?", strings.TrimSpace(id)).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("id = ?", strings.TrimSpace(id)).First(&ticket).Error; err != nil {
			return err
		}
		event.TicketID = firstNonEmpty(strings.TrimSpace(event.TicketID), ticket.ID)
		event.GuildID = firstNonEmpty(strings.TrimSpace(event.GuildID), ticket.GuildID)
		event.MetadataJSON = firstNonEmpty(strings.TrimSpace(event.MetadataJSON), "{}")
		event.CreatedAt = firstTime(event.CreatedAt, time.Now().UTC())
		return tx.Create(&event).Error
	})
	return ticket, err
}

func (r *SetupRepository) ListTickets(ctx context.Context, guildID, status string, limit int) ([]store.Ticket, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var tickets []store.Ticket
	query := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID))
	if strings.TrimSpace(status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(status))
	}
	err := query.Order("opened_at DESC").Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *SetupRepository) UpsertOnboardingFlow(ctx context.Context, flow store.OnboardingFlow) (store.OnboardingFlow, error) {
	now := time.Now().UTC()
	flow.ID = strings.TrimSpace(flow.ID)
	flow.GuildID = strings.TrimSpace(flow.GuildID)
	flow.VerificationMode = firstNonEmpty(strings.TrimSpace(flow.VerificationMode), "rules")
	flow.StepsJSON = firstNonEmpty(strings.TrimSpace(flow.StepsJSON), "[]")
	flow.CreatedAt = firstTime(flow.CreatedAt, now)
	flow.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"guild_id":           flow.GuildID,
			"project_id":         flow.ProjectID,
			"managed_alias":      flow.ManagedAlias,
			"welcome_channel_id": flow.WelcomeChannelID,
			"rules_channel_id":   flow.RulesChannelID,
			"verified_role_id":   flow.VerifiedRoleID,
			"newcomer_role_id":   flow.NewcomerRoleID,
			"verification_mode":  flow.VerificationMode,
			"steps_json":         flow.StepsJSON,
			"enabled":            flow.Enabled,
			"paused":             flow.Paused,
			"completion_message": flow.CompletionMessage,
			"intro_prompt":       flow.IntroPrompt,
			"created_by":         flow.CreatedBy,
			"updated_at":         flow.UpdatedAt,
		}),
	}).Create(&flow).Error
	return flow, err
}

func (r *SetupRepository) GetOnboardingFlow(ctx context.Context, id string) (store.OnboardingFlow, bool, error) {
	var flow store.OnboardingFlow
	err := r.db.WithContext(ctx).Where("id = ?", strings.TrimSpace(id)).First(&flow).Error
	if err == nil {
		return flow, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.OnboardingFlow{}, false, nil
	}
	return store.OnboardingFlow{}, false, err
}

func (r *SetupRepository) ListOnboardingFlows(ctx context.Context, guildID string, includePaused bool) ([]store.OnboardingFlow, error) {
	var flows []store.OnboardingFlow
	query := r.db.WithContext(ctx).Where("guild_id = ? AND enabled = ?", strings.TrimSpace(guildID), true)
	if !includePaused {
		query = query.Where("paused = ?", false)
	}
	err := query.Order("created_at ASC").Find(&flows).Error
	return flows, err
}

func (r *SetupRepository) UpsertOnboardingSession(ctx context.Context, session store.OnboardingSession) (store.OnboardingSession, error) {
	now := time.Now().UTC()
	session.ID = strings.TrimSpace(session.ID)
	session.FlowID = strings.TrimSpace(session.FlowID)
	session.GuildID = strings.TrimSpace(session.GuildID)
	session.UserID = strings.TrimSpace(session.UserID)
	session.Status = firstNonEmpty(strings.TrimSpace(session.Status), "in_progress")
	session.SelectedRoleIDsJSON = firstNonEmpty(strings.TrimSpace(session.SelectedRoleIDsJSON), "[]")
	session.AssignedRoleIDsJSON = firstNonEmpty(strings.TrimSpace(session.AssignedRoleIDsJSON), "[]")
	session.LastInteractionAt = firstTime(session.LastInteractionAt, now)
	session.CreatedAt = firstTime(session.CreatedAt, now)
	session.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "flow_id"}, {Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"status":                 session.Status,
			"current_step":           session.CurrentStep,
			"selected_role_ids_json": session.SelectedRoleIDsJSON,
			"assigned_role_ids_json": session.AssignedRoleIDsJSON,
			"completed_at":           session.CompletedAt,
			"last_interaction_at":    session.LastInteractionAt,
			"updated_at":             session.UpdatedAt,
		}),
	}).Create(&session).Error
	if err != nil {
		return store.OnboardingSession{}, err
	}
	var stored store.OnboardingSession
	err = r.db.WithContext(ctx).Where("flow_id = ? AND user_id = ?", session.FlowID, session.UserID).First(&stored).Error
	return stored, err
}

func (r *SetupRepository) GetOnboardingSession(ctx context.Context, flowID, userID string) (store.OnboardingSession, bool, error) {
	var session store.OnboardingSession
	err := r.db.WithContext(ctx).
		Where("flow_id = ? AND user_id = ?", strings.TrimSpace(flowID), strings.TrimSpace(userID)).
		First(&session).Error
	if err == nil {
		return session, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.OnboardingSession{}, false, nil
	}
	return store.OnboardingSession{}, false, err
}
