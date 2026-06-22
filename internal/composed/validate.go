package composed

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/tools"
)

var validName = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

func NormalizeSpec(spec Spec) Spec {
	spec.Name = strings.ToLower(strings.TrimSpace(spec.Name))
	spec.Description = strings.TrimSpace(spec.Description)
	if spec.SchemaVersion == 0 {
		spec.SchemaVersion = 1
	}
	spec.Runner.Type = strings.ToLower(strings.TrimSpace(spec.Runner.Type))
	if spec.Runner.Type == "" {
		spec.Runner.Type = RunnerDeterministic
	}
	if spec.Runner.MaxTokens <= 0 {
		spec.Runner.MaxTokens = 500
	}
	if spec.Runner.Temperature < 0 {
		spec.Runner.Temperature = 0
	}
	if spec.Safety.MaxNestedDepth <= 0 {
		spec.Safety.MaxNestedDepth = 2
	}
	if spec.Safety.CooldownSeconds < 0 {
		spec.Safety.CooldownSeconds = 0
	}
	if spec.Safety.MaxRunsPerHour < 0 {
		spec.Safety.MaxRunsPerHour = 0
	}
	if spec.Safety.DedupeWindowSeconds < 0 {
		spec.Safety.DedupeWindowSeconds = 0
	}
	return spec
}

func ParseSpec(data []byte) (Spec, error) {
	if err := rejectLegacyModelFields(data); err != nil {
		return Spec{}, err
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return Spec{}, err
	}
	return NormalizeSpec(spec), nil
}

func rejectLegacyModelFields(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, field := range []string{"model", "default_model", "classifier_model", "fallback_models"} {
		if _, ok := raw[field]; ok {
			return fmt.Errorf("%s is legacy model-routing configuration and is not supported in composed-tool specs", field)
		}
	}
	if runnerRaw, ok := raw["runner"]; ok {
		var runner map[string]json.RawMessage
		if err := json.Unmarshal(runnerRaw, &runner); err != nil {
			return fmt.Errorf("runner must be an object: %w", err)
		}
		for _, field := range []string{"model", "default_model", "classifier_model", "fallback_models"} {
			if _, ok := runner[field]; ok {
				return fmt.Errorf("runner.%s is legacy model-routing configuration and is not supported in composed-tool specs", field)
			}
		}
	}
	return nil
}

