package tools

import (
	"sort"
	"strings"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/llm"
)

type capabilityGroup struct {
	label       string
	description string
	order       int
}

// CapabilityOverviewForTools summarizes the exact model tool surface without
// forcing the response model to call an inventory/debug tool.
func CapabilityOverviewForTools(availableTools []llm.Tool, hasAdminAccess bool) string {
	if len(availableTools) == 0 {
		return ""
	}
	available := map[string]llm.Tool{}
	for _, tool := range availableTools {
		name := normalizeToolName(tool.Function.Name)
		if name != "" {
			available[name] = tool
		}
	}
	if len(available) == 0 {
		return ""
	}

	featuresByID := map[string]features.Feature{}
	featureOrder := map[string]int{}
	featureIDByToolName := map[string]string{}
	for index, feature := range features.Catalog() {
		featuresByID[feature.ID] = feature
		featureOrder[feature.ID] = index
		for _, toolName := range feature.ToolNames {
			featureIDByToolName[normalizeToolName(toolName)] = feature.ID
			featureIDByToolName[normalizeToolName(strings.NewReplacer(".", "_").Replace(toolName))] = feature.ID
		}
	}

	groups := map[string]capabilityGroup{}
	for _, definition := range DefaultDefinitions() {
		modelName := normalizeToolName(definition.ModelName())
		if _, ok := available[modelName]; !ok {
			continue
		}
		delete(available, modelName)
		featureID := firstCapabilityNonEmpty(featureIDByToolName[modelName], definition.FeatureID)
		group := capabilityGroupForDefinition(definition, featureID, featuresByID, featureOrder, hasAdminAccess)
		if group.label != "" {
			groups[group.label] = group
		}
	}
	for _, tool := range available {
		description := firstSentence(tool.Function.Description)
		if description == "" {
			description = "Custom or specialized server capability."
		}
		group := capabilityGroup{label: "Custom tools", description: description, order: len(featureOrder) + 100}
		if existing, ok := groups[group.label]; ok {
			existing.description = joinUniqueDescriptions(existing.description, group.description)
			groups[group.label] = existing
			continue
		}
		groups[group.label] = group
	}

	ordered := make([]capabilityGroup, 0, len(groups))
	for _, group := range groups {
		ordered = append(ordered, group)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].order == ordered[j].order {
			return ordered[i].label < ordered[j].label
		}
		return ordered[i].order < ordered[j].order
	})

	lines := make([]string, 0, len(ordered))
	for _, group := range ordered {
		lines = append(lines, group.label+"\n- "+group.description)
	}
	return strings.Join(lines, "\n")
}

func capabilityGroupForDefinition(definition Definition, featureID string, featuresByID map[string]features.Feature, featureOrder map[string]int, hasAdminAccess bool) capabilityGroup {
	if feature, ok := featuresByID[featureID]; ok {
		label := feature.Label
		if hasAdminAccess && featureUsesAdminPermission(feature) {
			label += " (caller has admin access)"
		}
		description := strings.TrimSpace(feature.Description)
		if description == "" {
			description = firstSentence(definition.Description)
		}
		return capabilityGroup{label: label, description: description, order: featureOrder[feature.ID]}
	}
	label := string(definition.ToolClass)
	if label == "" {
		label = "Other capabilities"
	}
	return capabilityGroup{
		label:       strings.TrimSpace(label),
		description: firstSentence(definition.Description),
		order:       len(featureOrder) + 50,
	}
}

func firstCapabilityNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func featureUsesAdminPermission(feature features.Feature) bool {
	for _, permission := range feature.PandaPermissions {
		switch permission {
		case admin.PermissionAdminConfigRead,
			admin.PermissionAdminConfigWrite,
			admin.PermissionAdminUsageRead,
			admin.PermissionAdminAuditRead,
			admin.PermissionAdminMemoryManage,
			admin.PermissionAssistantSoulWrite,
			admin.PermissionOwnerOps:
			return true
		}
	}
	return false
}

func firstSentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, separator := range []string{". ", ".\n"} {
		if index := strings.Index(value, separator); index >= 0 {
			return strings.TrimSpace(value[:index+1])
		}
	}
	return strings.TrimSuffix(value, ".") + "."
}

func joinUniqueDescriptions(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" || strings.Contains(left, right) {
		return left
	}
	return left + " " + right
}
