package setup

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/store"
)

const (
	wizardModeKey     = "wizard_mode"
	wizardModeScratch = "scratch"

	scratchIntentKey          = "scratch_intent"
	scratchModulesKey         = "scratch_modules"
	scratchPurposeCustomKey   = "scratch_purpose_custom"
	scratchIntentCommunity    = "community"
	scratchIntentCreator      = "creator"
	scratchIntentGaming       = "gaming"
	scratchIntentSupport      = "support"
	scratchIntentProduct      = "product"
	scratchIntentStudy        = "study"
	scratchIntentCustom       = "custom"
	scratchModuleOnboarding   = "onboarding"
	scratchModuleStaff        = "staff_area"
	scratchModuleAnnouncement = "announcements"
	scratchModuleMedia        = "media"
	scratchModuleFeedback     = "feedback"
	scratchModuleSupport      = "support_tickets"
	scratchModuleEvents       = "events"
	scratchModuleVoice        = "voice"
	scratchModuleKnowledge    = "knowledge"
)

func (s *Service) startScratchWizard(ctx context.Context, project store.GuildSetupProject) (store.GuildSetupProject, error) {
	variables := scratchDefaultVariables(scratchIntentCommunity)
	return s.materializeScratchProject(ctx, project, variables, wizardStepScratch)
}

func (s *Service) materializeScratchProject(ctx context.Context, project store.GuildSetupProject, variables map[string]string, currentStep string) (store.GuildSetupProject, error) {
	if variables == nil {
		variables = scratchDefaultVariables(scratchIntentCommunity)
	}
	variables[wizardModeKey] = wizardModeScratch
	if strings.TrimSpace(variables[scratchIntentKey]) == "" {
		variables[scratchIntentKey] = scratchIntentCommunity
	}
	if _, ok := variables[scratchModulesKey]; !ok {
		variables[scratchModulesKey] = strings.Join(scratchDefaultModules(variables[scratchIntentKey]), ",")
	}
	template := scratchTemplate(project.ID, variables)
	templateJSON, err := json.Marshal(template)
	if err != nil {
		return store.GuildSetupProject{}, err
	}
	defaultsJSON, _ := json.Marshal(template.DefaultVariables)
	if _, err := s.repo.UpsertTemplate(ctx, store.SetupTemplate{
		ID:               template.ID,
		SchemaVersion:    template.SchemaVersion,
		TemplateVersion:  template.TemplateVersion,
		Name:             template.Name,
		Description:      template.Description,
		ReleaseState:     "draft",
		DefaultVariables: string(defaultsJSON),
		TemplateJSON:     string(templateJSON),
		BuiltIn:          false,
		CreatedBy:        project.ActorID,
	}); err != nil {
		return store.GuildSetupProject{}, err
	}
	variablesJSON, _ := json.Marshal(variables)
	return s.repo.UpdateProject(ctx, project.ID, map[string]any{
		"template_id":       template.ID,
		"template_version":  template.TemplateVersion,
		"schema_version":    template.SchemaVersion,
		"variables_json":    string(variablesJSON),
		"preview_json":      "{}",
		"apply_plan_json":   "[]",
		"status":            ProjectStatusDraft,
		"current_step":      currentStep,
		"failed_steps_json": "[]",
		"last_error":        "",
	})
}

func wizardIsScratchProject(project store.GuildSetupProject) bool {
	variables := wizardVariables(project)
	return variables[wizardModeKey] == wizardModeScratch || strings.HasPrefix(project.TemplateID, "scratch_")
}

func scratchTemplateID(projectID string) string {
	return "scratch_" + strings.TrimSpace(projectID)
}

func scratchDefaultVariables(intent string) map[string]string {
	intent = firstNonEmpty(intent, scratchIntentCommunity)
	variables := baseVariables(scratchPurpose(intent))
	variables[wizardModeKey] = wizardModeScratch
	variables[scratchIntentKey] = intent
	variables[scratchModulesKey] = strings.Join(scratchDefaultModules(intent), ",")
	variables["welcome_copy"] = "Welcome in. Start with the rules, then say hello when you are ready."
	variables["rules_copy"] = "Be kind, keep conversations on topic, respect privacy, avoid spam, and follow Discord's Terms of Service."
	return variables
}

