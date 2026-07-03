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
)

const (
	defaultWizardTemplateID = "minimal_community"

	wizardStepTemplate  = "template"
	wizardStepScratch   = "scratch"
	wizardStepCustomize = "customize"
	wizardStepPreview   = "preview"
	wizardStepQueued    = "queued"
	wizardStepCancelled = "cancelled"

	wizardActionPreview     = "preview"
	wizardActionApply       = "apply"
	wizardActionBack        = "back"
	wizardActionCancel      = "cancel"
	wizardActionScratch     = "scratch"
	wizardActionEditPurpose = "edit_purpose"
	wizardActionEditRoles   = "edit_roles"
	wizardActionEditCopy    = "edit_copy"
	wizardActionEditTickets = "edit_tickets"

	wizardModalPurpose = "purpose"
	wizardModalRoles   = "roles"
	wizardModalCopy    = "copy"
	wizardModalTickets = "tickets"
)

func (s *Service) StartSetupWizard(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	if err := s.requireWizardAdmin(request); err != nil {
		return ComponentResponse{}, err
	}
	if err := s.SyncBuiltInTemplates(ctx); err != nil {
		return ComponentResponse{}, err
	}
	template, err := s.template(ctx, defaultWizardTemplateID)
	if err != nil {
		return ComponentResponse{}, err
	}
	projectID, err := randomID("sw")
	if err != nil {
		return ComponentResponse{}, err
	}
	variablesJSON, _ := json.Marshal(template.DefaultVariables)
	project, err := s.repo.CreateProject(ctx, store.GuildSetupProject{
		ID:              projectID,
		GuildID:         strings.TrimSpace(request.GuildID),
		TemplateID:      template.ID,
		TemplateVersion: template.TemplateVersion,
		SchemaVersion:   template.SchemaVersion,
		VariablesJSON:   string(variablesJSON),
		PreviewJSON:     "{}",
		ApplyPlanJSON:   "[]",
		Status:          ProjectStatusDraft,
		ActorID:         strings.TrimSpace(request.UserID),
		CurrentStep:     wizardStepTemplate,
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	s.recordAudit(ctx, project.GuildID, project.ActorID, "setup.wizard_started", "setup_project", project.ID, map[string]any{
		"template_id": project.TemplateID,
	})
	return s.renderWizardTemplateStep(ctx, project, "")
}

func WizardActionOpensModal(action string) bool {
	switch strings.TrimSpace(action) {
	case wizardActionEditPurpose, wizardActionEditRoles, wizardActionEditCopy, wizardActionEditTickets:
		return true
	default:
		return false
	}
}

func (s *Service) HandleSetupWizardTemplateSelection(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	projectID, ok := ParseWizardTemplateSelectCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid setup wizard template selection")
	}
	project, err := s.wizardProject(ctx, request, projectID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if response, done := s.renderLockedWizardProject(ctx, project); done {
		return response, nil
	}
	templateID := firstSelectedValue(request.Values)
	if templateID == "" {
		return ComponentResponse{}, errors.New("setup wizard template selection is empty")
	}
	template, err := s.template(ctx, templateID)
	if err != nil {
		return ComponentResponse{}, err
	}
	variables := mergeWizardTemplateVariables(template, wizardVariables(project))
	variablesJSON, _ := json.Marshal(variables)
	project, err = s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"template_id":       template.ID,
		"template_version":  template.TemplateVersion,
		"schema_version":    template.SchemaVersion,
		"variables_json":    string(variablesJSON),
		"preview_json":      "{}",
		"apply_plan_json":   "[]",
		"status":            ProjectStatusDraft,
		"current_step":      wizardStepCustomize,
		"failed_steps_json": "[]",
		"last_error":        "",
	})
	if err != nil {
		return ComponentResponse{}, err
	}
	return s.renderWizardCustomizeStep(ctx, project, "Template set to "+template.Name+".")
}

func (s *Service) HandleSetupWizardVerificationSelection(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	projectID, ok := ParseWizardVerificationSelectCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid setup wizard verification selection")
	}
	project, err := s.wizardProject(ctx, request, projectID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if response, done := s.renderLockedWizardProject(ctx, project); done {
		return response, nil
	}
	value := firstSelectedValue(request.Values)
	switch value {
	case "rules", "role_selection", "rules_and_roles":
	default:
		return ComponentResponse{}, errors.New("unsupported verification mode")
	}
	variables := wizardVariables(project)
	variables["verification_strictness"] = value
	project, err = s.updateWizardDraftVariables(ctx, project, variables, wizardStepCustomize)
	if err != nil {
		return ComponentResponse{}, err
	}
	return s.renderWizardCustomizeStep(ctx, project, "Verification mode set to "+wizardVerificationLabel(value)+".")
}

