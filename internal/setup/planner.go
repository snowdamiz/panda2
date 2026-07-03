package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
)

var (
	ErrUnknownTemplate      = errors.New("unknown setup template")
	ErrInvalidTemplate      = errors.New("invalid setup template")
	ErrPreviewBlocked       = errors.New("setup preview is blocked")
	templateVariablePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)
)

type Planner struct{}

func NewPlanner() Planner {
	return Planner{}
}

func (Planner) ValidateTemplate(template Template) error {
	if strings.TrimSpace(template.ID) == "" {
		return fmt.Errorf("%w: template id is required", ErrInvalidTemplate)
	}
	if template.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidTemplate, template.SchemaVersion)
	}
	if template.TemplateVersion <= 0 {
		return fmt.Errorf("%w: template version is required", ErrInvalidTemplate)
	}
	if strings.TrimSpace(template.Name) == "" {
		return fmt.Errorf("%w: template name is required", ErrInvalidTemplate)
	}
	aliases := map[string]string{}
	addAlias := func(kind, alias string) error {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return fmt.Errorf("%w: %s alias is required", ErrInvalidTemplate, kind)
		}
		if previous := aliases[alias]; previous != "" {
			return fmt.Errorf("%w: alias %q is used by both %s and %s", ErrInvalidTemplate, alias, previous, kind)
		}
		aliases[alias] = kind
		return nil
	}
	for _, role := range template.Roles {
		if err := addAlias(ResourceTypeRole, role.Alias); err != nil {
			return err
		}
		if strings.TrimSpace(role.Name) == "" {
			return fmt.Errorf("%w: role %s name is required", ErrInvalidTemplate, role.Alias)
		}
		for _, permission := range role.Permissions {
			if strings.EqualFold(strings.TrimSpace(permission), "ADMINISTRATOR") {
				return fmt.Errorf("%w: role %s requests Administrator, which templates refuse by default", ErrInvalidTemplate, role.Alias)
			}
		}
		if strings.TrimSpace(role.Color) != "" {
			if _, err := parseHexColor(role.Color); err != nil {
				return fmt.Errorf("%w: role %s color: %v", ErrInvalidTemplate, role.Alias, err)
			}
		}
	}
	for _, category := range template.Categories {
		if err := addAlias(ResourceTypeCategory, category.Alias); err != nil {
			return err
		}
		if strings.TrimSpace(category.Name) == "" {
			return fmt.Errorf("%w: category %s name is required", ErrInvalidTemplate, category.Alias)
		}
		if err := validateOverwrites(category.Overwrites, aliases); err != nil {
			return err
		}
	}
	for _, channel := range template.Channels {
		if err := addAlias(ResourceTypeChannel, channel.Alias); err != nil {
			return err
		}
		if strings.TrimSpace(channel.Name) == "" {
			return fmt.Errorf("%w: channel %s name is required", ErrInvalidTemplate, channel.Alias)
		}
		switch normalizeChannelType(channel.Type) {
		case "text", "announcement", "voice", "stage", "forum", "media":
		default:
			return fmt.Errorf("%w: channel %s has unsupported type %q", ErrInvalidTemplate, channel.Alias, channel.Type)
		}
		if channel.ParentAlias != "" && aliases[channel.ParentAlias] != ResourceTypeCategory {
			return fmt.Errorf("%w: channel %s parent alias %q is not a category", ErrInvalidTemplate, channel.Alias, channel.ParentAlias)
		}
		if err := validateOverwrites(channel.Overwrites, aliases); err != nil {
			return err
		}
	}
	for _, panel := range template.TicketPanels {
		if err := addAlias(ResourceTypeTicketPanel, panel.Alias); err != nil {
			return err
		}
		if aliases[panel.PanelChannelAlias] != ResourceTypeChannel {
			return fmt.Errorf("%w: ticket panel %s channel alias %q is not a channel", ErrInvalidTemplate, panel.Alias, panel.PanelChannelAlias)
		}
		if panel.TargetCategoryAlias != "" && aliases[panel.TargetCategoryAlias] != ResourceTypeCategory {
			return fmt.Errorf("%w: ticket panel %s target category %q is not a category", ErrInvalidTemplate, panel.Alias, panel.TargetCategoryAlias)
		}
		if len(panel.Departments) == 0 {
			return fmt.Errorf("%w: ticket panel %s needs at least one department", ErrInvalidTemplate, panel.Alias)
		}
		departmentIDs := map[string]struct{}{}
		for _, department := range panel.Departments {
			id := strings.TrimSpace(department.ID)
			if id == "" {
				return fmt.Errorf("%w: ticket panel %s department id is required", ErrInvalidTemplate, panel.Alias)
			}
			if _, exists := departmentIDs[id]; exists {
				return fmt.Errorf("%w: ticket panel %s has duplicate department %q", ErrInvalidTemplate, panel.Alias, id)
			}
			departmentIDs[id] = struct{}{}
			for _, alias := range append(panel.StaffRoleAliases, department.StaffRoleAliases...) {
				if aliases[alias] != ResourceTypeRole {
					return fmt.Errorf("%w: ticket panel %s staff alias %q is not a role", ErrInvalidTemplate, panel.Alias, alias)
				}
			}
		}
	}
	for _, flow := range template.OnboardingFlows {
		if err := addAlias(ResourceTypeOnboardingFlow, flow.Alias); err != nil {
			return err
		}
		if aliases[flow.WelcomeChannelAlias] != ResourceTypeChannel {
			return fmt.Errorf("%w: onboarding flow %s welcome channel %q is not a channel", ErrInvalidTemplate, flow.Alias, flow.WelcomeChannelAlias)
		}
		if flow.RulesChannelAlias != "" && aliases[flow.RulesChannelAlias] != ResourceTypeChannel {
			return fmt.Errorf("%w: onboarding flow %s rules channel %q is not a channel", ErrInvalidTemplate, flow.Alias, flow.RulesChannelAlias)
		}
		if aliases[flow.VerifiedRoleAlias] != ResourceTypeRole {
			return fmt.Errorf("%w: onboarding flow %s verified role %q is not a role", ErrInvalidTemplate, flow.Alias, flow.VerifiedRoleAlias)
		}
		if flow.NewcomerRoleAlias != "" && aliases[flow.NewcomerRoleAlias] != ResourceTypeRole {
			return fmt.Errorf("%w: onboarding flow %s newcomer role %q is not a role", ErrInvalidTemplate, flow.Alias, flow.NewcomerRoleAlias)
		}
	}
	return nil
}

