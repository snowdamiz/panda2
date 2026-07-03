package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

func (s *Service) ManageServerSetup(ctx context.Context, request toolsvc.ServerSetupManagementRequest) (any, error) {
	switch strings.ToLower(strings.TrimSpace(request.Action)) {
	case "list":
		templates, err := s.Catalog(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"templates": templateSummaries(templates),
		}, nil
	case "export":
		template, err := s.template(ctx, setupRequestTemplateID(request.TemplateID, request.Text))
		if err != nil {
			return nil, err
		}
		return map[string]any{"template": template}, nil
	case "import":
		template, err := s.importTemplate(ctx, request.TemplateJSON, request.ActorID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"template": templateSummaries([]Template{template})[0]}, nil
	case "preview":
		project, preview, err := s.Preview(ctx, SetupRequest{
			GuildID:    request.GuildID,
			ActorID:    request.ActorID,
			ChannelID:  request.ChannelID,
			TemplateID: setupRequestTemplateID(request.TemplateID, request.Text),
			Variables:  setupRequestVariables(request.Variables, request.Text),
			DryRun:     true,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"project": projectPayload(project),
			"preview": preview,
		}, nil
	case "status":
		project, ok, err := s.projectForRequest(ctx, request.GuildID, request.ProjectID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, repository.ErrNotFound
		}
		return map[string]any{
			"project":  projectPayload(project),
			"preview":  decodeJSONObject(project.PreviewJSON),
			"progress": decodeJSONObject(project.ProgressJSON),
			"recovery": decodeJSONObject(project.RecoveryJSON),
		}, nil
	case "apply":
		project, preview, err := s.projectForApplyRequest(ctx, request)
		if err != nil {
			return nil, err
		}
		result := map[string]any{
			"project":                projectPayload(project),
			"preview":                preview,
			"confirmation_preview":   map[string]any{"project_id": project.ID},
			"confirmation_arguments": map[string]any{"project_id": project.ID},
		}
		if preview.Blocked {
			result["blocked"] = true
			result["message"] = "Setup preview is blocked. Resolve the preview warnings before applying."
			return result, nil
		}
		result["confirmation_required"] = true
		result["action"] = "server_setup.apply"
		result["message"] = "Confirm to apply this server setup in a background job."
		return result, nil
	case "rollback":
		project, ok, err := s.projectForRequest(ctx, request.GuildID, request.ProjectID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, repository.ErrNotFound
		}
		preview := map[string]any{"project_id": project.ID}
		result := map[string]any{
			"project":                projectPayload(project),
			"confirmation_preview":   preview,
			"confirmation_arguments": preview,
			"message":                "Confirm to roll back resources Panda created for this setup project.",
		}
		if request.DryRun {
			result["dry_run"] = true
			result["rollback_preview"] = rollbackPreview(project)
			return result, nil
		}
		result["confirmation_required"] = true
		result["action"] = "server_setup.rollback"
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported setup action %q", request.Action)
	}
}

func (s *Service) ManageTicket(ctx context.Context, request toolsvc.TicketManagementRequest) (any, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("ticketing is not configured for this runtime")
	}
	switch strings.ToLower(strings.TrimSpace(request.Action)) {
	case "list", "":
		tickets, err := s.repo.ListTickets(ctx, request.GuildID, "", request.Limit)
		if err != nil {
			return nil, err
		}
		if !request.IncludeClosed {
			tickets = filterActiveTickets(tickets)
		}
		return map[string]any{"tickets": ticketPayloads(tickets)}, nil
	case "show":
		ticket, ok, err := s.repo.GetTicket(ctx, request.TicketID)
		if err != nil {
			return nil, err
		}
		if !ok || ticket.GuildID != strings.TrimSpace(request.GuildID) {
			return nil, repository.ErrNotFound
		}
		return map[string]any{"ticket": ticketPayload(ticket)}, nil
	case "claim":
		if request.DryRun {
			return map[string]any{"dry_run": true, "ticket_id": request.TicketID, "action": "claim"}, nil
		}
		return s.updateTicket(ctx, request, TicketStatusClaimed, map[string]any{"assignee_user_id": request.ActorID}, "ticket.claimed")
	case "close":
		if request.DryRun {
			return map[string]any{"dry_run": true, "ticket_id": request.TicketID, "action": "close", "reason": request.Reason}, nil
		}
		updates := map[string]any{
			"status":       TicketStatusClosed,
			"close_reason": strings.TrimSpace(request.Reason),
			"closed_at":    s.now().UTC(),
		}
		if ticket, ok, err := s.repo.GetTicket(ctx, request.TicketID); err != nil {
			return nil, err
		} else if !ok || ticket.GuildID != strings.TrimSpace(request.GuildID) {
			return nil, repository.ErrNotFound
		} else if s.adapter != nil && ticket.ChannelID != "" {
			if transcript, err := s.adapter.ExportTranscript(ctx, ticket.ChannelID, 200); err == nil {
				updates["transcript_json"] = mustJSON(transcript)
			}
		}
		return s.updateTicket(ctx, request, TicketStatusClosed, updates, "ticket.closed")
	case "reopen":
		if request.DryRun {
			return map[string]any{"dry_run": true, "ticket_id": request.TicketID, "action": "reopen"}, nil
		}
		return s.updateTicket(ctx, request, TicketStatusReopened, map[string]any{"status": TicketStatusReopened, "closed_at": nil, "close_reason": ""}, "ticket.reopened")
	case "archive":
		if request.DryRun {
			return map[string]any{"dry_run": true, "ticket_id": request.TicketID, "action": "archive"}, nil
		}
		return s.updateTicket(ctx, request, TicketStatusArchived, map[string]any{"status": TicketStatusArchived}, "ticket.archived")
	case "add_participant", "remove_participant":
		return s.updateTicketParticipant(ctx, request)
	default:
		return nil, fmt.Errorf("unsupported ticket action %q", request.Action)
	}
}