func (s *Service) HandleSetupWizardScratchSelection(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	projectID, field, ok := ParseWizardScratchSelectCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid setup wizard scratch selection")
	}
	project, err := s.wizardProject(ctx, request, projectID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if response, done := s.renderLockedWizardProject(ctx, project); done {
		return response, nil
	}
	variables := wizardVariables(project)
	if variables[wizardModeKey] != wizardModeScratch {
		variables = scratchDefaultVariables(scratchIntentCommunity)
	}
	notice := "Suggestions updated."
	switch field {
	case scratchIntentKey:
		intent := firstSelectedValue(request.Values)
		if intent == "" {
			intent = scratchIntentCommunity
		}
		variables[scratchIntentKey] = intent
		variables[scratchModulesKey] = strings.Join(scratchDefaultModules(intent), ",")
		if variables[scratchPurposeCustomKey] != "1" {
			variables["server_purpose"] = scratchPurpose(intent)
		}
		notice = "Panda refreshed suggestions for " + wizardScratchIntentLabel(intent) + "."
	case scratchModulesKey:
		variables[scratchModulesKey] = strings.Join(request.Values, ",")
	default:
		return ComponentResponse{}, errors.New("unsupported setup wizard scratch selection")
	}
	project, err = s.materializeScratchProject(ctx, project, variables, wizardStepScratch)
	if err != nil {
		return ComponentResponse{}, err
	}
	return s.renderWizardScratchStep(ctx, project, notice)
}

func (s *Service) HandleSetupWizardAction(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	projectID, action, ok := ParseWizardActionCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid setup wizard action")
	}
	project, err := s.wizardProject(ctx, request, projectID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if response, done := s.renderLockedWizardProject(ctx, project); done {
		return response, nil
	}
	switch action {
	case wizardActionScratch:
		project, err := s.startScratchWizard(ctx, project)
		if err != nil {
			return ComponentResponse{}, err
		}
		return s.renderWizardScratchStep(ctx, project, "Panda selected a practical starter set. Keep what fits, remove what does not.")
	case wizardActionEditPurpose:
		return s.wizardModalResponse(ctx, project, wizardModalPurpose)
	case wizardActionEditRoles:
		return s.wizardModalResponse(ctx, project, wizardModalRoles)
	case wizardActionEditCopy:
		return s.wizardModalResponse(ctx, project, wizardModalCopy)
	case wizardActionEditTickets:
		return s.wizardModalResponse(ctx, project, wizardModalTickets)
	case wizardActionPreview:
		if wizardIsScratchProject(project) {
			project, err = s.materializeScratchProject(ctx, project, wizardVariables(project), wizardStepScratch)
			if err != nil {
				return ComponentResponse{}, err
			}
		}
		project, preview, err := s.buildWizardPreview(ctx, project)
		if err != nil {
			return ComponentResponse{}, err
		}
		return s.renderWizardPreviewStep(ctx, project, preview, "")
	case wizardActionBack:
		if wizardIsScratchProject(project) {
			project, err := s.repo.UpdateProject(ctx, project.ID, map[string]any{
				"status":       ProjectStatusDraft,
				"current_step": wizardStepScratch,
			})
			if err != nil {
				return ComponentResponse{}, err
			}
			return s.renderWizardScratchStep(ctx, project, "")
		}
		project, err := s.repo.UpdateProject(ctx, project.ID, map[string]any{
			"status":       ProjectStatusDraft,
			"current_step": wizardStepCustomize,
		})
		if err != nil {
			return ComponentResponse{}, err
		}
		return s.renderWizardCustomizeStep(ctx, project, "")
	case wizardActionApply:
		project, preview, err := s.ensureWizardPreview(ctx, project)
		if err != nil {
			return ComponentResponse{}, err
		}
		if preview.Blocked {
			return s.renderWizardPreviewStep(ctx, project, preview, "Resolve the warnings before applying.")
		}
		confirmed, err := s.Confirm(ctx, project.ID, request.UserID, true)
		if err != nil {
			return ComponentResponse{}, err
		}
		return s.renderWizardAppliedStep(confirmed), nil
	case wizardActionCancel:
		cancelled, err := s.repo.UpdateProject(ctx, project.ID, map[string]any{
			"status":       ProjectStatusCancelled,
			"current_step": wizardStepCancelled,
			"finished_at":  s.now().UTC(),
		})
		if err != nil {
			return ComponentResponse{}, err
		}
		s.recordAudit(ctx, cancelled.GuildID, cancelled.ActorID, "setup.wizard_cancelled", "setup_project", cancelled.ID, nil)
		return ComponentResponse{
			Content:   "**Server setup wizard**\nSetup wizard cancelled. No server changes were made.",
			Title:     "Setup wizard cancelled",
			Accent:    "warning",
			Ephemeral: false,
		}, nil
	default:
		return ComponentResponse{Content: "That setup wizard action is no longer supported.", Ephemeral: true, Title: "Setup action expired", Accent: "warning"}, nil
	}
}