func ValidateSpec(spec Spec, registry *tools.Registry) ValidationReport {
	spec = NormalizeSpec(spec)
	report := ValidationReport{Valid: true, RiskLevel: "low"}
	addError := func(format string, args ...any) {
		report.Valid = false
		report.Errors = append(report.Errors, fmt.Sprintf(format, args...))
	}
	addWarning := func(format string, args ...any) {
		report.Warnings = append(report.Warnings, fmt.Sprintf(format, args...))
	}

	if spec.SchemaVersion != 1 {
		addError("schema_version must be 1")
	}
	if !validName.MatchString(spec.Name) {
		addError("name must match %s", validName.String())
	}
	if spec.Description == "" {
		addError("description is required")
	}
	if registry != nil {
		if _, exists := registry.Get(spec.Name); exists {
			addError("name %q collides with a native tool wire name", spec.Name)
		}
	}
	if err := validateJSONSchema("input_schema", spec.InputSchema); err != nil {
		addError("%s", err.Error())
	}
	if err := validateJSONSchema("output_schema", spec.OutputSchema); err != nil {
		addError("%s", err.Error())
	}

	switch spec.Runner.Type {
	case RunnerDeterministic, RunnerAgentic, RunnerHybrid:
	default:
		addError("runner.type must be deterministic, agentic, or hybrid")
	}
	if spec.Runner.Type != RunnerDeterministic && strings.TrimSpace(spec.Runner.SystemPrompt) == "" {
		addError("agentic and hybrid runners require a system_prompt")
	}
	if len(spec.Runner.ToolAllowlist) == 0 && len(spec.Steps) > 0 {
		addError("runner.tool_allowlist is required when steps call native tools")
	}
	allowedNative := stringSet(spec.Runner.ToolAllowlist)
	allowedComposed := stringSet(spec.Runner.ComposedToolAllowlist)

	for _, toolName := range sortedKeys(allowedNative) {
		definition, ok := tools.Definition{}, false
		if registry != nil {
			definition, ok = registry.Get(toolName)
		}
		if registry == nil || !ok {
			addError("allowed native tool %q does not exist", toolName)
			continue
		}
		report.NativeTools = append(report.NativeTools, definition.Name)
		switch definition.ToolClass {
		case tools.ToolClassOwnerOps:
			addError("owner-only native tool %q cannot be used by composed tools", definition.Name)
		case tools.ToolClassDiscordWrite, tools.ToolClassAdminWrite, tools.ToolClassModerationWrite:
			if definition.Name == "draft_moderator_note" {
				continue
			}
			report.Writes = append(report.Writes, definition.Name)
			report.RiskLevel = "high"
			if !supportedComposedWrite(definition.Name) {
				addError("write tool %q is not available for approved composed-tool execution yet", definition.Name)
			}
		}
	}

	for _, step := range spec.Steps {
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			addError("every step requires an id")
		}
		switch strings.TrimSpace(step.Type) {
		case StepToolCall:
			if strings.TrimSpace(step.Tool) == "" {
				addError("step %q requires a tool", stepID)
			} else if !allowedNative[strings.TrimSpace(step.Tool)] {
				addError("step %q calls native tool %q outside runner.tool_allowlist", stepID, step.Tool)
			}
			if step.Arguments == nil {
				addWarning("step %q has no arguments", stepID)
			}
			validateStepSafety(step, &report)
		case StepComposedToolCall:
			if strings.TrimSpace(step.Tool) == "" {
				addError("step %q requires a composed tool", stepID)
			} else if !allowedComposed[strings.TrimSpace(step.Tool)] {
				addError("step %q calls composed tool %q outside runner.composed_tool_allowlist", stepID, step.Tool)
			}
		default:
			addError("step %q has unsupported type %q", stepID, step.Type)
		}
	}

	if len(spec.Invocations) == 0 {
		addError("at least one invocation mode is required")
	}
	seenInvocation := map[string]struct{}{}
	for _, invocation := range spec.Invocations {
		if !invocationEnabled(invocation) {
			continue
		}
		mode := strings.TrimSpace(invocation.Type)
		seenInvocation[mode] = struct{}{}
		switch mode {
		case InvocationChatTool, InvocationSlashCommand, InvocationMessageContext, InvocationScheduled, InvocationEvent, InvocationNestedTool:
		default:
			addError("unsupported invocation type %q", invocation.Type)
		}
		if mode == InvocationEvent {
			if strings.TrimSpace(invocation.EventType) == "" {
				addError("event invocation requires event_type")
			}
			if !SupportsEventType(invocation.EventType) {
				addError("event invocation type %q is not supported", invocation.EventType)
			}
			if (invocation.EventType == EventGuildMemberRoleAdded || invocation.EventType == EventGuildMemberRoleRemoved) && strings.TrimSpace(invocation.Filters["role_id"]) == "" {
				addError("%s invocation requires filters.role_id", invocation.EventType)
			}
		}
		if (mode == InvocationEvent || mode == InvocationScheduled) && len(report.Writes) > 0 && spec.Safety.RequiresConfirmationOnWrite {
			addError("%s invocations cannot use write tools while safety.requires_confirmation_on_write is true", mode)
		}
	}
	if _, ok := seenInvocation[InvocationNestedTool]; len(spec.Runner.ComposedToolAllowlist) > 0 && !ok {
		addWarning("runner allows nested composed tools but no nested_tool invocation is exposed")
	}
	if spec.Safety.MaxNestedDepth > 8 {
		addError("safety.max_nested_depth must be 8 or lower")
	}
	if spec.Safety.MaxRunsPerHour > 1000 {
		addError("safety.max_runs_per_hour must be 1000 or lower")
	}
	if len(report.Writes) == 0 && report.RiskLevel == "low" && spec.Runner.Type != RunnerDeterministic {
		report.RiskLevel = "medium"
	}
	sort.Strings(report.NativeTools)
	sort.Strings(report.Writes)
	return report
}

