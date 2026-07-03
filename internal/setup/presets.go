package setup

import "github.com/sn0w/panda2/internal/features"

func BuiltInTemplates() []Template {
	return []Template{
		minimalCommunityTemplate(),
		creatorHubTemplate(),
		gamingServerTemplate(),
		supportDeskTemplate(),
		productCommunityTemplate(),
		studyCourseTemplate(),
	}
}

func BuiltInTemplateByID(id string) (Template, bool) {
	for _, template := range BuiltInTemplates() {
		if template.ID == id {
			return template, true
		}
	}
	return Template{}, false
}

func baseVariables(purpose string) map[string]string {
	return map[string]string{
		"server_purpose":          purpose,
		"member_role":             "Member",
		"verified_role":           "Verified",
		"newcomer_role":           "Newcomer",
		"moderator_role":          "Moderator",
		"admin_role":              "Admin",
		"support_role":            "Support Team",
		"triage_role":             "Triage",
		"rules_copy":              "Be kind, keep conversations on topic, respect privacy, and follow Discord's Terms of Service.",
		"welcome_copy":            "Welcome to the server. Read the rules, pick any roles that fit, and make yourself comfortable.",
		"ticket_panel_title":      "Need help?",
		"ticket_panel_body":       "Open a ticket and the right team will help you privately.",
		"announcement_topic":      "Important updates for the community.",
		"general_topic":           "Friendly conversation and day-to-day discussion.",
		"feedback_topic":          "Share ideas, requests, and constructive feedback.",
		"verification_strictness": "rules",
	}
}

func defaultEditableVariables() []TemplateVariable {
	return []TemplateVariable{
		{Key: "server_purpose", Label: "Server purpose", Type: "text", Required: true},
		{Key: "member_role", Label: "Member role", Type: "text", Required: true},
		{Key: "moderator_role", Label: "Moderator role", Type: "text", Required: true},
		{Key: "admin_role", Label: "Admin role", Type: "text", Required: true},
		{Key: "support_role", Label: "Support team role", Type: "text", Required: false},
		{Key: "welcome_copy", Label: "Welcome copy", Type: "textarea", Required: true},
		{Key: "rules_copy", Label: "Rules copy", Type: "textarea", Required: true},
		{Key: "verification_strictness", Label: "Verification mode", Type: "select", Required: true, Options: []string{"rules", "role_selection", "rules_and_roles"}},
	}
}

func communityRoles() []RoleTemplate {
	return []RoleTemplate{
		{Alias: "role_admin", Name: "{{admin_role}}", Color: "#ed4245", Hoist: true, Mentionable: false, Permissions: []string{"MANAGE_CHANNELS", "MANAGE_ROLES", "MANAGE_MESSAGES"}, Position: 80, Profile: "admin"},
		{Alias: "role_moderator", Name: "{{moderator_role}}", Color: "#5865f2", Hoist: true, Mentionable: true, Permissions: []string{"MANAGE_MESSAGES", "MODERATE_MEMBERS"}, Position: 70, Profile: "moderator"},
		{Alias: "role_member", Name: "{{member_role}}", Color: "#57f287", Position: 20},
		{Alias: "role_verified", Name: "{{verified_role}}", Color: "#fee75c", Position: 10},
		{Alias: "role_newcomer", Name: "{{newcomer_role}}", Color: "#99aab5", Position: 5},
		{Alias: "role_announcements", Name: "Announcements", Color: "#57f287", Mentionable: true, Position: 24},
		{Alias: "role_events", Name: "Events", Color: "#5865f2", Mentionable: true, Position: 23},
		{Alias: "role_projects", Name: "Projects", Color: "#fee75c", Mentionable: true, Position: 22},
	}
}