func (s *Service) HandleSetupWizardModal(ctx context.Context, request ComponentRequest) (ComponentResponse, error) {
	projectID, group, ok := ParseWizardModalCustomID(request.CustomID)
	if !ok {
		return ComponentResponse{}, errors.New("invalid setup wizard modal")
	}
	project, err := s.wizardProject(ctx, request, projectID)
	if err != nil {
		return ComponentResponse{}, err
	}
	if response, done := s.renderLockedWizardProject(ctx, project); done {
		return response, nil
	}
	variables := wizardVariables(project)
	changed := 0
	for key, value := range request.Fields {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		variables[key] = value
		if key == "server_purpose" && wizardIsScratchProject(project) {
			variables[scratchPurposeCustomKey] = "1"
		}
		changed++
	}
	if changed == 0 {
		if wizardIsScratchProject(project) {
			return s.renderWizardScratchStep(ctx, project, "No setup details changed.")
		}
		return s.renderWizardCustomizeStep(ctx, project, "No setup details changed.")
	}
	if wizardIsScratchProject(project) {
		project, err = s.materializeScratchProject(ctx, project, variables, wizardStepScratch)
		if err != nil {
			return ComponentResponse{}, err
		}
		return s.renderWizardScratchStep(ctx, project, wizardModalGroupLabel(group)+" saved.")
	}
	project, err = s.updateWizardDraftVariables(ctx, project, variables, wizardStepCustomize)
	if err != nil {
		return ComponentResponse{}, err
	}
	return s.renderWizardCustomizeStep(ctx, project, wizardModalGroupLabel(group)+" saved.")
}

func (s *Service) requireWizardAdmin(request ComponentRequest) error {
	if s == nil || s.repo == nil {
		return errors.New("setup service is not configured")
	}
	if strings.TrimSpace(request.GuildID) == "" {
		return errors.New("setup wizard must run inside a Discord server")
	}
	if strings.TrimSpace(request.UserID) == "" {
		return errors.New("setup wizard requires a Discord user")
	}
	if !request.IsAdmin {
		return errors.New("setup wizard requires a server admin")
	}
	return nil
}

func (s *Service) wizardProject(ctx context.Context, request ComponentRequest, projectID string) (store.GuildSetupProject, error) {
	if err := s.requireWizardAdmin(request); err != nil {
		return store.GuildSetupProject{}, err
	}
	project, ok, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		return store.GuildSetupProject{}, err
	}
	if !ok {
		return store.GuildSetupProject{}, repository.ErrNotFound
	}
	if project.GuildID != strings.TrimSpace(request.GuildID) {
		return store.GuildSetupProject{}, errors.New("setup wizard belongs to another server")
	}
	if project.ActorID != "" && project.ActorID != strings.TrimSpace(request.UserID) {
		return store.GuildSetupProject{}, errors.New("setup wizard can only be changed by the admin who started it")
	}
	return project, nil
}

func (s *Service) renderLockedWizardProject(ctx context.Context, project store.GuildSetupProject) (ComponentResponse, bool) {
	switch project.Status {
	case ProjectStatusQueued, ProjectStatusConfirmed, ProjectStatusApplying, ProjectStatusSucceeded, ProjectStatusFailed, ProjectStatusRolledBack:
		return s.renderWizardAppliedStep(project), true
	case ProjectStatusCancelled:
		return ComponentResponse{
			Content:   "**Server setup wizard**\nThis setup wizard has already been cancelled. No server changes were made.",
			Title:     "Setup wizard cancelled",
			Accent:    "warning",
			Ephemeral: false,
		}, true
	default:
		return ComponentResponse{}, false
	}
}

func (s *Service) updateWizardDraftVariables(ctx context.Context, project store.GuildSetupProject, variables map[string]string, currentStep string) (store.GuildSetupProject, error) {
	variablesJSON, _ := json.Marshal(variables)
	return s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"variables_json":    string(variablesJSON),
		"preview_json":      "{}",
		"apply_plan_json":   "[]",
		"status":            ProjectStatusDraft,
		"current_step":      currentStep,
		"failed_steps_json": "[]",
		"last_error":        "",
	})
}

func (s *Service) renderWizardTemplateStep(ctx context.Context, project store.GuildSetupProject, notice string) (ComponentResponse, error) {
	template, err := s.template(ctx, project.TemplateID)
	if err != nil {
		return ComponentResponse{}, err
	}
	content := wizardBaseContent(project, "Step 1 of 4: choose a starting point.", notice)
	content.WriteString("\nStart from scratch and let Panda suggest the usual roles, channels, onboarding, tickets, and staff areas, or choose a preset to move faster.")
	content.WriteString("\n\nPreset shown: **")
	content.WriteString(template.Name)
	content.WriteString("**\n")
	content.WriteString(template.Description)
	return ComponentResponse{
		Content: content.String(),
		Title:   "Server setup wizard",
		Accent:  "info",
		Components: []MessageComponent{
			s.wizardTemplateSelect(ctx, project.ID, project.TemplateID),
			{Type: "button", Label: "Start from scratch", CustomID: WizardActionCustomID(project.ID, wizardActionScratch), Style: "primary"},
			{Type: "button", Label: "Cancel", CustomID: WizardActionCustomID(project.ID, wizardActionCancel), Style: "secondary"},
		},
	}, nil
}