func (s *Service) ManageOnboarding(ctx context.Context, request toolsvc.OnboardingManagementRequest) (any, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("onboarding is not configured for this runtime")
	}
	switch strings.ToLower(strings.TrimSpace(request.Action)) {
	case "list", "":
		flows, err := s.repo.ListOnboardingFlows(ctx, request.GuildID, true)
		if err != nil {
			return nil, err
		}
		return map[string]any{"flows": onboardingFlowPayloads(flows)}, nil
	case "pause", "resume":
		flow, ok, err := s.repo.GetOnboardingFlow(ctx, request.FlowID)
		if err != nil {
			return nil, err
		}
		if !ok || flow.GuildID != strings.TrimSpace(request.GuildID) {
			return nil, repository.ErrNotFound
		}
		paused := request.Action == "pause"
		if request.DryRun {
			return map[string]any{"dry_run": true, "flow_id": flow.ID, "paused": paused}, nil
		}
		flow.Paused = paused
		flow.Enabled = true
		updated, err := s.repo.UpsertOnboardingFlow(ctx, flow)
		if err != nil {
			return nil, err
		}
		s.recordAudit(ctx, updated.GuildID, request.ActorID, "onboarding."+request.Action, "onboarding_flow", updated.ID, map[string]any{"paused": paused})
		return map[string]any{"flow": onboardingFlowPayload(updated)}, nil
	case "complete":
		if strings.TrimSpace(request.UserID) == "" {
			return nil, errors.New("user_id is required")
		}
		if request.DryRun {
			return map[string]any{"dry_run": true, "flow_id": request.FlowID, "user_id": request.UserID, "action": "complete"}, nil
		}
		response, err := s.CompleteOnboarding(ctx, ComponentRequest{
			CustomID: OnboardingAcknowledgeCustomID(request.FlowID),
			GuildID:  request.GuildID,
			UserID:   request.UserID,
			IsAdmin:  true,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"response": response}, nil
	default:
		return nil, fmt.Errorf("unsupported onboarding action %q", request.Action)
	}
}