func validateOverwrites(overwrites []OverwriteTemplate, aliases map[string]string) error {
	for _, overwrite := range overwrites {
		target := strings.TrimSpace(overwrite.TargetAlias)
		if target == "" {
			return fmt.Errorf("%w: overwrite target alias is required", ErrInvalidTemplate)
		}
		if target == "@everyone" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(overwrite.TargetType)) {
		case "role":
			if aliases[target] != ResourceTypeRole {
				return fmt.Errorf("%w: overwrite target %q is not a role", ErrInvalidTemplate, target)
			}
		case "member", "user":
			if !looksLikeDiscordID(target) {
				return fmt.Errorf("%w: member overwrite target %q must be a Discord user id", ErrInvalidTemplate, target)
			}
		default:
			return fmt.Errorf("%w: overwrite target type must be role or member", ErrInvalidTemplate)
		}
	}
	return nil
}

func (p Planner) RenderTemplate(template Template, variables map[string]string) (Template, map[string]string, error) {
	merged := map[string]string{}
	for key, value := range template.DefaultVariables {
		merged[key] = value
	}
	for key, value := range variables {
		key = strings.TrimSpace(key)
		if key != "" {
			merged[key] = strings.TrimSpace(value)
		}
	}
	for _, variable := range template.EditableVariables {
		if !variable.Required {
			continue
		}
		if strings.TrimSpace(merged[variable.Key]) == "" {
			return Template{}, nil, fmt.Errorf("%w: required variable %q is empty", ErrInvalidTemplate, variable.Key)
		}
	}
	raw, err := json.Marshal(template)
	if err != nil {
		return Template{}, nil, err
	}
	renderedRaw := templateVariablePattern.ReplaceAllStringFunc(string(raw), func(match string) string {
		parts := templateVariablePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return escapeJSONStringValue(merged[parts[1]])
	})
	var rendered Template
	if err := json.Unmarshal([]byte(renderedRaw), &rendered); err != nil {
		return Template{}, nil, err
	}
	if err := p.ValidateTemplate(rendered); err != nil {
		return Template{}, nil, err
	}
	return rendered, merged, nil
}