func (s *Service) renderWizardScratchStep(ctx context.Context, project store.GuildSetupProject, notice string) (ComponentResponse, error) {
	project, err := s.materializeScratchProject(ctx, project, wizardVariables(project), wizardStepScratch)
	if err != nil {
		return ComponentResponse{}, err
	}
	variables := wizardVariables(project)
	template, err := s.template(ctx, project.TemplateID)
	if err != nil {
		return ComponentResponse{}, err
	}
	content := wizardBaseContent(project, "Step 2 of 4: build from scratch with Panda suggestions.", notice)
	content.WriteString("\nPanda will always account for core setup: admin/mod/member roles, rules, welcome, general chat, and Panda access controls.")
	content.WriteString("\n\nServer type: **")
	content.WriteString(wizardScratchIntentLabel(variables[scratchIntentKey]))
	content.WriteString("**")
	content.WriteString("\nSuggested additions: ")
	content.WriteString(wizardScratchModuleSummary(variables[scratchModulesKey]))
	content.WriteString("\n\nCurrent plan: ")
	content.WriteString(fmt.Sprintf("%d roles, %d categories, %d channels", len(template.Roles), len(template.Categories), len(template.Channels)))
	if len(template.TicketPanels) > 0 {
		content.WriteString(fmt.Sprintf(", %d ticket panel", len(template.TicketPanels)))
	}
	if len(template.OnboardingFlows) > 0 {
		content.WriteString(fmt.Sprintf(", %d onboarding flow", len(template.OnboardingFlows)))
	}
	content.WriteString(".")
	components := []MessageComponent{
		wizardScratchIntentSelect(project.ID, variables[scratchIntentKey]),
		wizardScratchModulesSelect(project.ID, variables[scratchModulesKey]),
		{Type: "button", Label: "Purpose", CustomID: WizardActionCustomID(project.ID, wizardActionEditPurpose), Style: "secondary"},
		{Type: "button", Label: "Roles", CustomID: WizardActionCustomID(project.ID, wizardActionEditRoles), Style: "secondary"},
		{Type: "button", Label: "Copy", CustomID: WizardActionCustomID(project.ID, wizardActionEditCopy), Style: "secondary"},
	}
	if scratchModuleSet(variables[scratchModulesKey])[scratchModuleSupport] {
		components = append(components, MessageComponent{Type: "button", Label: "Tickets", CustomID: WizardActionCustomID(project.ID, wizardActionEditTickets), Style: "secondary"})
	}
	components = append(components,
		MessageComponent{Type: "button", Label: "Preview setup", CustomID: WizardActionCustomID(project.ID, wizardActionPreview), Style: "primary"},
		MessageComponent{Type: "button", Label: "Cancel", CustomID: WizardActionCustomID(project.ID, wizardActionCancel), Style: "secondary"},
	)
	return ComponentResponse{
		Content:    content.String(),
		Title:      "Build setup from scratch",
		Accent:     "info",
		Components: components,
	}, nil
}

func (s *Service) renderWizardCustomizeStep(ctx context.Context, project store.GuildSetupProject, notice string) (ComponentResponse, error) {
	template, err := s.template(ctx, project.TemplateID)
	if err != nil {
		return ComponentResponse{}, err
	}
	variables := wizardVariables(project)
	content := wizardBaseContent(project, "Step 2 of 4: customize the setup details.", notice)
	content.WriteString("\nTemplate: **")
	content.WriteString(template.Name)
	content.WriteString("**\n")
	content.WriteString("Purpose: ")
	content.WriteString(firstNonEmpty(variables["server_purpose"], template.DefaultVariables["server_purpose"]))
	content.WriteString("\nRoles: ")
	content.WriteString(firstNonEmpty(variables["admin_role"], "Admin"))
	content.WriteString(", ")
	content.WriteString(firstNonEmpty(variables["moderator_role"], "Moderator"))
	content.WriteString(", ")
	content.WriteString(firstNonEmpty(variables["member_role"], "Member"))
	if len(template.OnboardingFlows) > 0 {
		content.WriteString("\nVerification: ")
		content.WriteString(wizardVerificationLabel(firstNonEmpty(variables["verification_strictness"], "rules")))
	}
	if len(template.TicketPanels) > 0 {
		content.WriteString("\nTicket panel: ")
		content.WriteString(firstNonEmpty(variables["ticket_panel_title"], "Need help?"))
	}
	content.WriteString("\n\nNext: preview exactly what Panda will create, update, reuse, or skip.")

	components := []MessageComponent{s.wizardTemplateSelect(ctx, project.ID, project.TemplateID)}
	if len(template.OnboardingFlows) > 0 {
		components = append(components, wizardVerificationSelect(project.ID))
	}
	components = append(components,
		MessageComponent{Type: "button", Label: "Purpose", CustomID: WizardActionCustomID(project.ID, wizardActionEditPurpose), Style: "secondary"},
		MessageComponent{Type: "button", Label: "Roles", CustomID: WizardActionCustomID(project.ID, wizardActionEditRoles), Style: "secondary"},
		MessageComponent{Type: "button", Label: "Copy", CustomID: WizardActionCustomID(project.ID, wizardActionEditCopy), Style: "secondary"},
	)
	if len(template.TicketPanels) > 0 {
		components = append(components, MessageComponent{Type: "button", Label: "Tickets", CustomID: WizardActionCustomID(project.ID, wizardActionEditTickets), Style: "secondary"})
	}
	components = append(components,
		MessageComponent{Type: "button", Label: "Preview setup", CustomID: WizardActionCustomID(project.ID, wizardActionPreview), Style: "primary"},
		MessageComponent{Type: "button", Label: "Cancel", CustomID: WizardActionCustomID(project.ID, wizardActionCancel), Style: "secondary"},
	)
	return ComponentResponse{
		Content:    content.String(),
		Title:      "Customize server setup",
		Accent:     "info",
		Components: components,
	}, nil
}