func (s *Service) projectForApplyRequest(ctx context.Context, request toolsvc.ServerSetupManagementRequest) (store.GuildSetupProject, Preview, error) {
	if strings.TrimSpace(request.ProjectID) != "" {
		project, ok, err := s.repo.GetProject(ctx, request.ProjectID)
		if err != nil {
			return store.GuildSetupProject{}, Preview{}, err
		}
		if !ok || project.GuildID != strings.TrimSpace(request.GuildID) {
			return store.GuildSetupProject{}, Preview{}, repository.ErrNotFound
		}
		return project, previewFromProject(project), nil
	}
	project, preview, err := s.Preview(ctx, SetupRequest{
		GuildID:    request.GuildID,
		ActorID:    request.ActorID,
		ChannelID:  request.ChannelID,
		TemplateID: setupRequestTemplateID(request.TemplateID, request.Text),
		Variables:  setupRequestVariables(request.Variables, request.Text),
		DryRun:     true,
	})
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	return project, preview, nil
}

func (s *Service) importTemplate(ctx context.Context, rawJSON, actorID string) (Template, error) {
	if s == nil || s.repo == nil {
		return Template{}, errors.New("setup templates are not configured for this runtime")
	}
	rawJSON = strings.TrimSpace(rawJSON)
	if rawJSON == "" {
		return Template{}, errors.New("template_json is required")
	}
	var template Template
	if err := json.Unmarshal([]byte(rawJSON), &template); err != nil {
		return Template{}, err
	}
	if template.SchemaVersion == 0 {
		template.SchemaVersion = SchemaVersion
	}
	if template.TemplateVersion == 0 {
		template.TemplateVersion = 1
	}
	if template.DefaultVariables == nil {
		template.DefaultVariables = map[string]string{}
	}
	template.ReleaseState = firstNonEmpty(template.ReleaseState, "draft")
	if _, builtIn := BuiltInTemplateByID(template.ID); builtIn {
		return Template{}, fmt.Errorf("%w: built-in template ids cannot be overwritten", ErrInvalidTemplate)
	}
	if err := s.planner.ValidateTemplate(template); err != nil {
		return Template{}, err
	}
	templateJSON, err := json.Marshal(template)
	if err != nil {
		return Template{}, err
	}
	defaultVariables, err := json.Marshal(template.DefaultVariables)
	if err != nil {
		return Template{}, err
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
		BuiltIn:          false,
		CreatedBy:        strings.TrimSpace(actorID),
	}); err != nil {
		return Template{}, err
	}
	s.recordAudit(ctx, "", actorID, "setup.template_imported", "setup_template", template.ID, map[string]any{"template_version": template.TemplateVersion})
	return template, nil
}

func setupRequestTemplateID(templateID, text string) string {
	if strings.TrimSpace(templateID) != "" {
		return strings.TrimSpace(templateID)
	}
	id, _ := setupIntentTemplate(text)
	return id
}

func setupRequestVariables(input map[string]string, text string) map[string]string {
	variables := map[string]string{}
	for key, value := range input {
		variables[key] = value
	}
	if strings.TrimSpace(text) == "" {
		return variables
	}
	_, purpose := setupIntentTemplate(text)
	if purpose != "" && strings.TrimSpace(variables["server_purpose"]) == "" {
		variables["server_purpose"] = purpose
	}
	return variables
}

func setupIntentTemplate(text string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(normalized, "support") || strings.Contains(normalized, "ticket") || strings.Contains(normalized, "help desk") || strings.Contains(normalized, "helpdesk"):
		return "support_desk", "a support desk"
	case strings.Contains(normalized, "creator") || strings.Contains(normalized, "stream") || strings.Contains(normalized, "content") || strings.Contains(normalized, "fan"):
		return "creator_hub", "a creator community"
	case strings.Contains(normalized, "game") || strings.Contains(normalized, "gaming") || strings.Contains(normalized, "lfg") || strings.Contains(normalized, "voice lobby"):
		return "gaming_server", "a gaming group"
	case strings.Contains(normalized, "product") || strings.Contains(normalized, "saas") || strings.Contains(normalized, "customer") || strings.Contains(normalized, "beta"):
		return "saas_product_community", "a product community"
	case strings.Contains(normalized, "study") || strings.Contains(normalized, "course") || strings.Contains(normalized, "class") || strings.Contains(normalized, "student"):
		return "study_course", "a course or study group"
	default:
		return "minimal_community", "a friendly community"
	}
}

