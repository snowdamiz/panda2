package setup

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

func TestPlannerValidatesVariablesAndStoredAliases(t *testing.T) {
	planner := NewPlanner()
	template := testSetupTemplate()
	template.Roles[0].Name = "{{member_role}}"
	template.Roles[0].Permissions = []string{"VIEW_CHANNEL", "ADMINISTRATOR"}
	if err := planner.ValidateTemplate(template); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("expected Administrator permission to be rejected, got %v", err)
	}

	template = testSetupTemplate()
	template.Roles[0].Name = "{{member_role}}"
	preview, variables, err := planner.Plan(template, map[string]string{"member_role": "Verified"}, GuildSnapshot{}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if variables["member_role"] != "Verified" || preview.Blocked {
		t.Fatalf("unexpected preview: variables=%+v blocked=%t", variables, preview.Blocked)
	}
	if preview.Plan[0].ResourceType != ResourceTypeRole || preview.Plan[0].Name != "Verified" || preview.Plan[0].Action != PlanActionCreate {
		t.Fatalf("expected rendered role create first, got %+v", preview.Plan[0])
	}

	stored := []store.GuildSetupResource{{
		GuildID:         "guild-1",
		ManagedAlias:    "member",
		ObjectType:      ResourceTypeRole,
		ObjectID:        "role-existing",
		LastAppliedHash: preview.Plan[0].Hash,
	}}
	again, _, err := planner.Plan(template, map[string]string{"member_role": "Verified"}, GuildSnapshot{}, stored)
	if err != nil {
		t.Fatalf("Plan with stored alias: %v", err)
	}
	if again.Plan[0].Action != PlanActionSkip || again.Plan[0].ObjectID != "role-existing" {
		t.Fatalf("expected stored alias to skip unchanged role, got %+v", again.Plan[0])
	}
}

func TestServiceApplyProjectRerunUsesStoredResources(t *testing.T) {
	ctx := context.Background()
	service, repo, adapter, cleanup := newSetupServiceTest(t)
	defer cleanup()
	upsertTemplate(t, ctx, repo, testSetupTemplate())

	project, preview, err := service.Preview(ctx, SetupRequest{
		GuildID:    "guild-1",
		ActorID:    "admin-1",
		TemplateID: "test_setup",
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if preview.Summary.Roles == 0 || preview.Summary.Channels == 0 || preview.Summary.TicketPanels != 1 || preview.Summary.OnboardingFlows != 1 {
		t.Fatalf("unexpected preview summary: %+v", preview.Summary)
	}

	result, err := service.ApplyProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ApplyProject: %v", err)
	}
	if result.Status != ProjectStatusSucceeded {
		t.Fatalf("expected succeeded apply, got %+v", result)
	}
	firstRoleCreates := adapter.roleCreates
	firstChannelCreates := adapter.channelCreates
	firstMessages := adapter.messages
	if firstRoleCreates != 2 || firstChannelCreates != 4 || firstMessages != 3 {
		t.Fatalf("unexpected first apply counts: roles=%d channels=%d messages=%d", firstRoleCreates, firstChannelCreates, firstMessages)
	}

	result, err = service.ApplyProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ApplyProject rerun: %v", err)
	}
	if result.Status != ProjectStatusSucceeded {
		t.Fatalf("expected rerun success, got %+v", result)
	}
	if adapter.roleCreates != firstRoleCreates || adapter.channelCreates != firstChannelCreates {
		t.Fatalf("rerun should not create duplicate Discord resources: roles %d->%d channels %d->%d", firstRoleCreates, adapter.roleCreates, firstChannelCreates, adapter.channelCreates)
	}
	if adapter.messages != firstMessages {
		t.Fatalf("rerun should not resend starter/panel/onboarding messages: %d->%d", firstMessages, adapter.messages)
	}
	if adapter.roleUpdates != 2 || adapter.channelUpdates != 4 {
		t.Fatalf("rerun should update stored resources, got role_updates=%d channel_updates=%d", adapter.roleUpdates, adapter.channelUpdates)
	}

	rollback, err := service.RollbackProject(ctx, project.ID, "admin-1")
	if err != nil {
		t.Fatalf("RollbackProject: %v", err)
	}
	if rollback.Status != ProjectStatusRolledBack {
		t.Fatalf("expected rolled back project, got %+v", rollback)
	}
	if adapter.roleDeletes != 2 || adapter.channelDeletes != 4 {
		t.Fatalf("unexpected rollback deletes: roles=%d channels=%d", adapter.roleDeletes, adapter.channelDeletes)
	}
	panel, ok, err := repo.GetTicketPanel(ctx, stablePanelID("guild-1", "support_panel"))
	if err != nil || !ok || panel.Enabled {
		t.Fatalf("expected ticket panel disabled, ok=%t err=%v panel=%+v", ok, err, panel)
	}
	flow, ok, err := repo.GetOnboardingFlow(ctx, stableFlowID("guild-1", "rules"))
	if err != nil || !ok || flow.Enabled || !flow.Paused {
		t.Fatalf("expected onboarding flow disabled and paused, ok=%t err=%v flow=%+v", ok, err, flow)
	}
}