func (s *Service) renderWizardPreviewStep(ctx context.Context, project store.GuildSetupProject, preview Preview, notice string) (ComponentResponse, error) {
	template, err := s.template(ctx, project.TemplateID)
	if err != nil {
		return ComponentResponse{}, err
	}
	content := wizardBaseContent(project, "Step 3 of 4: review the setup plan.", notice)
	if wizardIsScratchProject(project) {
		content.WriteString("\nPlan: **")
	} else {
		content.WriteString("\nTemplate: **")
	}
	content.WriteString(template.Name)
	content.WriteString("**")
	content.WriteString("\nCreates: ")
	content.WriteString(fmt.Sprint(preview.Summary.Creates))
	content.WriteString("  Updates: ")
	content.WriteString(fmt.Sprint(preview.Summary.Updates))
	content.WriteString("  Reuses: ")
	content.WriteString(fmt.Sprint(preview.Summary.Reuses))
	content.WriteString("  Skips: ")
	content.WriteString(fmt.Sprint(preview.Summary.Skips))
	content.WriteString("\nIncludes: ")
	content.WriteString(fmt.Sprintf("%d roles, %d categories, %d channels", preview.Summary.Roles, preview.Summary.Categories, preview.Summary.Channels))
	if preview.Summary.TicketPanels > 0 {
		content.WriteString(fmt.Sprintf(", %d ticket panels", preview.Summary.TicketPanels))
	}
	if preview.Summary.OnboardingFlows > 0 {
		content.WriteString(fmt.Sprintf(", %d onboarding flows", preview.Summary.OnboardingFlows))
	}
	if preview.Summary.StarterMessages > 0 {
		content.WriteString(fmt.Sprintf(", %d starter messages", preview.Summary.StarterMessages))
	}
	if preview.Blocked {
		content.WriteString("\n\nBlocked warnings:")
	} else if len(preview.Warnings) > 0 {
		content.WriteString("\n\nWarnings:")
	}
	for index, warning := range preview.Warnings {
		if index == 3 {
			content.WriteString("\n- More warnings are saved on the project preview.")
			break
		}
		content.WriteString("\n- ")
		content.WriteString(warning)
	}
	if !preview.Blocked {
		content.WriteString("\n\nNext: apply the saved plan to this server.")
	}
	components := []MessageComponent{}
	if !preview.Blocked {
		components = append(components, MessageComponent{Type: "button", Label: "Apply setup", CustomID: WizardActionCustomID(project.ID, wizardActionApply), Style: "success"})
	}
	components = append(components,
		MessageComponent{Type: "button", Label: "Back", CustomID: WizardActionCustomID(project.ID, wizardActionBack), Style: "secondary"},
		MessageComponent{Type: "button", Label: "Cancel", CustomID: WizardActionCustomID(project.ID, wizardActionCancel), Style: "secondary"},
	)
	return ComponentResponse{
		Content:    content.String(),
		Title:      "Review setup plan",
		Accent:     firstNonEmpty(wizardPreviewAccent(preview), "info"),
		Components: components,
	}, nil
}

func (s *Service) renderWizardAppliedStep(project store.GuildSetupProject) ComponentResponse {
	statusLine := "Setup is confirmed and ready to apply."
	accent := "success"
	title := "Setup confirmed"
	switch project.Status {
	case ProjectStatusQueued:
		statusLine = "Step 4 of 4: setup is queued. Panda will apply the saved plan in the background."
		title = "Setup queued"
	case ProjectStatusApplying:
		statusLine = "Step 4 of 4: setup is being applied now."
		title = "Setup applying"
		accent = "info"
	case ProjectStatusSucceeded:
		statusLine = "Setup completed successfully."
		title = "Setup complete"
	case ProjectStatusFailed:
		statusLine = "Setup failed while applying. Check the saved project status for recovery details."
		title = "Setup failed"
		accent = "danger"
	case ProjectStatusRolledBack:
		statusLine = "Setup was rolled back."
		title = "Setup rolled back"
		accent = "warning"
	}
	return ComponentResponse{
		Content:   "**Server setup wizard**\n" + statusLine + "\nProject: `" + project.ID + "`",
		Title:     title,
		Accent:    accent,
		Ephemeral: false,
	}
}