func (s *Service) projectForRequest(ctx context.Context, guildID, projectID string) (store.GuildSetupProject, bool, error) {
	if strings.TrimSpace(projectID) != "" {
		project, ok, err := s.repo.GetProject(ctx, projectID)
		if err != nil || !ok {
			return project, ok, err
		}
		if project.GuildID != strings.TrimSpace(guildID) {
			return store.GuildSetupProject{}, false, nil
		}
		return project, true, nil
	}
	return s.repo.GetLatestProjectForGuild(ctx, guildID)
}

func (s *Service) updateTicket(ctx context.Context, request toolsvc.TicketManagementRequest, status string, updates map[string]any, eventType string) (any, error) {
	ticket, ok, err := s.repo.GetTicket(ctx, request.TicketID)
	if err != nil {
		return nil, err
	}
	if !ok || ticket.GuildID != strings.TrimSpace(request.GuildID) {
		return nil, repository.ErrNotFound
	}
	if strings.TrimSpace(status) != "" {
		updates["status"] = status
	}
	updated, err := s.repo.UpdateTicketWithEvent(ctx, ticket.ID, updates, store.TicketEvent{
		ActorID:   request.ActorID,
		EventType: eventType,
		MetadataJSON: mustJSON(map[string]any{
			"reason":   strings.TrimSpace(request.Reason),
			"priority": strings.TrimSpace(request.Priority),
			"tags":     request.Tags,
			"user_id":  strings.TrimSpace(request.UserID),
		}),
	})
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, updated.GuildID, request.ActorID, eventType, "ticket", updated.ID, map[string]any{"status": updated.Status})
	return map[string]any{"ticket": ticketPayload(updated)}, nil
}

func (s *Service) updateTicketParticipant(ctx context.Context, request toolsvc.TicketManagementRequest) (any, error) {
	if strings.TrimSpace(request.UserID) == "" {
		return nil, errors.New("user_id is required")
	}
	ticket, ok, err := s.repo.GetTicket(ctx, request.TicketID)
	if err != nil {
		return nil, err
	}
	if !ok || ticket.GuildID != strings.TrimSpace(request.GuildID) {
		return nil, repository.ErrNotFound
	}
	if ticket.Status == TicketStatusClosed || ticket.Status == TicketStatusArchived {
		return nil, errors.New("ticket must be open before participants can be changed")
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if request.DryRun {
		return map[string]any{"dry_run": true, "ticket_id": ticket.ID, "action": action, "user_id": request.UserID}, nil
	}
	if s.adapter == nil {
		return nil, errors.New("ticket participant updates require a Discord adapter")
	}
	if action == "add_participant" {
		if err := s.adapter.AddTicketParticipant(ctx, ticket.GuildID, ticket.ChannelID, request.UserID, "Panda ticket participant added"); err != nil {
			return nil, err
		}
	} else {
		if err := s.adapter.RemoveTicketParticipant(ctx, ticket.GuildID, ticket.ChannelID, request.UserID, "Panda ticket participant removed"); err != nil {
			return nil, err
		}
	}
	eventType := "ticket.participant_added"
	if action == "remove_participant" {
		eventType = "ticket.participant_removed"
	}
	updated, err := s.repo.UpdateTicketWithEvent(ctx, ticket.ID, map[string]any{}, store.TicketEvent{
		ActorID:   request.ActorID,
		EventType: eventType,
		MetadataJSON: mustJSON(map[string]any{
			"user_id": strings.TrimSpace(request.UserID),
			"reason":  strings.TrimSpace(request.Reason),
		}),
	})
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, updated.GuildID, request.ActorID, eventType, "ticket", updated.ID, map[string]any{"user_id": strings.TrimSpace(request.UserID)})
	return map[string]any{"ticket": ticketPayload(updated)}, nil
}

func templateSummaries(templates []Template) []map[string]any {
	result := make([]map[string]any, 0, len(templates))
	for _, template := range templates {
		result = append(result, map[string]any{
			"id":                 template.ID,
			"name":               template.Name,
			"description":        template.Description,
			"release_state":      template.ReleaseState,
			"schema_version":     template.SchemaVersion,
			"template_version":   template.TemplateVersion,
			"default_variables":  template.DefaultVariables,
			"editable_variables": template.EditableVariables,
			"feature_ids":        template.FeatureIDs,
		})
	}
	return result
}