func (p Planner) Plan(template Template, variables map[string]string, snapshot GuildSnapshot, stored []store.GuildSetupResource) (Preview, map[string]string, error) {
	rendered, merged, err := p.RenderTemplate(template, variables)
	if err != nil {
		return Preview{}, nil, err
	}
	resourceByAlias := map[string]store.GuildSetupResource{}
	for _, resource := range stored {
		resourceByAlias[resource.ManagedAlias] = resource
	}
	roleNameMatches := roleMatchesByName(snapshot.Roles)
	channelNameMatches := channelMatchesByName(snapshot.Channels)

	var plan []PlanStep
	var warnings []string
	blocked := false
	add := func(step PlanStep) {
		if step.ID == "" {
			step.ID = step.ResourceType + ":" + step.Alias
		}
		if step.Hash == "" {
			step.Hash = stableHash(step.Payload)
		}
		plan = append(plan, step)
	}
	for _, role := range rendered.Roles {
		payload := map[string]any{"role": role}
		objectID, action, matchWarnings, conflict := planResourceAction(resourceByAlias[role.Alias], roleNameMatches[normalizeDiscordName(role.Name)], stableHash(payload))
		if conflict {
			blocked = true
		}
		warnings = append(warnings, matchWarnings...)
		add(PlanStep{Action: action, ResourceType: ResourceTypeRole, Alias: role.Alias, Name: role.Name, ObjectID: objectID, Hash: stableHash(payload), Payload: payload, Warnings: matchWarnings})
	}
	for _, category := range rendered.Categories {
		payload := map[string]any{"category": category}
		objectID, action, matchWarnings, conflict := planResourceAction(resourceByAlias[category.Alias], channelNameMatches[normalizeChannelKey("category", category.Name)], stableHash(payload))
		if conflict {
			blocked = true
		}
		warnings = append(warnings, matchWarnings...)
		add(PlanStep{Action: action, ResourceType: ResourceTypeCategory, Alias: category.Alias, Name: category.Name, ObjectID: objectID, Hash: stableHash(payload), Payload: payload, Warnings: matchWarnings})
	}
	for _, channel := range rendered.Channels {
		payload := map[string]any{"channel": channel}
		objectID, action, matchWarnings, conflict := planResourceAction(resourceByAlias[channel.Alias], channelNameMatches[normalizeChannelKey(channel.Type, channel.Name)], stableHash(payload))
		if conflict {
			blocked = true
		}
		warnings = append(warnings, matchWarnings...)
		dependencies := []string{}
		if channel.ParentAlias != "" {
			dependencies = append(dependencies, ResourceTypeCategory+":"+channel.ParentAlias)
		}
		add(PlanStep{Action: action, ResourceType: ResourceTypeChannel, Alias: channel.Alias, Name: channel.Name, ObjectID: objectID, DependsOn: dependencies, Hash: stableHash(payload), Payload: payload, Warnings: matchWarnings})
		for _, message := range channel.StarterMessages {
			messageAlias := channel.Alias + ".starter"
			if strings.TrimSpace(message.Alias) != "" {
				messageAlias = channel.Alias + ".starter." + strings.TrimSpace(message.Alias)
			}
			messagePayload := map[string]any{"channel_alias": channel.Alias, "message": message}
			add(PlanStep{Action: PlanActionCreate, ResourceType: ResourceTypeStarterMessage, Alias: messageAlias, Name: "Starter message for #" + channel.Name, DependsOn: []string{ResourceTypeChannel + ":" + channel.Alias}, Hash: stableHash(messagePayload), Payload: messagePayload})
		}
	}
	if hasPandaConfig(rendered.Panda) {
		payload := map[string]any{"panda": rendered.Panda}
		add(PlanStep{Action: PlanActionUpdate, ResourceType: ResourceTypePandaConfig, Alias: "panda_config", Name: "Panda access and prompt settings", Hash: stableHash(payload), Payload: payload})
	}
	for _, panel := range rendered.TicketPanels {
		payload := map[string]any{"ticket_panel": panel}
		dependencies := []string{ResourceTypeChannel + ":" + panel.PanelChannelAlias}
		if panel.TargetCategoryAlias != "" {
			dependencies = append(dependencies, ResourceTypeCategory+":"+panel.TargetCategoryAlias)
		}
		for _, roleAlias := range panel.StaffRoleAliases {
			dependencies = append(dependencies, ResourceTypeRole+":"+roleAlias)
		}
		add(PlanStep{Action: PlanActionCreate, ResourceType: ResourceTypeTicketPanel, Alias: panel.Alias, Name: panel.Title, DependsOn: dependencies, Hash: stableHash(payload), Payload: payload})
	}
	for _, flow := range rendered.OnboardingFlows {
		payload := map[string]any{"onboarding_flow": flow}
		dependencies := []string{ResourceTypeChannel + ":" + flow.WelcomeChannelAlias, ResourceTypeRole + ":" + flow.VerifiedRoleAlias}
		if flow.RulesChannelAlias != "" {
			dependencies = append(dependencies, ResourceTypeChannel+":"+flow.RulesChannelAlias)
		}
		if flow.NewcomerRoleAlias != "" {
			dependencies = append(dependencies, ResourceTypeRole+":"+flow.NewcomerRoleAlias)
		}
		add(PlanStep{Action: PlanActionCreate, ResourceType: ResourceTypeOnboardingFlow, Alias: flow.Alias, Name: flow.VerificationMode, DependsOn: dependencies, Hash: stableHash(payload), Payload: payload})
	}
	sortPlan(plan)
	preview := Preview{
		TemplateID:   rendered.ID,
		TemplateName: rendered.Name,
		Summary:      summarizePlan(plan),
		Groups:       previewGroups(plan),
		Warnings:     uniqueStrings(warnings),
		Blocked:      blocked,
		Plan:         plan,
		GeneratedAt:  time.Now().UTC(),
	}
	return preview, merged, nil
}