func (s *Service) wizardModalResponse(ctx context.Context, project store.GuildSetupProject, group string) (ComponentResponse, error) {
	template, err := s.template(ctx, project.TemplateID)
	if err != nil {
		return ComponentResponse{}, err
	}
	fields := wizardModalFields(group, wizardVariables(project), template)
	if len(fields) == 0 {
		return ComponentResponse{Content: "That setup section is not available for this template.", Ephemeral: true, Title: "Setup section unavailable", Accent: "warning"}, nil
	}
	return ComponentResponse{
		Modal: &ComponentModal{
			ID:     WizardModalCustomID(project.ID, group),
			Title:  wizardModalGroupLabel(group),
			Fields: fields,
		},
	}, nil
}

func (s *Service) wizardTemplateSelect(ctx context.Context, projectID, selectedTemplateID string) MessageComponent {
	templates, err := s.wizardTemplates(ctx)
	options := []MessageComponentOption{}
	if err == nil {
		for _, template := range templates {
			label := template.Name
			if template.ID == selectedTemplateID {
				label += " (selected)"
			}
			options = append(options, MessageComponentOption{
				Label:       limitWizardComponentText(label, 100),
				Value:       template.ID,
				Description: limitWizardComponentText(template.Description, 100),
			})
		}
	}
	if len(options) == 0 {
		options = append(options, MessageComponentOption{Label: "Minimal Community", Value: defaultWizardTemplateID, Description: "Rules, announcements, chat, staff area, and onboarding."})
	}
	return MessageComponent{
		Type:        "select",
		CustomID:    WizardTemplateSelectCustomID(projectID),
		Placeholder: "Choose setup template",
		MinValues:   1,
		MaxValues:   1,
		Options:     options,
	}
}

func (s *Service) wizardTemplates(ctx context.Context) ([]Template, error) {
	if err := s.SyncBuiltInTemplates(ctx); err != nil {
		return nil, err
	}
	stored, err := s.repo.ListTemplates(ctx, false)
	if err != nil {
		return nil, err
	}
	templates := make([]Template, 0, len(stored))
	for _, item := range stored {
		if strings.HasPrefix(strings.TrimSpace(item.ID), "scratch_") {
			continue
		}
		var template Template
		if err := json.Unmarshal([]byte(item.TemplateJSON), &template); err != nil {
			return nil, err
		}
		if template.ID == "" {
			template.ID = item.ID
		}
		if template.Name == "" {
			template.Name = item.Name
		}
		if template.Description == "" {
			template.Description = item.Description
		}
		templates = append(templates, template)
		if len(templates) == 25 {
			break
		}
	}
	return templates, nil
}

func wizardVerificationSelect(projectID string) MessageComponent {
	return MessageComponent{
		Type:        "select",
		CustomID:    WizardVerificationSelectCustomID(projectID),
		Placeholder: "Choose verification mode",
		MinValues:   1,
		MaxValues:   1,
		Options: []MessageComponentOption{
			{Label: "Rules acknowledgement", Value: "rules", Description: "Members confirm the rules to complete onboarding."},
			{Label: "Role selection", Value: "role_selection", Description: "Members pick onboarding roles."},
			{Label: "Rules and roles", Value: "rules_and_roles", Description: "Members confirm rules and pick roles."},
		},
	}
}

func wizardScratchIntentSelect(projectID, selectedIntent string) MessageComponent {
	selectedIntent = firstNonEmpty(selectedIntent, scratchIntentCommunity)
	options := []MessageComponentOption{
		{Label: "Community", Value: scratchIntentCommunity, Description: "General-purpose server with chat, welcome, and moderation."},
		{Label: "Creator hub", Value: scratchIntentCreator, Description: "Audience, clips, announcements, events, and feedback."},
		{Label: "Gaming server", Value: scratchIntentGaming, Description: "LFG-style chat, voice rooms, events, and community channels."},
		{Label: "Support desk", Value: scratchIntentSupport, Description: "Help channels, ticketing, docs, and staff workflow."},
		{Label: "Product community", Value: scratchIntentProduct, Description: "Customers, support tickets, docs, feedback, and updates."},
		{Label: "Study or course", Value: scratchIntentStudy, Description: "Resources, questions, events, voice rooms, and onboarding."},
		{Label: "Custom", Value: scratchIntentCustom, Description: "A lighter suggestion set for a custom server."},
	}
	for index := range options {
		options[index].Default = options[index].Value == selectedIntent
	}
	return MessageComponent{
		Type:        "select",
		CustomID:    WizardScratchSelectCustomID(projectID, scratchIntentKey),
		Placeholder: "What kind of server is this?",
		MinValues:   1,
		MaxValues:   1,
		Options:     options,
	}
}