func TestManageServerSetupIntentAndTemplateImportExport(t *testing.T) {
	ctx := context.Background()
	service, _, _, cleanup := newSetupServiceTest(t)
	defer cleanup()

	response, err := service.ManageServerSetup(ctx, toolsvc.ServerSetupManagementRequest{
		GuildID: "guild-1",
		ActorID: "admin-1",
		Action:  "preview",
		Text:    "Set this server up as a customer support desk with tickets.",
	})
	if err != nil {
		t.Fatalf("ManageServerSetup preview intent: %v", err)
	}
	payload := response.(map[string]any)
	project := payload["project"].(map[string]any)
	if project["template_id"] != "support_desk" {
		t.Fatalf("expected support_desk intent selection, got %+v", project)
	}

	custom := testSetupTemplate()
	custom.ID = "custom_setup"
	custom.Name = "Custom Setup"
	templateJSON, _ := json.Marshal(custom)
	imported, err := service.ManageServerSetup(ctx, toolsvc.ServerSetupManagementRequest{
		ActorID:      "admin-1",
		Action:       "import",
		TemplateJSON: string(templateJSON),
	})
	if err != nil {
		t.Fatalf("ManageServerSetup import: %v", err)
	}
	importedTemplate := imported.(map[string]any)["template"].(map[string]any)
	if importedTemplate["id"] != "custom_setup" {
		t.Fatalf("unexpected imported template: %+v", importedTemplate)
	}
	exported, err := service.ManageServerSetup(ctx, toolsvc.ServerSetupManagementRequest{
		Action:     "export",
		TemplateID: "custom_setup",
	})
	if err != nil {
		t.Fatalf("ManageServerSetup export: %v", err)
	}
	exportedTemplate := exported.(map[string]any)["template"].(Template)
	if exportedTemplate.ID != "custom_setup" || exportedTemplate.Name != "Custom Setup" {
		t.Fatalf("unexpected exported template: %+v", exportedTemplate)
	}
}