func scratchTemplate(projectID string, variables map[string]string) Template {
	variables = mergeScratchDefaults(variables)
	modules := scratchModuleSet(variables[scratchModulesKey])
	template := Template{
		ID:               scratchTemplateID(projectID),
		SchemaVersion:    SchemaVersion,
		TemplateVersion:  1,
		Name:             "Custom from scratch",
		Description:      "A custom setup generated from native Discord wizard choices.",
		ReleaseState:     "draft",
		DefaultVariables: variables,
		EditableVariables: []TemplateVariable{
			{Key: "server_purpose", Label: "Server purpose", Type: "text", Required: true},
			{Key: "member_role", Label: "Member role", Type: "text", Required: true},
			{Key: "verified_role", Label: "Verified role", Type: "text", Required: false},
			{Key: "newcomer_role", Label: "Newcomer role", Type: "text", Required: false},
			{Key: "moderator_role", Label: "Moderator role", Type: "text", Required: true},
			{Key: "admin_role", Label: "Admin role", Type: "text", Required: true},
			{Key: "support_role", Label: "Support team role", Type: "text", Required: false},
			{Key: "triage_role", Label: "Triage role", Type: "text", Required: false},
			{Key: "welcome_copy", Label: "Welcome copy", Type: "textarea", Required: true},
			{Key: "rules_copy", Label: "Rules copy", Type: "textarea", Required: true},
			{Key: "ticket_panel_title", Label: "Ticket panel title", Type: "text", Required: false},
			{Key: "ticket_panel_body", Label: "Ticket panel body", Type: "textarea", Required: false},
			{Key: "verification_strictness", Label: "Verification mode", Type: "select", Required: true, Options: []string{"rules", "role_selection", "rules_and_roles"}},
		},
		FeatureIDs: []string{
			features.AssistantChat,
			features.AdminSetup,
			features.AdminAccessControl,
			features.AdminAudit,
			features.DiscordMessages,
			features.DiscordChannelTools,
			features.DiscordRoleManagement,
		},
		Roles: []RoleTemplate{
			{Alias: "role_admin", Name: "{{admin_role}}", Color: "#ed4245", Hoist: true, Mentionable: false, Permissions: []string{"MANAGE_CHANNELS", "MANAGE_ROLES", "MANAGE_MESSAGES"}, Position: 80, Profile: "admin"},
			{Alias: "role_moderator", Name: "{{moderator_role}}", Color: "#5865f2", Hoist: true, Mentionable: true, Permissions: []string{"MANAGE_MESSAGES", "MODERATE_MEMBERS"}, Position: 70, Profile: "moderator"},
			{Alias: "role_member", Name: "{{member_role}}", Color: "#57f287", Position: 20},
		},
		Categories: []CategoryTemplate{
			{Alias: "cat_welcome", Name: "Start Here", Position: 10},
			{Alias: "cat_community", Name: "Community", Position: 20},
		},
		Channels: []ChannelTemplate{
			{Alias: "chan_rules", Type: "text", Name: "rules", ParentAlias: "cat_welcome", Topic: "{{rules_copy}}", Position: 10, StarterMessages: []StarterMessage{{Alias: "rules", Content: "{{rules_copy}}"}}},
			{Alias: "chan_welcome", Type: "text", Name: "welcome", ParentAlias: "cat_welcome", Topic: "New member welcome and first steps.", Position: 20, StarterMessages: []StarterMessage{{Alias: "welcome", Content: "{{welcome_copy}}"}}},
			{Alias: "chan_general", Type: "text", Name: "general", ParentAlias: "cat_community", Topic: "{{general_topic}}", Position: 40},
		},
		Panda: PandaConfigTemplate{
			PromptOverlay: "This server is {{server_purpose}}. Be helpful, concise, and careful when changing server configuration.",
			RoleProfiles:  map[string]string{"role_admin": "admin", "role_moderator": "moderator"},
			ChannelRules:  map[string]string{},
		},
	}
	if modules[scratchModuleOnboarding] {
		template.Roles = append(template.Roles,
			RoleTemplate{Alias: "role_verified", Name: "{{verified_role}}", Color: "#fee75c", Position: 10},
			RoleTemplate{Alias: "role_newcomer", Name: "{{newcomer_role}}", Color: "#99aab5", Position: 5},
		)
		template.OnboardingFlows = append(template.OnboardingFlows, OnboardingFlowTemplate{
			Alias: "onboarding_default", WelcomeChannelAlias: "chan_welcome", RulesChannelAlias: "chan_rules", VerifiedRoleAlias: "role_verified", NewcomerRoleAlias: "role_newcomer",
			VerificationMode: "{{verification_strictness}}", IntroPrompt: "{{welcome_copy}}", CompletionMessage: "You are all set. Welcome in.",
			Steps: []OnboardingStepTemplate{
				{ID: "rules", Type: "rules_ack", Prompt: "{{rules_copy}}", Required: true},
			},
		})
	}
	if modules[scratchModuleStaff] {
		template.Categories = append(template.Categories, CategoryTemplate{Alias: "cat_staff", Name: "Staff", Position: 90, Overwrites: staffOverwrite()})
		template.Channels = append(template.Channels,
			ChannelTemplate{Alias: "chan_staff", Type: "text", Name: "staff-chat", ParentAlias: "cat_staff", Topic: "Private staff coordination.", Position: 90},
			ChannelTemplate{Alias: "chan_mod_logs", Type: "text", Name: "mod-log", ParentAlias: "cat_staff", Topic: "Moderation notes and operational context.", Position: 91},
		)
		template.Panda.ChannelRules["chan_staff"] = "allow"
		template.Panda.ChannelRules["chan_mod_logs"] = "allow"
	}
	if modules[scratchModuleAnnouncement] {
		template.Roles = append(template.Roles, RoleTemplate{Alias: "role_announcements", Name: "Announcements", Color: "#57f287", Mentionable: true, Position: 24})
		template.Channels = append(template.Channels, ChannelTemplate{Alias: "chan_announcements", Type: "text", Name: "announcements", ParentAlias: "cat_welcome", Topic: "{{announcement_topic}}", Position: 30})
	}
	if modules[scratchModuleMedia] {
		template.Channels = append(template.Channels, ChannelTemplate{Alias: "chan_media", Type: "text", Name: "media", ParentAlias: "cat_community", Topic: "Share images, links, and community moments.", Position: 50})
	}
	if modules[scratchModuleFeedback] {
		template.Channels = append(template.Channels, ChannelTemplate{Alias: "chan_feedback", Type: "text", Name: "feedback", ParentAlias: "cat_community", Topic: "{{feedback_topic}}", Position: 60})
	}
	if modules[scratchModuleEvents] {
		template.Roles = append(template.Roles, RoleTemplate{Alias: "role_events", Name: "Events", Color: "#5865f2", Mentionable: true, Position: 23})
		template.Channels = append(template.Channels, ChannelTemplate{Alias: "chan_events", Type: "text", Name: "events", ParentAlias: "cat_welcome", Topic: "Upcoming server events and reminders.", Position: 31})
	}
	if modules[scratchModuleVoice] {
		template.Channels = append(template.Channels,
			ChannelTemplate{Alias: "voice_lobby_1", Type: "voice", Name: "Lobby 1", ParentAlias: "cat_community", Position: 70, UserLimit: 8},
			ChannelTemplate{Alias: "voice_lobby_2", Type: "voice", Name: "Lobby 2", ParentAlias: "cat_community", Position: 71, UserLimit: 8},
			ChannelTemplate{Alias: "voice_afk", Type: "voice", Name: "AFK", ParentAlias: "cat_community", Position: 72},
		)
	}
	if modules[scratchModuleKnowledge] {
		template.FeatureIDs = append(template.FeatureIDs, features.Knowledge)
		template.Channels = append(template.Channels, ChannelTemplate{Alias: "chan_docs", Type: "text", Name: "docs-and-faq", ParentAlias: "cat_community", Topic: "Reference links, FAQs, and canonical answers.", Position: 65, StarterMessages: []StarterMessage{{Alias: "docs", Content: "Add your key docs, FAQs, and recurring answers here. Panda can use this as server context when knowledge is enabled."}}})
	}
	if modules[scratchModuleSupport] {
		template.FeatureIDs = append(template.FeatureIDs, features.ComposedTools)
		template.Roles = append(template.Roles,
			RoleTemplate{Alias: "role_support", Name: "{{support_role}}", Color: "#57f287", Hoist: true, Mentionable: true, Permissions: []string{"MANAGE_MESSAGES"}, Position: 60, Profile: "moderator"},
			RoleTemplate{Alias: "role_triage", Name: "{{triage_role}}", Color: "#fee75c", Hoist: true, Mentionable: true, Position: 55},
		)
		template.Categories = append(template.Categories,
			CategoryTemplate{Alias: "cat_support", Name: "Support", Position: 30},
			CategoryTemplate{Alias: "cat_tickets", Name: "Tickets", Position: 80, Overwrites: staffOverwrite()},
		)
		template.Channels = append(template.Channels,
			ChannelTemplate{Alias: "chan_help", Type: "text", Name: "help", ParentAlias: "cat_support", Topic: "Public support questions and quick answers.", Position: 30},
			ChannelTemplate{Alias: "chan_tickets", Type: "text", Name: "open-a-ticket", ParentAlias: "cat_support", Topic: "Create private support tickets.", Position: 32},
		)
		template.TicketPanels = append(template.TicketPanels, TicketPanelTemplate{
			Alias: "ticket_panel_support", PanelChannelAlias: "chan_tickets", Title: "{{ticket_panel_title}}", Body: "{{ticket_panel_body}}",
			StaffRoleAliases: []string{"role_support", "role_triage"}, TargetCategoryAlias: "cat_tickets", TranscriptPolicy: "retain",
			Departments: []TicketDepartmentTemplate{
				{ID: "general", Label: "General Help", Description: "Questions, issues, and account help.", StaffRoleAliases: []string{"role_support"}, InitialPriority: "normal"},
				{ID: "urgent", Label: "Urgent", Description: "Time-sensitive issues that need staff attention.", StaffRoleAliases: []string{"role_triage", "role_support"}, InitialPriority: "high"},
			},
		})
		template.Panda.RoleProfiles["role_support"] = "moderator"
	}
	template.FeatureIDs = uniqueStrings(template.FeatureIDs)
	return template
}