func wizardScratchModulesSelect(projectID, selectedModules string) MessageComponent {
	selected := scratchModuleSet(selectedModules)
	options := []MessageComponentOption{
		{Label: "Member onboarding", Value: scratchModuleOnboarding, Description: "Rules acknowledgement and verification roles."},
		{Label: "Staff area", Value: scratchModuleStaff, Description: "Private staff chat and moderation log."},
		{Label: "Announcements", Value: scratchModuleAnnouncement, Description: "Announcements channel and mentionable update role."},
		{Label: "Media channel", Value: scratchModuleMedia, Description: "Dedicated place for images, links, and highlights."},
		{Label: "Feedback channel", Value: scratchModuleFeedback, Description: "Ideas, requests, and constructive feedback."},
		{Label: "Support tickets", Value: scratchModuleSupport, Description: "Ticket panel, departments, support roles, and private tickets."},
		{Label: "Events", Value: scratchModuleEvents, Description: "Events channel and event notification role."},
		{Label: "Voice rooms", Value: scratchModuleVoice, Description: "Lobby voice channels and AFK room."},
		{Label: "Docs and FAQ", Value: scratchModuleKnowledge, Description: "Reference channel for docs and recurring answers."},
	}
	for index := range options {
		options[index].Default = selected[options[index].Value]
	}
	return MessageComponent{
		Type:        "select",
		CustomID:    WizardScratchSelectCustomID(projectID, scratchModulesKey),
		Placeholder: "Choose Panda's suggested additions",
		MinValues:   0,
		MaxValues:   len(options),
		Options:     options,
	}
}

func (s *Service) ensureWizardPreview(ctx context.Context, project store.GuildSetupProject) (store.GuildSetupProject, Preview, error) {
	var preview Preview
	if project.Status == ProjectStatusPreviewed && json.Unmarshal([]byte(project.PreviewJSON), &preview) == nil && preview.ProjectID == project.ID && len(preview.Plan) > 0 {
		return project, preview, nil
	}
	return s.buildWizardPreview(ctx, project)
}

func (s *Service) buildWizardPreview(ctx context.Context, project store.GuildSetupProject) (store.GuildSetupProject, Preview, error) {
	if ok, retry := s.setupLimits.Allow("wizard:" + project.GuildID); !ok {
		return store.GuildSetupProject{}, Preview{}, fmt.Errorf("setup preview rate limit exceeded; retry in %s", retry.Round(time.Second))
	}
	if wizardIsScratchProject(project) {
		var err error
		project, err = s.materializeScratchProject(ctx, project, wizardVariables(project), wizardStepScratch)
		if err != nil {
			return store.GuildSetupProject{}, Preview{}, err
		}
	}
	template, err := s.template(ctx, project.TemplateID)
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	snapshot := GuildSnapshot{}
	if s.adapter != nil && strings.TrimSpace(project.GuildID) != "" {
		snapshot, err = s.adapter.Snapshot(ctx, project.GuildID)
		if err != nil {
			return store.GuildSetupProject{}, Preview{}, err
		}
	}
	resources, err := s.repo.ListResources(ctx, project.GuildID)
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	preview, variables, err := s.planner.Plan(template, wizardVariables(project), snapshot, resources)
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	preview.ProjectID = project.ID
	variablesJSON, _ := json.Marshal(variables)
	previewJSON, _ := json.Marshal(preview)
	planJSON, _ := json.Marshal(preview.Plan)
	updated, err := s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"variables_json":  string(variablesJSON),
		"preview_json":    string(previewJSON),
		"apply_plan_json": string(planJSON),
		"status":          ProjectStatusPreviewed,
		"current_step":    wizardStepPreview,
		"last_error":      "",
	})
	if err != nil {
		return store.GuildSetupProject{}, Preview{}, err
	}
	s.recordAudit(ctx, updated.GuildID, updated.ActorID, "setup.wizard_preview_created", "setup_project", updated.ID, map[string]any{
		"template_id": template.ID,
		"blocked":     preview.Blocked,
		"summary":     preview.Summary,
	})
	return updated, preview, nil
}

func wizardBaseContent(project store.GuildSetupProject, step, notice string) *strings.Builder {
	content := &strings.Builder{}
	content.WriteString("**Server setup wizard**\n")
	content.WriteString(step)
	content.WriteString("\nProject: `")
	content.WriteString(project.ID)
	content.WriteString("`")
	if project.ActorID != "" {
		content.WriteString("\nAdmin: <@")
		content.WriteString(project.ActorID)
		content.WriteString(">")
	}
	if strings.TrimSpace(notice) != "" {
		content.WriteString("\n\n")
		content.WriteString(strings.TrimSpace(notice))
	}
	return content
}