func validateStepSafety(step StepSpec, report *ValidationReport) {
	if strings.TrimSpace(step.Tool) != "discord.send_message" {
		return
	}
	content := strings.TrimSpace(fmt.Sprint(firstNonNil(step.Arguments["content"], step.Arguments["content_template"])))
	allowed := mapValue(step.Arguments["allowed_mentions"])
	everyoneAllowed := boolMapValue(allowed, "everyone")
	rolesAllowed := boolMapValue(allowed, "roles")
	if everyoneAllowed {
		report.Valid = false
		report.Errors = append(report.Errors, fmt.Sprintf("step %q may not allow everyone mentions", step.ID))
	}
	if rolesAllowed {
		report.Warnings = append(report.Warnings, fmt.Sprintf("step %q allows role mentions; prefer explicit users only", step.ID))
	}
	if strings.Contains(content, "@everyone") || strings.Contains(content, "@here") {
		report.Valid = false
		report.Errors = append(report.Errors, fmt.Sprintf("step %q content may not include @everyone or @here", step.ID))
	}
}

func validateJSONSchema(label string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%s is required", label)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("%s must be valid JSON: %w", label, err)
	}
	if strings.TrimSpace(fmt.Sprint(schema["type"])) != "object" {
		return fmt.Errorf("%s.type must be object", label)
	}
	if _, ok := schema["properties"].(map[string]any); !ok {
		return fmt.Errorf("%s.properties must be an object", label)
	}
	if required, ok := schema["required"]; ok {
		if _, err := stringSlice(required); err != nil {
			return fmt.Errorf("%s.required must be an array of strings", label)
		}
	}
	return nil
}

func ValidateInput(schema json.RawMessage, input map[string]any) error {
	var spec map[string]any
	if err := json.Unmarshal(schema, &spec); err != nil {
		return err
	}
	required, _ := stringSlice(spec["required"])
	for _, name := range required {
		value, ok := input[name]
		if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	properties, _ := spec["properties"].(map[string]any)
	for name, value := range input {
		property, ok := properties[name].(map[string]any)
		if !ok || value == nil {
			continue
		}
		if err := validateJSONValueType(name, property, value); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONValueType(name string, property map[string]any, value any) error {
	kind := strings.TrimSpace(fmt.Sprint(property["type"]))
	switch kind {
	case "", "string":
		if _, ok := value.(string); ok {
			return nil
		}
	case "boolean":
		if _, ok := value.(bool); ok {
			return nil
		}
	case "integer":
		switch value.(type) {
		case int, int64, float64:
			return nil
		}
	case "number":
		switch value.(type) {
		case int, int64, float64:
			return nil
		}
	case "object":
		if _, ok := value.(map[string]any); ok {
			return nil
		}
	case "array":
		if _, ok := value.([]any); ok {
			return nil
		}
	default:
		return nil
	}
	return fmt.Errorf("%s must be %s", name, kind)
}

func ToolDefinition(spec Spec) tools.Definition {
	spec = NormalizeSpec(spec)
	return tools.Definition{
		Name:                  "composed." + spec.Name,
		WireName:              spec.Name,
		Description:           spec.Description,
		RequiredPermission:    admin.PermissionToolComposeInvoke,
		ToolClass:             tools.ToolClassWorkflow,
		InputSchema:           spec.InputSchema,
		OutputSchema:          spec.OutputSchema,
		Timeout:               timeoutForSpec(spec),
		Redaction:             tools.RedactContent,
		Audit:                 tools.AuditSensitive,
		IncludeInModelContext: true,
	}
}

func OpenRouterTool(spec Spec) llm.Tool {
	return ToolDefinition(spec).OpenRouterTool()
}

func timeoutForSpec(spec Spec) time.Duration {
	steps := len(spec.Steps)
	if steps <= 0 {
		steps = 1
	}
	timeout := time.Duration(steps*5) * time.Second
	if timeout < 10*time.Second {
		return 10 * time.Second
	}
	if timeout > time.Minute {
		return time.Minute
	}
	return timeout
}

func supportedComposedWrite(name string) bool {
	switch strings.TrimSpace(name) {
	case "discord.send_message":
		return true
	default:
		return false
	}
}

func stringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result[value] = true
		}
	}
	return result
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringSlice(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed, nil
		}
		return nil, fmt.Errorf("not a string array")
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("not a string array")
		}
		result = append(result, text)
	}
	return result, nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return ""
}

func mapValue(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[string]bool:
		result := make(map[string]any, len(typed))
		for key, value := range typed {
			result[key] = value
		}
		return result
	default:
		return nil
	}
}

func boolMapValue(values map[string]any, key string) bool {
	switch value := values[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}