func mergeScratchDefaults(input map[string]string) map[string]string {
	intent := firstNonEmpty(input[scratchIntentKey], scratchIntentCommunity)
	merged := scratchDefaultVariables(intent)
	for key, value := range input {
		if strings.TrimSpace(key) != "" {
			merged[key] = strings.TrimSpace(value)
		}
	}
	merged[wizardModeKey] = wizardModeScratch
	return merged
}

func scratchPurpose(intent string) string {
	switch strings.TrimSpace(intent) {
	case scratchIntentCreator:
		return "a creator community"
	case scratchIntentGaming:
		return "a gaming group"
	case scratchIntentSupport:
		return "a support desk"
	case scratchIntentProduct:
		return "a product community"
	case scratchIntentStudy:
		return "a study or course community"
	case scratchIntentCustom:
		return "a custom Discord server"
	default:
		return "a friendly community"
	}
}

func scratchDefaultModules(intent string) []string {
	switch strings.TrimSpace(intent) {
	case scratchIntentCreator:
		return []string{scratchModuleOnboarding, scratchModuleStaff, scratchModuleAnnouncement, scratchModuleMedia, scratchModuleEvents, scratchModuleFeedback}
	case scratchIntentGaming:
		return []string{scratchModuleOnboarding, scratchModuleStaff, scratchModuleAnnouncement, scratchModuleMedia, scratchModuleEvents, scratchModuleVoice}
	case scratchIntentSupport:
		return []string{scratchModuleStaff, scratchModuleAnnouncement, scratchModuleSupport, scratchModuleKnowledge, scratchModuleFeedback}
	case scratchIntentProduct:
		return []string{scratchModuleOnboarding, scratchModuleStaff, scratchModuleAnnouncement, scratchModuleSupport, scratchModuleKnowledge, scratchModuleFeedback}
	case scratchIntentStudy:
		return []string{scratchModuleOnboarding, scratchModuleStaff, scratchModuleAnnouncement, scratchModuleEvents, scratchModuleVoice, scratchModuleKnowledge}
	case scratchIntentCustom:
		return []string{scratchModuleStaff, scratchModuleFeedback}
	default:
		return []string{scratchModuleOnboarding, scratchModuleStaff, scratchModuleAnnouncement, scratchModuleMedia, scratchModuleFeedback}
	}
}

func scratchModuleSet(raw string) map[string]bool {
	result := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			result[value] = true
		}
	}
	return result
}

func scratchModuleList(raw string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