func staffOverwrite() []OverwriteTemplate {
	return []OverwriteTemplate{
		{TargetAlias: "@everyone", TargetType: "role", Deny: []string{"VIEW_CHANNEL"}},
		{TargetAlias: "role_moderator", TargetType: "role", Allow: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY", "MANAGE_MESSAGES"}},
		{TargetAlias: "role_admin", TargetType: "role", Allow: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY", "MANAGE_CHANNELS"}},
	}
}

func memberOverwrite() []OverwriteTemplate {
	return []OverwriteTemplate{
		{TargetAlias: "@everyone", TargetType: "role", Deny: []string{"VIEW_CHANNEL"}},
		{TargetAlias: "role_member", TargetType: "role", Allow: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY"}},
		{TargetAlias: "role_verified", TargetType: "role", Allow: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY"}},
	}
}

func baseCategories(includeSupport bool) []CategoryTemplate {
	categories := []CategoryTemplate{
		{Alias: "cat_welcome", Name: "Start Here", Position: 10},
		{Alias: "cat_community", Name: "Community", Position: 20, Overwrites: memberOverwrite()},
		{Alias: "cat_staff", Name: "Staff", Position: 90, Overwrites: staffOverwrite()},
	}
	if includeSupport {
		categories = append(categories, CategoryTemplate{Alias: "cat_support", Name: "Support", Position: 30, Overwrites: memberOverwrite()})
		categories = append(categories, CategoryTemplate{Alias: "cat_tickets", Name: "Tickets", Position: 80, Overwrites: staffOverwrite()})
	}
	return categories
}

func minimalCommunityTemplate() Template {
	return Template{
		ID:                "minimal_community",
		SchemaVersion:     SchemaVersion,
		TemplateVersion:   1,
		Name:              "Minimal Community",
		Description:       "Rules, announcements, general chat, media, feedback, staff area, and low-friction onboarding.",
		ReleaseState:      "stable",
		DefaultVariables:  baseVariables("a friendly community"),
		EditableVariables: defaultEditableVariables(),
		FeatureIDs:        []string{features.AssistantChat, features.Threads, features.Polls, features.AdminSetup, features.AdminAccessControl, features.DiscordMessages, features.DiscordChannelTools, features.DiscordRoleManagement},
		Roles:             communityRoles(),
		Categories:        baseCategories(false),
		Channels: []ChannelTemplate{
			{Alias: "chan_rules", Type: "text", Name: "rules", ParentAlias: "cat_welcome", Topic: "{{rules_copy}}", Position: 10, StarterMessages: []StarterMessage{{Alias: "rules", Content: "{{rules_copy}}"}}},
			{Alias: "chan_announcements", Type: "text", Name: "announcements", ParentAlias: "cat_welcome", Topic: "{{announcement_topic}}", Position: 20},
			{Alias: "chan_welcome", Type: "text", Name: "welcome", ParentAlias: "cat_welcome", Topic: "New member onboarding and first steps.", Position: 30, StarterMessages: []StarterMessage{{Alias: "welcome", Content: "{{welcome_copy}}"}}},
			{Alias: "chan_general", Type: "text", Name: "general", ParentAlias: "cat_community", Topic: "{{general_topic}}", Position: 40},
			{Alias: "chan_media", Type: "text", Name: "media", ParentAlias: "cat_community", Topic: "Share images, links, and community moments.", Position: 50},
			{Alias: "chan_feedback", Type: "text", Name: "feedback", ParentAlias: "cat_community", Topic: "{{feedback_topic}}", Position: 60},
			{Alias: "chan_staff", Type: "text", Name: "staff-chat", ParentAlias: "cat_staff", Topic: "Private staff coordination.", Position: 90},
			{Alias: "chan_mod_logs", Type: "text", Name: "mod-log", ParentAlias: "cat_staff", Topic: "Moderation notes and operational context.", Position: 91},
		},
		Panda: PandaConfigTemplate{
			PromptOverlay: "This server is {{server_purpose}}. Keep replies helpful, concise, and community-minded.",
			ChannelRules:  map[string]string{"chan_staff": "allow", "chan_mod_logs": "allow"},
			RoleProfiles:  map[string]string{"role_admin": "admin", "role_moderator": "moderator"},
		},
		OnboardingFlows: []OnboardingFlowTemplate{
			{
				Alias: "onboarding_default", WelcomeChannelAlias: "chan_welcome", RulesChannelAlias: "chan_rules", VerifiedRoleAlias: "role_verified", NewcomerRoleAlias: "role_newcomer",
				VerificationMode: "{{verification_strictness}}", IntroPrompt: "{{welcome_copy}}", CompletionMessage: "You are all set. Welcome in.",
				Steps: []OnboardingStepTemplate{
					{ID: "rules", Type: "rules_ack", Prompt: "{{rules_copy}}", Required: true},
					{ID: "interests", Type: "role_selection", Prompt: "Choose optional roles for updates you want to see.", Required: false, RoleAliases: []string{"role_announcements", "role_events", "role_projects"}, MinSelections: 0, MaxSelections: 3},
				},
			},
		},
	}
}

func creatorHubTemplate() Template {
	template := minimalCommunityTemplate()
	template.ID = "creator_hub"
	template.Name = "Creator Hub"
	template.Description = "Announcements, clips, collab, fan chat, content requests, events, media showcase, and mod queue."
	template.DefaultVariables = baseVariables("a creator community")
	template.Channels = append([]ChannelTemplate{
		{Alias: "chan_clips", Type: "text", Name: "clips", ParentAlias: "cat_community", Topic: "Share favorite clips and highlights.", Position: 61},
		{Alias: "chan_collab", Type: "text", Name: "collab", ParentAlias: "cat_community", Topic: "Collaborations, shoutouts, and creative matchmaking.", Position: 62},
		{Alias: "chan_requests", Type: "text", Name: "content-requests", ParentAlias: "cat_community", Topic: "Suggest topics, guests, formats, and future content.", Position: 63},
		{Alias: "chan_events", Type: "text", Name: "events", ParentAlias: "cat_welcome", Topic: "Upcoming streams, drops, and community events.", Position: 31},
	}, template.Channels...)
	return template
}

func gamingServerTemplate() Template {
	template := minimalCommunityTemplate()
	template.ID = "gaming_server"
	template.Name = "Gaming Server"
	template.Description = "Welcome, roles, LFG, voice lobbies, clips, announcements, rules, and moderation logs."
	template.DefaultVariables = baseVariables("a gaming group")
	template.Channels = append(template.Channels,
		ChannelTemplate{Alias: "chan_lfg", Type: "text", Name: "lfg", ParentAlias: "cat_community", Topic: "Find a group or queue partner.", Position: 64},
		ChannelTemplate{Alias: "chan_builds", Type: "text", Name: "builds-and-loadouts", ParentAlias: "cat_community", Topic: "Share strategies, builds, and loadouts.", Position: 65},
		ChannelTemplate{Alias: "voice_lobby_1", Type: "voice", Name: "Lobby 1", ParentAlias: "cat_community", Position: 70, UserLimit: 8},
		ChannelTemplate{Alias: "voice_lobby_2", Type: "voice", Name: "Lobby 2", ParentAlias: "cat_community", Position: 71, UserLimit: 8},
		ChannelTemplate{Alias: "voice_afk", Type: "voice", Name: "AFK", ParentAlias: "cat_community", Position: 72, UserLimit: 0},
	)
	return template
}

func supportDeskTemplate() Template {
	vars := baseVariables("a support desk")
	vars["support_role"] = "Support Team"
	vars["triage_role"] = "Triage"
	template := Template{
		ID:                "support_desk",
		SchemaVersion:     SchemaVersion,
		TemplateVersion:   1,
		Name:              "Support Desk",
		Description:       "Public help, FAQ, ticket panel, private ticket category, staff role, triage role, and retained lifecycle state.",
		ReleaseState:      "stable",
		DefaultVariables:  vars,
		EditableVariables: defaultEditableVariables(),
		FeatureIDs:        []string{features.AssistantChat, features.Knowledge, features.AdminSetup, features.AdminAccessControl, features.DiscordMessages, features.DiscordChannelTools, features.DiscordRoleManagement, features.ComposedTools},
		Roles: append(communityRoles(),
			RoleTemplate{Alias: "role_support", Name: "{{support_role}}", Color: "#57f287", Hoist: true, Mentionable: true, Permissions: []string{"MANAGE_MESSAGES"}, Position: 60, Profile: "moderator"},
			RoleTemplate{Alias: "role_triage", Name: "{{triage_role}}", Color: "#fee75c", Hoist: true, Mentionable: true, Position: 55},
		),
		Categories: baseCategories(true),
		Channels: []ChannelTemplate{
			{Alias: "chan_rules", Type: "text", Name: "rules", ParentAlias: "cat_welcome", Topic: "{{rules_copy}}", Position: 10, StarterMessages: []StarterMessage{{Alias: "rules", Content: "{{rules_copy}}"}}},
			{Alias: "chan_help", Type: "text", Name: "help", ParentAlias: "cat_support", Topic: "Public support questions and quick answers.", Position: 30},
			{Alias: "chan_faq", Type: "text", Name: "faq", ParentAlias: "cat_support", Topic: "Frequently asked questions and canonical answers.", Position: 31, StarterMessages: []StarterMessage{{Alias: "faq", Content: "Add your most common answers here. Panda can use this as support context when knowledge is enabled."}}},
			{Alias: "chan_tickets", Type: "text", Name: "open-a-ticket", ParentAlias: "cat_support", Topic: "Create private support tickets.", Position: 32},
			{Alias: "chan_support_staff", Type: "text", Name: "support-staff", ParentAlias: "cat_staff", Topic: "Private support staff coordination.", Position: 90},
			{Alias: "chan_support_log", Type: "text", Name: "support-log", ParentAlias: "cat_staff", Topic: "Ticket lifecycle and support audit trail.", Position: 91},
		},
		TicketPanels: []TicketPanelTemplate{
			{
				Alias: "ticket_panel_support", PanelChannelAlias: "chan_tickets", Title: "{{ticket_panel_title}}", Body: "{{ticket_panel_body}}",
				StaffRoleAliases: []string{"role_support", "role_triage"}, TargetCategoryAlias: "cat_tickets", TranscriptPolicy: "retain",
				Departments: []TicketDepartmentTemplate{
					{ID: "general", Label: "General Help", Description: "Questions, issues, and account help.", StaffRoleAliases: []string{"role_support"}, InitialPriority: "normal"},
					{ID: "billing", Label: "Billing", Description: "Payments, plans, invoices, and account access.", StaffRoleAliases: []string{"role_triage", "role_support"}, InitialPriority: "high"},
				},
			},
		},
		Panda: PandaConfigTemplate{
			PromptOverlay: "This server is {{server_purpose}}. Prioritize accurate support guidance, cite known server docs when possible, and avoid overpromising.",
			RoleProfiles:  map[string]string{"role_admin": "admin", "role_moderator": "moderator", "role_support": "moderator"},
		},
	}
	return template
}

func productCommunityTemplate() Template {
	template := supportDeskTemplate()
	template.ID = "saas_product_community"
	template.Name = "SaaS/Product Community"
	template.Description = "Announcements, changelog, support tickets, feedback, beta testers, docs/FAQ, and customer-tier roles."
	template.DefaultVariables = baseVariables("a product community")
	template.Roles = append(template.Roles,
		RoleTemplate{Alias: "role_customer", Name: "Customer", Color: "#57f287", Position: 30},
		RoleTemplate{Alias: "role_beta", Name: "Beta Tester", Color: "#5865f2", Mentionable: true, Position: 31},
	)
	template.Channels = append(template.Channels,
		ChannelTemplate{Alias: "chan_changelog", Type: "text", Name: "changelog", ParentAlias: "cat_welcome", Topic: "Product releases and updates.", Position: 21},
		ChannelTemplate{Alias: "chan_docs", Type: "text", Name: "docs-and-faq", ParentAlias: "cat_support", Topic: "Documentation links and answers.", Position: 33},
		ChannelTemplate{Alias: "chan_beta", Type: "text", Name: "beta-testers", ParentAlias: "cat_community", Topic: "Beta feedback and early access discussion.", Position: 66},
	)
	return template
}

func studyCourseTemplate() Template {
	template := minimalCommunityTemplate()
	template.ID = "study_course"
	template.Name = "Study/Course"
	template.Description = "Syllabus/resources, questions, study groups, office hours, assignments, and verified/student roles."
	template.DefaultVariables = baseVariables("a course or study group")
	template.Roles = append(template.Roles,
		RoleTemplate{Alias: "role_student", Name: "Student", Color: "#57f287", Position: 30},
		RoleTemplate{Alias: "role_ta", Name: "TA", Color: "#5865f2", Hoist: true, Mentionable: true, Position: 65, Profile: "moderator"},
	)
	template.Channels = append(template.Channels,
		ChannelTemplate{Alias: "chan_syllabus", Type: "text", Name: "syllabus-resources", ParentAlias: "cat_welcome", Topic: "Course outline, links, and resources.", Position: 22},
		ChannelTemplate{Alias: "chan_questions", Type: "text", Name: "questions", ParentAlias: "cat_community", Topic: "Ask questions and share answers.", Position: 66},
		ChannelTemplate{Alias: "chan_assignments", Type: "text", Name: "assignments", ParentAlias: "cat_community", Topic: "Assignment reminders and discussion.", Position: 67},
		ChannelTemplate{Alias: "voice_office_hours", Type: "voice", Name: "Office Hours", ParentAlias: "cat_community", Position: 73},
		ChannelTemplate{Alias: "voice_study_1", Type: "voice", Name: "Study Room 1", ParentAlias: "cat_community", Position: 74, UserLimit: 8},
	)
	return template
}