func projectPayload(project store.GuildSetupProject) map[string]any {
	return map[string]any{
		"id":                    project.ID,
		"guild_id":              project.GuildID,
		"template_id":           project.TemplateID,
		"template_version":      project.TemplateVersion,
		"schema_version":        project.SchemaVersion,
		"status":                project.Status,
		"actor_id":              project.ActorID,
		"source_install_intent": project.SourceInstallIntent,
		"current_step":          project.CurrentStep,
		"last_error":            project.LastError,
		"created_at":            formatTime(project.CreatedAt),
		"confirmed_at":          formatTimePtr(project.ConfirmedAt),
		"started_at":            formatTimePtr(project.StartedAt),
		"finished_at":           formatTimePtr(project.FinishedAt),
	}
}

func previewFromProject(project store.GuildSetupProject) Preview {
	var preview Preview
	_ = json.Unmarshal([]byte(project.PreviewJSON), &preview)
	return preview
}

func rollbackPreview(project store.GuildSetupProject) map[string]any {
	steps, err := projectPlan(project)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	deletable := []map[string]string{}
	kept := []map[string]string{}
	for _, step := range steps {
		item := map[string]string{"alias": step.Alias, "type": step.ResourceType, "name": step.Name}
		if step.Action == PlanActionCreate {
			switch step.ResourceType {
			case ResourceTypeRole, ResourceTypeCategory, ResourceTypeChannel, ResourceTypeTicketPanel, ResourceTypeOnboardingFlow:
				deletable = append(deletable, item)
			default:
				kept = append(kept, item)
			}
			continue
		}
		kept = append(kept, item)
	}
	return map[string]any{"deletable_or_disabled": deletable, "kept": kept}
}

func ticketPayloads(tickets []store.Ticket) []map[string]any {
	result := make([]map[string]any, 0, len(tickets))
	for _, ticket := range tickets {
		result = append(result, ticketPayload(ticket))
	}
	return result
}

func ticketPayload(ticket store.Ticket) map[string]any {
	return map[string]any{
		"id":                ticket.ID,
		"guild_id":          ticket.GuildID,
		"panel_id":          ticket.PanelID,
		"department_id":     ticket.DepartmentID,
		"requester_user_id": ticket.RequesterUserID,
		"channel_id":        ticket.ChannelID,
		"status":            ticket.Status,
		"assignee_user_id":  ticket.AssigneeUserID,
		"priority":          ticket.Priority,
		"tags":              decodeJSONArray(ticket.TagsJSON),
		"close_reason":      ticket.CloseReason,
		"opened_at":         formatTime(ticket.OpenedAt),
		"closed_at":         formatTimePtr(ticket.ClosedAt),
		"updated_at":        formatTime(ticket.UpdatedAt),
	}
}

func filterActiveTickets(tickets []store.Ticket) []store.Ticket {
	result := tickets[:0]
	for _, ticket := range tickets {
		switch ticket.Status {
		case TicketStatusClosed, TicketStatusArchived:
			continue
		default:
			result = append(result, ticket)
		}
	}
	return result
}

func onboardingFlowPayloads(flows []store.OnboardingFlow) []map[string]any {
	result := make([]map[string]any, 0, len(flows))
	for _, flow := range flows {
		result = append(result, onboardingFlowPayload(flow))
	}
	return result
}

func onboardingFlowPayload(flow store.OnboardingFlow) map[string]any {
	return map[string]any{
		"id":                 flow.ID,
		"guild_id":           flow.GuildID,
		"project_id":         flow.ProjectID,
		"managed_alias":      flow.ManagedAlias,
		"welcome_channel_id": flow.WelcomeChannelID,
		"rules_channel_id":   flow.RulesChannelID,
		"verified_role_id":   flow.VerifiedRoleID,
		"newcomer_role_id":   flow.NewcomerRoleID,
		"verification_mode":  flow.VerificationMode,
		"enabled":            flow.Enabled,
		"paused":             flow.Paused,
		"steps":              decodeJSONArray(flow.StepsJSON),
		"created_at":         formatTime(flow.CreatedAt),
		"updated_at":         formatTime(flow.UpdatedAt),
	}
}

func decodeJSONObject(raw string) map[string]any {
	result := map[string]any{}
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &result)
	return result
}

func decodeJSONArray(raw string) []any {
	result := []any{}
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &result)
	return result
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTime(*value)
}