func TestSetupWizardNativeFlow(t *testing.T) {
	ctx := context.Background()
	service, repo, _, cleanup := newSetupServiceTest(t)
	defer cleanup()

	start, err := service.StartSetupWizard(ctx, ComponentRequest{
		GuildID: "guild-1",
		UserID:  "admin-1",
		IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("StartSetupWizard: %v", err)
	}
	if len(start.Components) == 0 || !strings.Contains(start.Content, "Step 1 of 4") {
		t.Fatalf("expected native template step, got %+v", start)
	}
	project, ok, err := repo.GetLatestProjectForGuild(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("GetLatestProjectForGuild: ok=%t err=%v", ok, err)
	}

	customize, err := service.HandleSetupWizardTemplateSelection(ctx, ComponentRequest{
		CustomID: WizardTemplateSelectCustomID(project.ID),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
		Values:   []string{"support_desk"},
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardTemplateSelection: %v", err)
	}
	if !strings.Contains(customize.Content, "Step 2 of 4") || !strings.Contains(customize.Content, "Support Desk") {
		t.Fatalf("expected customize step for support desk, got %+v", customize)
	}
	project, ok, err = repo.GetProject(ctx, project.ID)
	if err != nil || !ok || project.TemplateID != "support_desk" {
		t.Fatalf("expected support_desk project, ok=%t err=%v project=%+v", ok, err, project)
	}

	modal, err := service.HandleSetupWizardAction(ctx, ComponentRequest{
		CustomID: WizardActionCustomID(project.ID, wizardActionEditPurpose),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardAction edit purpose: %v", err)
	}
	if modal.Modal == nil || modal.Modal.ID != WizardModalCustomID(project.ID, wizardModalPurpose) || len(modal.Modal.Fields) == 0 {
		t.Fatalf("expected purpose modal, got %+v", modal)
	}

	saved, err := service.HandleSetupWizardModal(ctx, ComponentRequest{
		CustomID: WizardModalCustomID(project.ID, wizardModalPurpose),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
		Fields: map[string]string{
			"server_purpose": "a premium support community",
			"member_role":    "Customer",
		},
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardModal: %v", err)
	}
	if !strings.Contains(saved.Content, "premium support community") {
		t.Fatalf("expected saved variables in customize response, got %q", saved.Content)
	}
	project, _, _ = repo.GetProject(ctx, project.ID)

	preview, err := service.HandleSetupWizardAction(ctx, ComponentRequest{
		CustomID: WizardActionCustomID(project.ID, wizardActionPreview),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardAction preview: %v", err)
	}
	if !strings.Contains(preview.Content, "Step 3 of 4") || !strings.Contains(preview.Content, "ticket panels") {
		t.Fatalf("expected native preview with ticket panel summary, got %+v", preview)
	}
	if len(preview.Components) == 0 || preview.Components[0].CustomID != WizardActionCustomID(project.ID, wizardActionApply) {
		t.Fatalf("expected apply button in preview, got %+v", preview.Components)
	}

	applied, err := service.HandleSetupWizardAction(ctx, ComponentRequest{
		CustomID: WizardActionCustomID(project.ID, wizardActionApply),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardAction apply: %v", err)
	}
	if !strings.Contains(applied.Content, "Project: `"+project.ID+"`") || len(applied.Components) != 0 {
		t.Fatalf("expected terminal apply response, got %+v", applied)
	}
	project, ok, err = repo.GetProject(ctx, project.ID)
	if err != nil || !ok || project.Status != ProjectStatusConfirmed {
		t.Fatalf("expected confirmed project without job queue, ok=%t err=%v project=%+v", ok, err, project)
	}
}

func TestSetupWizardScratchFlowGeneratesCustomPlan(t *testing.T) {
	ctx := context.Background()
	service, repo, _, cleanup := newSetupServiceTest(t)
	defer cleanup()

	start, err := service.StartSetupWizard(ctx, ComponentRequest{
		GuildID: "guild-1",
		UserID:  "admin-1",
		IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("StartSetupWizard: %v", err)
	}
	if !strings.Contains(start.Content, "Start from scratch") {
		t.Fatalf("expected scratch option in first step, got %q", start.Content)
	}
	project, ok, err := repo.GetLatestProjectForGuild(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("GetLatestProjectForGuild: ok=%t err=%v", ok, err)
	}

	scratch, err := service.HandleSetupWizardAction(ctx, ComponentRequest{
		CustomID: WizardActionCustomID(project.ID, wizardActionScratch),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardAction scratch: %v", err)
	}
	if !strings.Contains(scratch.Content, "build from scratch") || !strings.Contains(scratch.Content, "core setup") {
		t.Fatalf("expected scratch suggestion panel, got %+v", scratch)
	}
	project, ok, err = repo.GetProject(ctx, project.ID)
	if err != nil || !ok || !strings.HasPrefix(project.TemplateID, "scratch_") || wizardVariables(project)[wizardModeKey] != wizardModeScratch {
		t.Fatalf("expected scratch project, ok=%t err=%v project=%+v variables=%+v", ok, err, project, wizardVariables(project))
	}

	support, err := service.HandleSetupWizardScratchSelection(ctx, ComponentRequest{
		CustomID: WizardScratchSelectCustomID(project.ID, scratchIntentKey),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
		Values:   []string{scratchIntentSupport},
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardScratchSelection intent: %v", err)
	}
	if !strings.Contains(support.Content, "support desk") || !strings.Contains(support.Content, "support tickets") {
		t.Fatalf("expected support suggestions, got %+v", support)
	}

	trimmed, err := service.HandleSetupWizardScratchSelection(ctx, ComponentRequest{
		CustomID: WizardScratchSelectCustomID(project.ID, scratchModulesKey),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
		Values:   []string{scratchModuleStaff, scratchModuleSupport, scratchModuleKnowledge},
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardScratchSelection modules: %v", err)
	}
	if !strings.Contains(trimmed.Content, "docs and FAQ") || !strings.Contains(trimmed.Content, "ticket panel") {
		t.Fatalf("expected selected scratch modules in panel, got %+v", trimmed)
	}

	preview, err := service.HandleSetupWizardAction(ctx, ComponentRequest{
		CustomID: WizardActionCustomID(project.ID, wizardActionPreview),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardAction preview scratch: %v", err)
	}
	if !strings.Contains(preview.Content, "Plan: **Custom from scratch**") || !strings.Contains(preview.Content, "ticket panels") {
		t.Fatalf("expected scratch preview with generated plan, got %+v", preview)
	}

	applied, err := service.HandleSetupWizardAction(ctx, ComponentRequest{
		CustomID: WizardActionCustomID(project.ID, wizardActionApply),
		GuildID:  "guild-1",
		UserID:   "admin-1",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("HandleSetupWizardAction apply scratch: %v", err)
	}
	if !strings.Contains(applied.Content, "Project: `"+project.ID+"`") {
		t.Fatalf("expected terminal scratch apply response, got %+v", applied)
	}
	project, ok, err = repo.GetProject(ctx, project.ID)
	if err != nil || !ok || project.Status != ProjectStatusConfirmed {
		t.Fatalf("expected confirmed scratch project, ok=%t err=%v project=%+v", ok, err, project)
	}
}

func TestTicketLifecycleAndOnboardingCompletion(t *testing.T) {
	ctx := context.Background()
	service, repo, adapter, cleanup := newSetupServiceTest(t)
	defer cleanup()

	panelID := stablePanelID("guild-1", "support_panel")
	departmentsJSON, _ := json.Marshal([]TicketDepartment{{
		ID:              "support",
		Label:           "Support",
		StaffRoleIDs:    []string{"role-staff"},
		InitialPriority: "normal",
	}})
	if _, err := repo.UpsertTicketPanel(ctx, store.TicketPanel{
		ID:               panelID,
		GuildID:          "guild-1",
		PanelChannelID:   "channel-support",
		Title:            "Support",
		DepartmentsJSON:  string(departmentsJSON),
		StaffRoleIDsJSON: `["role-staff"]`,
		TargetCategoryID: "category-tickets",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertTicketPanel: %v", err)
	}

	openResponse, err := service.OpenTicket(ctx, ComponentRequest{
		CustomID: TicketOpenCustomID(panelID, "support"),
		GuildID:  "guild-1",
		UserID:   "user-1",
	})
	if err != nil {
		t.Fatalf("OpenTicket: %v", err)
	}
	if !openResponse.Ephemeral || adapter.ticketChannels != 1 {
		t.Fatalf("unexpected open response=%+v ticket_channels=%d", openResponse, adapter.ticketChannels)
	}
	tickets, err := repo.ListTickets(ctx, "guild-1", "", 10)
	if err != nil || len(tickets) != 1 {
		t.Fatalf("ListTickets: tickets=%+v err=%v", tickets, err)
	}
	ticket := tickets[0]
	if ticket.Status != TicketStatusOpen || ticket.ChannelID == "" {
		t.Fatalf("unexpected opened ticket: %+v", ticket)
	}

	claim, err := service.HandleTicketAction(ctx, ComponentRequest{
		CustomID: TicketActionCustomID("claim", ticket.ID),
		GuildID:  "guild-1",
		UserID:   "staff-1",
	})
	if err != nil {
		t.Fatalf("HandleTicketAction claim: %v", err)
	}
	if claim.Ephemeral || claim.Accent != "success" {
		t.Fatalf("unexpected claim response: %+v", claim)
	}
	closeResponse, err := service.HandleTicketCloseModal(ctx, ComponentRequest{
		CustomID: TicketCloseModalID(ticket.ID),
		GuildID:  "guild-1",
		UserID:   "staff-1",
		Fields:   map[string]string{"reason": "resolved"},
	})
	if err != nil {
		t.Fatalf("HandleTicketCloseModal: %v", err)
	}
	if closeResponse.Accent != "success" || adapter.transcripts != 1 {
		t.Fatalf("unexpected close response=%+v transcripts=%d", closeResponse, adapter.transcripts)
	}
	closed, ok, err := repo.GetTicket(ctx, ticket.ID)
	if err != nil || !ok {
		t.Fatalf("GetTicket: ok=%t err=%v", ok, err)
	}
	if closed.Status != TicketStatusClosed || closed.CloseReason != "resolved" {
		t.Fatalf("unexpected closed ticket: %+v", closed)
	}

	openResponse, err = service.OpenTicket(ctx, ComponentRequest{
		CustomID: TicketOpenCustomID(panelID, "support"),
		GuildID:  "guild-1",
		UserID:   "user-2",
	})
	if err != nil {
		t.Fatalf("OpenTicket second: %v", err)
	}
	if openResponse.Accent != "success" {
		t.Fatalf("unexpected second open response: %+v", openResponse)
	}
	tickets, err = repo.ListTickets(ctx, "guild-1", TicketStatusOpen, 10)
	if err != nil || len(tickets) != 1 {
		t.Fatalf("ListTickets open: tickets=%+v err=%v", tickets, err)
	}
	openTicket := tickets[0]
	if _, err := service.ManageTicket(ctx, toolsvc.TicketManagementRequest{
		GuildID:  "guild-1",
		ActorID:  "staff-1",
		Action:   "add_participant",
		TicketID: openTicket.ID,
		UserID:   "observer-1",
	}); err != nil {
		t.Fatalf("ManageTicket add_participant: %v", err)
	}
	if _, err := service.ManageTicket(ctx, toolsvc.TicketManagementRequest{
		GuildID:  "guild-1",
		ActorID:  "staff-1",
		Action:   "remove_participant",
		TicketID: openTicket.ID,
		UserID:   "observer-1",
	}); err != nil {
		t.Fatalf("ManageTicket remove_participant: %v", err)
	}
	if adapter.participantAdds != 1 || adapter.participantRemoves != 1 {
		t.Fatalf("unexpected participant adapter calls: add=%d remove=%d", adapter.participantAdds, adapter.participantRemoves)
	}

	flowID := stableFlowID("guild-1", "rules")
	if _, err := repo.UpsertOnboardingFlow(ctx, store.OnboardingFlow{
		ID:               flowID,
		GuildID:          "guild-1",
		WelcomeChannelID: "channel-welcome",
		VerifiedRoleID:   "role-verified",
		NewcomerRoleID:   "role-new",
		VerificationMode: "rules",
		StepsJSON:        "[]",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertOnboardingFlow: %v", err)
	}
	onboarding, err := service.CompleteOnboarding(ctx, ComponentRequest{
		CustomID: OnboardingAcknowledgeCustomID(flowID),
		GuildID:  "guild-1",
		UserID:   "user-2",
	})
	if err != nil {
		t.Fatalf("CompleteOnboarding: %v", err)
	}
	if onboarding.Accent != "success" || len(adapter.addedRoles) != 1 || len(adapter.removedRoles) != 1 {
		t.Fatalf("unexpected onboarding response=%+v added=%+v removed=%+v", onboarding, adapter.addedRoles, adapter.removedRoles)
	}
	session, ok, err := repo.GetOnboardingSession(ctx, flowID, "user-2")
	if err != nil || !ok {
		t.Fatalf("GetOnboardingSession: ok=%t err=%v", ok, err)
	}
	if session.Status != OnboardingStatusCompleted || session.CurrentStep != "complete" {
		t.Fatalf("unexpected onboarding session: %+v", session)
	}

	roleFlowID := stableFlowID("guild-1", "rules_and_roles")
	roleStepsJSON, _ := json.Marshal([]OnboardingStepTemplate{{
		ID:            "interests",
		Type:          "role_selection",
		Prompt:        "Pick roles",
		RoleAliases:   []string{"role-updates", "role-events"},
		MinSelections: 0,
		MaxSelections: 2,
	}})
	if _, err := repo.UpsertOnboardingFlow(ctx, store.OnboardingFlow{
		ID:               roleFlowID,
		GuildID:          "guild-1",
		WelcomeChannelID: "channel-welcome",
		VerifiedRoleID:   "role-verified",
		NewcomerRoleID:   "role-new",
		VerificationMode: "rules_and_roles",
		StepsJSON:        string(roleStepsJSON),
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertOnboardingFlow role flow: %v", err)
	}
	beforeAdds := len(adapter.addedRoles)
	ack, err := service.CompleteOnboarding(ctx, ComponentRequest{
		CustomID: OnboardingAcknowledgeCustomID(roleFlowID),
		GuildID:  "guild-1",
		UserID:   "user-3",
	})
	if err != nil {
		t.Fatalf("CompleteOnboarding rules_and_roles ack: %v", err)
	}
	if ack.Accent != "warning" || len(adapter.addedRoles) != beforeAdds {
		t.Fatalf("expected ack to wait for roles, response=%+v added=%+v", ack, adapter.addedRoles)
	}
	roleSelection, err := service.HandleOnboardingRoleSelection(ctx, ComponentRequest{
		CustomID: OnboardingRoleSelectCustomID(roleFlowID, "interests"),
		GuildID:  "guild-1",
		UserID:   "user-3",
		Values:   []string{"role-updates"},
	})
	if err != nil {
		t.Fatalf("HandleOnboardingRoleSelection: %v", err)
	}
	if roleSelection.Accent != "success" || len(adapter.addedRoles) != beforeAdds+2 {
		t.Fatalf("unexpected role selection response=%+v added=%+v", roleSelection, adapter.addedRoles)
	}
	roleSession, ok, err := repo.GetOnboardingSession(ctx, roleFlowID, "user-3")
	if err != nil || !ok {
		t.Fatalf("GetOnboardingSession role flow: ok=%t err=%v", ok, err)
	}
	if roleSession.Status != OnboardingStatusCompleted || roleSession.SelectedRoleIDsJSON != `["role-updates"]` {
		t.Fatalf("unexpected role onboarding session: %+v", roleSession)
	}
}

func newSetupServiceTest(t *testing.T) (*Service, *repository.SetupRepository, *fakeSetupAdapter, func()) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "panda.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	repo := repository.NewSetupRepository(db.DB)
	adapter := &fakeSetupAdapter{}
	service := NewService(repo, adapter)
	return service, repo, adapter, func() { _ = db.Close() }
}

func upsertTemplate(t *testing.T, ctx context.Context, repo *repository.SetupRepository, template Template) {
	t.Helper()
	templateJSON, err := json.Marshal(template)
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	defaultsJSON, _ := json.Marshal(template.DefaultVariables)
	if _, err := repo.UpsertTemplate(ctx, store.SetupTemplate{
		ID:               template.ID,
		SchemaVersion:    template.SchemaVersion,
		TemplateVersion:  template.TemplateVersion,
		Name:             template.Name,
		Description:      template.Description,
		ReleaseState:     template.ReleaseState,
		DefaultVariables: string(defaultsJSON),
		TemplateJSON:     string(templateJSON),
		BuiltIn:          false,
	}); err != nil {
		t.Fatalf("UpsertTemplate: %v", err)
	}
}

func testSetupTemplate() Template {
	return Template{
		ID:              "test_setup",
		SchemaVersion:   SchemaVersion,
		TemplateVersion: 1,
		Name:            "Test Setup",
		Description:     "Test setup template",
		ReleaseState:    "stable",
		DefaultVariables: map[string]string{
			"member_role": "Member",
		},
		EditableVariables: []TemplateVariable{{Key: "member_role", Label: "Member role", Required: true}},
		Roles: []RoleTemplate{
			{Alias: "member", Name: "{{member_role}}", Color: "#336699"},
			{Alias: "staff", Name: "Staff", Color: "#663399"},
		},
		Categories: []CategoryTemplate{
			{Alias: "community", Name: "Community", Position: 1},
			{Alias: "tickets", Name: "Tickets", Position: 2},
		},
		Channels: []ChannelTemplate{
			{
				Alias:       "welcome",
				Type:        "text",
				Name:        "welcome",
				ParentAlias: "community",
				StarterMessages: []StarterMessage{{
					Alias:   "hello",
					Content: "Welcome to the server.",
				}},
			},
			{Alias: "support", Type: "text", Name: "support", ParentAlias: "community"},
		},
		TicketPanels: []TicketPanelTemplate{{
			Alias:               "support_panel",
			PanelChannelAlias:   "support",
			Title:               "Need help?",
			Body:                "Open a ticket.",
			StaffRoleAliases:    []string{"staff"},
			TargetCategoryAlias: "tickets",
			Departments: []TicketDepartmentTemplate{{
				ID:              "support",
				Label:           "Support",
				InitialPriority: "normal",
			}},
		}},
		OnboardingFlows: []OnboardingFlowTemplate{{
			Alias:               "rules",
			WelcomeChannelAlias: "welcome",
			VerifiedRoleAlias:   "member",
			VerificationMode:    "rules",
			IntroPrompt:         "Acknowledge the rules.",
			Steps:               []OnboardingStepTemplate{{ID: "rules", Type: "rules_ack", Prompt: "Rules", Required: true}},
		}},
	}
}

type fakeSetupAdapter struct {
	roleCreates        int
	roleUpdates        int
	roleDeletes        int
	channelCreates     int
	channelUpdates     int
	channelDeletes     int
	messages           int
	ticketChannels     int
	participantAdds    int
	participantRemoves int
	transcripts        int
	addedRoles         []string
	removedRoles       []string
}

func (f *fakeSetupAdapter) Snapshot(context.Context, string) (GuildSnapshot, error) {
	return GuildSnapshot{}, nil
}

func (f *fakeSetupAdapter) CreateRole(_ context.Context, request RoleApplyRequest) (DiscordResource, error) {
	f.roleCreates++
	return DiscordResource{ID: "role-" + normalizeDiscordName(request.Name), Name: request.Name, Type: ResourceTypeRole}, nil
}

func (f *fakeSetupAdapter) UpdateRole(_ context.Context, request RoleApplyRequest) (DiscordResource, error) {
	f.roleUpdates++
	return DiscordResource{ID: request.RoleID, Name: request.Name, Type: ResourceTypeRole}, nil
}

func (f *fakeSetupAdapter) DeleteRole(context.Context, string, string, string) error {
	f.roleDeletes++
	return nil
}

func (f *fakeSetupAdapter) MoveRoles(context.Context, string, []PositionUpdate, string) error {
	return nil
}

func (f *fakeSetupAdapter) CreateChannel(_ context.Context, request ChannelApplyRequest) (DiscordResource, error) {
	f.channelCreates++
	return DiscordResource{ID: "channel-" + normalizeDiscordName(request.Name), Name: request.Name, Type: request.Type}, nil
}

func (f *fakeSetupAdapter) UpdateChannel(_ context.Context, request ChannelApplyRequest) (DiscordResource, error) {
	f.channelUpdates++
	return DiscordResource{ID: request.ChannelID, Name: request.Name, Type: request.Type}, nil
}

func (f *fakeSetupAdapter) DeleteChannel(context.Context, string, string) error {
	f.channelDeletes++
	return nil
}

func (f *fakeSetupAdapter) MoveChannels(context.Context, string, []PositionUpdate, string) error {
	return nil
}

func (f *fakeSetupAdapter) SendMessage(context.Context, MessageApplyRequest) (DiscordResource, error) {
	f.messages++
	return DiscordResource{ID: "message-" + string(rune('0'+f.messages)), Name: "message", Type: "message"}, nil
}

func (f *fakeSetupAdapter) CreateTicketChannel(_ context.Context, request TicketChannelRequest) (DiscordResource, error) {
	f.ticketChannels++
	return DiscordResource{ID: "ticket-channel-" + request.RequesterUserID, Name: request.Name, Type: "channel"}, nil
}

func (f *fakeSetupAdapter) AddTicketParticipant(context.Context, string, string, string, string) error {
	f.participantAdds++
	return nil
}

func (f *fakeSetupAdapter) RemoveTicketParticipant(context.Context, string, string, string, string) error {
	f.participantRemoves++
	return nil
}

func (f *fakeSetupAdapter) ExportTranscript(context.Context, string, int) (map[string]any, error) {
	f.transcripts++
	return map[string]any{"messages": []any{"hello"}}, nil
}

func (f *fakeSetupAdapter) AddMemberRole(_ context.Context, _, userID, roleID, _ string) error {
	f.addedRoles = append(f.addedRoles, userID+":"+roleID)
	return nil
}

func (f *fakeSetupAdapter) RemoveMemberRole(_ context.Context, _, userID, roleID, _ string) error {
	f.removedRoles = append(f.removedRoles, userID+":"+roleID)
	return nil
}