func wizardModalFields(group string, variables map[string]string, template Template) []ComponentModalField {
	switch group {
	case wizardModalPurpose:
		return compactWizardFields([]ComponentModalField{
			wizardTextField("server_purpose", "Server purpose", "a friendly community", true, false, 120, variables),
			wizardTextField("member_role", "Member role", "Member", true, false, 80, variables),
			wizardTextField("verified_role", "Verified role", "Verified", false, false, 80, variables),
			wizardTextField("newcomer_role", "Newcomer role", "Newcomer", false, false, 80, variables),
		}, template)
	case wizardModalRoles:
		return compactWizardFields([]ComponentModalField{
			wizardTextField("admin_role", "Admin role", "Admin", true, false, 80, variables),
			wizardTextField("moderator_role", "Moderator role", "Moderator", true, false, 80, variables),
			wizardTextField("support_role", "Support role", "Support Team", false, false, 80, variables),
			wizardTextField("triage_role", "Triage role", "Triage", false, false, 80, variables),
		}, template)
	case wizardModalCopy:
		return compactWizardFields([]ComponentModalField{
			wizardTextField("welcome_copy", "Welcome copy", "Welcome to the server.", true, true, 1000, variables),
			wizardTextField("rules_copy", "Rules copy", "Be kind and stay on topic.", true, true, 1000, variables),
		}, template)
	case wizardModalTickets:
		if len(template.TicketPanels) == 0 {
			return nil
		}
		return compactWizardFields([]ComponentModalField{
			wizardTextField("ticket_panel_title", "Ticket panel title", "Need help?", true, false, 100, variables),
			wizardTextField("ticket_panel_body", "Ticket panel body", "Open a ticket and the team will help.", true, true, 1000, variables),
		}, template)
	default:
		return nil
	}
}

func wizardTextField(id, label, placeholder string, required, paragraph bool, maxLength int, variables map[string]string) ComponentModalField {
	return ComponentModalField{
		ID:          id,
		Label:       label,
		Placeholder: placeholder,
		Value:       variables[id],
		Required:    required,
		Paragraph:   paragraph,
		MaxLength:   maxLength,
	}
}

func compactWizardFields(fields []ComponentModalField, template Template) []ComponentModalField {
	keys := wizardTemplateVariableKeys(template)
	result := make([]ComponentModalField, 0, len(fields))
	for _, field := range fields {
		if keys[field.ID] {
			result = append(result, field)
		}
		if len(result) == 5 {
			break
		}
	}
	return result
}

func mergeWizardTemplateVariables(template Template, previous map[string]string) map[string]string {
	next := map[string]string{}
	for key, value := range template.DefaultVariables {
		next[key] = value
	}
	keys := wizardTemplateVariableKeys(template)
	for key, value := range previous {
		if keys[key] && strings.TrimSpace(value) != "" {
			next[key] = strings.TrimSpace(value)
		}
	}
	return next
}

func wizardTemplateVariableKeys(template Template) map[string]bool {
	keys := map[string]bool{}
	for key := range template.DefaultVariables {
		keys[key] = true
	}
	for _, variable := range template.EditableVariables {
		keys[variable.Key] = true
	}
	return keys
}

func wizardVariables(project store.GuildSetupProject) map[string]string {
	var variables map[string]string
	if err := json.Unmarshal([]byte(project.VariablesJSON), &variables); err != nil || variables == nil {
		return map[string]string{}
	}
	return variables
}

func firstSelectedValue(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func wizardVerificationLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "role_selection":
		return "role selection"
	case "rules_and_roles":
		return "rules and roles"
	default:
		return "rules acknowledgement"
	}
}

func wizardScratchIntentLabel(value string) string {
	switch strings.TrimSpace(value) {
	case scratchIntentCreator:
		return "creator hub"
	case scratchIntentGaming:
		return "gaming server"
	case scratchIntentSupport:
		return "support desk"
	case scratchIntentProduct:
		return "product community"
	case scratchIntentStudy:
		return "study or course"
	case scratchIntentCustom:
		return "custom server"
	default:
		return "community"
	}
}

func wizardScratchModuleSummary(raw string) string {
	modules := scratchModuleList(raw)
	if len(modules) == 0 {
		return "none beyond the core setup"
	}
	labels := make([]string, 0, len(modules))
	for _, module := range modules {
		switch module {
		case scratchModuleOnboarding:
			labels = append(labels, "onboarding")
		case scratchModuleStaff:
			labels = append(labels, "staff area")
		case scratchModuleAnnouncement:
			labels = append(labels, "announcements")
		case scratchModuleMedia:
			labels = append(labels, "media")
		case scratchModuleFeedback:
			labels = append(labels, "feedback")
		case scratchModuleSupport:
			labels = append(labels, "support tickets")
		case scratchModuleEvents:
			labels = append(labels, "events")
		case scratchModuleVoice:
			labels = append(labels, "voice rooms")
		case scratchModuleKnowledge:
			labels = append(labels, "docs and FAQ")
		}
	}
	if len(labels) == 0 {
		return "none beyond the core setup"
	}
	return strings.Join(labels, ", ")
}

func wizardModalGroupLabel(group string) string {
	switch group {
	case wizardModalPurpose:
		return "Purpose"
	case wizardModalRoles:
		return "Roles"
	case wizardModalCopy:
		return "Welcome and rules copy"
	case wizardModalTickets:
		return "Ticket panel"
	default:
		return "Setup details"
	}
}

func wizardPreviewAccent(preview Preview) string {
	if preview.Blocked {
		return "warning"
	}
	if len(preview.Warnings) > 0 {
		return "warning"
	}
	return "success"
}

func limitWizardComponentText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "."
}