func planResourceAction(stored store.GuildSetupResource, matches []matchedResource, hash string) (string, string, []string, bool) {
	if strings.TrimSpace(stored.ObjectID) != "" {
		if stored.LastAppliedHash == hash {
			return stored.ObjectID, PlanActionSkip, nil, false
		}
		return stored.ObjectID, PlanActionUpdate, nil, false
	}
	if len(matches) == 1 {
		return matches[0].ID, PlanActionUpdate, []string{fmt.Sprintf("Matched existing %s %q by name; confirm preview before Panda manages it.", matches[0].Type, matches[0].Name)}, false
	}
	if len(matches) > 1 {
		return "", PlanActionSkip, []string{fmt.Sprintf("Found %d existing resources named %q; rename or map one before applying.", len(matches), matches[0].Name)}, true
	}
	return "", PlanActionCreate, nil, false
}

type matchedResource struct {
	ID   string
	Name string
	Type string
}

func roleMatchesByName(roles []RoleState) map[string][]matchedResource {
	result := map[string][]matchedResource{}
	for _, role := range roles {
		if role.Managed {
			continue
		}
		key := normalizeDiscordName(role.Name)
		result[key] = append(result[key], matchedResource{ID: role.ID, Name: role.Name, Type: ResourceTypeRole})
	}
	return result
}

func channelMatchesByName(channels []ChannelState) map[string][]matchedResource {
	result := map[string][]matchedResource{}
	for _, channel := range channels {
		key := normalizeChannelKey(channel.Type, channel.Name)
		result[key] = append(result[key], matchedResource{ID: channel.ID, Name: channel.Name, Type: channel.Type})
	}
	return result
}

func sortPlan(plan []PlanStep) {
	rank := map[string]int{
		ResourceTypeRole:           10,
		ResourceTypeCategory:       20,
		ResourceTypeChannel:        30,
		ResourceTypeStarterMessage: 40,
		ResourceTypePandaConfig:    50,
		ResourceTypeTicketPanel:    60,
		ResourceTypeOnboardingFlow: 70,
	}
	sort.SliceStable(plan, func(i, j int) bool {
		left, right := rank[plan[i].ResourceType], rank[plan[j].ResourceType]
		if left != right {
			return left < right
		}
		if plan[i].Alias != plan[j].Alias {
			return plan[i].Alias < plan[j].Alias
		}
		return plan[i].Name < plan[j].Name
	})
}

func summarizePlan(plan []PlanStep) PreviewSummary {
	var summary PreviewSummary
	for _, step := range plan {
		switch step.ResourceType {
		case ResourceTypeRole:
			summary.Roles++
		case ResourceTypeCategory:
			summary.Categories++
		case ResourceTypeChannel:
			summary.Channels++
		case ResourceTypeTicketPanel:
			summary.TicketPanels++
		case ResourceTypeOnboardingFlow:
			summary.OnboardingFlows++
		case ResourceTypeStarterMessage:
			summary.StarterMessages++
		case ResourceTypePandaConfig:
			summary.PandaConfigItems++
		}
		switch step.Action {
		case PlanActionCreate:
			summary.Creates++
		case PlanActionUpdate:
			summary.Updates++
		case PlanActionReuse:
			summary.Reuses++
		case PlanActionSkip:
			summary.Skips++
		}
	}
	return summary
}

func previewGroups(plan []PlanStep) []PreviewGroup {
	names := map[string]string{
		ResourceTypeRole:           "Roles",
		ResourceTypeCategory:       "Categories",
		ResourceTypeChannel:        "Channels",
		ResourceTypeStarterMessage: "Starter Messages",
		ResourceTypePandaConfig:    "Panda Settings",
		ResourceTypeTicketPanel:    "Ticketing",
		ResourceTypeOnboardingFlow: "Onboarding",
	}
	indexByName := map[string]int{}
	var groups []PreviewGroup
	for _, step := range plan {
		groupName := names[step.ResourceType]
		if groupName == "" {
			groupName = "Other"
		}
		index, ok := indexByName[groupName]
		if !ok {
			index = len(groups)
			indexByName[groupName] = index
			groups = append(groups, PreviewGroup{Name: groupName})
		}
		groups[index].Items = append(groups[index].Items, PreviewGroupItem{
			Action:      step.Action,
			Type:        step.ResourceType,
			Alias:       step.Alias,
			Name:        step.Name,
			ObjectID:    step.ObjectID,
			Description: stepDescription(step),
			Warnings:    step.Warnings,
		})
	}
	return groups
}

func stepDescription(step PlanStep) string {
	switch step.ResourceType {
	case ResourceTypeRole:
		return "Discord role"
	case ResourceTypeCategory:
		return "Discord category"
	case ResourceTypeChannel:
		return "Discord " + strings.TrimSpace(fmt.Sprint(nestedPayload(step.Payload, "channel", "type"))) + " channel"
	case ResourceTypeTicketPanel:
		return "Persistent ticket panel and departments"
	case ResourceTypeOnboardingFlow:
		return "Reusable member onboarding flow"
	case ResourceTypeStarterMessage:
		return "Safe starter copy with broad mentions suppressed"
	case ResourceTypePandaConfig:
		return "Panda prompt, access, and channel settings"
	default:
		return step.ResourceType
	}
}

func nestedPayload(payload map[string]any, objectKey, field string) any {
	if payload == nil {
		return ""
	}
	value, _ := payload[objectKey].(map[string]any)
	if value == nil {
		raw, err := json.Marshal(payload[objectKey])
		if err != nil {
			return ""
		}
		_ = json.Unmarshal(raw, &value)
	}
	return value[field]
}

func hasPandaConfig(config PandaConfigTemplate) bool {
	return strings.TrimSpace(config.PromptOverlay) != "" ||
		len(config.ChannelRules) > 0 ||
		len(config.RoleProfiles) > 0 ||
		len(config.ToolAccess) > 0 ||
		len(config.Budgets) > 0
}

func stableHash(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(fmt.Sprint(value))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func parseHexColor(value string) (int, error) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "#"))
	if value == "" {
		return 0, nil
	}
	if len(value) != 6 {
		return 0, fmt.Errorf("must be #RRGGBB")
	}
	parsed, err := strconv.ParseInt(value, 16, 32)
	if err != nil {
		return 0, err
	}
	return int(parsed), nil
}

func normalizeChannelType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "category", "guild_category":
		return "category"
	case "announcement", "news", "guild_news":
		return "announcement"
	case "voice", "guild_voice":
		return "voice"
	case "stage", "stage_voice", "guild_stage_voice":
		return "stage"
	case "forum", "guild_forum":
		return "forum"
	case "media", "guild_media":
		return "media"
	default:
		return "text"
	}
}

func normalizeChannelKey(channelType, name string) string {
	return normalizeChannelType(channelType) + ":" + normalizeDiscordName(name)
}

func normalizeDiscordName(name string) string {
	name = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(name, "#"), "@"))
	return strings.ToLower(name)
}

func looksLikeDiscordID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 10 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func escapeJSONStringValue(value string) string {
	raw, _ := json.Marshal(value)
	if len(raw) < 2 {
		return value
	}
	return string(raw[1 : len(raw)-1])
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
