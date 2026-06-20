package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

func (r *Router) handleTool(ctx context.Context, request Request) Response {
	if r.composed == nil {
		return Response{Content: "Composed tools are not configured for this runtime.", Ephemeral: true}
	}
	if request.GuildID == "" {
		return Response{Content: "Tool commands must be run inside a Discord server.", Ephemeral: true}
	}
	switch strings.ToLower(strings.TrimSpace(request.Subcommand)) {
	case "draft":
		return r.handleToolDraft(ctx, request)
	case "approve":
		return r.handleToolApprove(ctx, request)
	case "list":
		return r.handleToolList(ctx, request)
	case "show":
		return r.handleToolShow(ctx, request)
	case "pause":
		return r.handleToolStatus(ctx, request, composed.StatusPaused)
	case "resume", "enable":
		return r.handleToolStatus(ctx, request, composed.StatusEnabled)
	case "disable":
		return r.handleToolStatus(ctx, request, composed.StatusDisabled)
	case "archive":
		return r.handleToolStatus(ctx, request, composed.StatusArchived)
	case "run":
		return r.handleToolRun(ctx, request, false)
	case "simulate":
		return r.handleToolRun(ctx, request, true)
	case "export":
		return r.handleToolExport(ctx, request)
	case "rollback":
		return r.handleToolRollback(ctx, request)
	default:
		return Response{Content: "Unknown tool command.", Ephemeral: true}
	}
}

func (r *Router) handleToolDraft(ctx context.Context, request Request) Response {
	if denied := r.ensureComposedPermission(ctx, request, r.admin.CanDraftComposedTool, "You do not have permission to draft composed tools."); denied.Content != "" {
		return denied
	}
	draftRequest := composed.DraftRequest{
		GuildID:      request.GuildID,
		ActorID:      request.UserID,
		Text:         firstNonEmpty(request.Options["request"], request.Options["description"]),
		SpecJSON:     request.Options["spec_json"],
		RoleID:       request.Options["role_id"],
		RoleName:     request.Options["role_name"],
		ChannelID:    request.Options["channel_id"],
		ChannelName:  request.Options["channel_name"],
		WelcomeText:  request.Options["welcome_text"],
		DefaultModel: request.Options["model"],
	}
	var result composed.DraftResult
	var err error
	if dryRunRequested(request) {
		result, err = r.composed.PreviewDraft(ctx, draftRequest)
	} else {
		result, err = r.composed.Draft(ctx, draftRequest)
	}
	if err != nil {
		return Response{Content: security.SafeDiscordContent(err.Error()), Ephemeral: true}
	}
	prefix := "Drafted"
	if dryRunRequested(request) {
		prefix = "Dry run"
	}
	return Response{Content: security.SafeDiscordContent(renderDraftResult(prefix, result)), Ephemeral: true}
}

func (r *Router) handleToolApprove(ctx context.Context, request Request) Response {
	if denied := r.ensureComposedPermission(ctx, request, r.admin.CanApproveComposedTool, "You do not have permission to approve composed tools."); denied.Content != "" {
		return denied
	}
	name := toolNameOption(request)
	version := versionOption(request, 1)
	if name == "" {
		return Response{Content: "Provide a tool name.", Ephemeral: true}
	}
	confirmationID := toolApproveConfirmationID(request.UserID, name, strconv.Itoa(version))
	if !confirmed(request, confirmationID) {
		return destructiveConfirmation(confirmationID, "Approve tool", fmt.Sprintf("Approve `%s` version %d and enable it for this server.", name, version))
	}
	result, err := r.composed.Approve(ctx, request.GuildID, name, version, request.UserID)
	if err != nil {
		return Response{Content: security.SafeDiscordContent("Approval failed: " + err.Error()), Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Approved `%s` version %d. Risk: `%s`.", result.Tool, result.Version, result.Validation.RiskLevel), Ephemeral: true}
}

func (r *Router) handleToolList(ctx context.Context, request Request) Response {
	if denied := r.ensureComposedPermission(ctx, request, r.admin.CanAuditComposedTool, "You do not have permission to inspect composed tools."); denied.Content != "" {
		return denied
	}
	tools, err := r.composed.List(ctx, request.GuildID)
	if err != nil {
		return Response{Content: "Tool list lookup failed.", Ephemeral: true}
	}
	if len(tools) == 0 {
		return Response{Content: "No composed tools have been drafted for this server.", Ephemeral: true}
	}
	lines := []string{"Composed tools:"}
	for _, tool := range tools {
		lines = append(lines, fmt.Sprintf("- `%s` status `%s` visibility `%s`", tool.Name, tool.Status, tool.Visibility))
	}
	return Response{Content: security.SafeDiscordContent(strings.Join(lines, "\n")), Ephemeral: true}
}

func (r *Router) handleToolShow(ctx context.Context, request Request) Response {
	if denied := r.ensureComposedPermission(ctx, request, r.admin.CanAuditComposedTool, "You do not have permission to inspect composed tools."); denied.Content != "" {
		return denied
	}
	name := toolNameOption(request)
	if name == "" {
		return Response{Content: "Provide a tool name.", Ephemeral: true}
	}
	record, versions, runs, ok, err := r.composed.Show(ctx, request.GuildID, name)
	if err != nil {
		return Response{Content: "Tool lookup failed.", Ephemeral: true}
	}
	if !ok {
		return Response{Content: "No matching composed tool was found.", Ephemeral: true}
	}
	return Response{Content: security.SafeDiscordContent(renderToolDetails(record.Tool, versions, runs)), Ephemeral: true}
}

func (r *Router) handleToolStatus(ctx context.Context, request Request, status string) Response {
	if denied := r.ensureComposedPermission(ctx, request, r.admin.CanApproveComposedTool, "You do not have permission to change composed tool status."); denied.Content != "" {
		return denied
	}
	name := toolNameOption(request)
	if name == "" {
		return Response{Content: "Provide a tool name.", Ephemeral: true}
	}
	if dryRunRequested(request) {
		return dryRunResponse("`%s` would be set to `%s`.", name, status)
	}
	tool, err := r.composed.SetStatus(ctx, request.GuildID, name, status, request.UserID)
	if err != nil {
		return Response{Content: security.SafeDiscordContent("Status update failed: " + err.Error()), Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("`%s` is now `%s`.", tool.Name, tool.Status), Ephemeral: true}
}

func (r *Router) handleToolRun(ctx context.Context, request Request, simulate bool) Response {
	name := toolNameOption(request)
	if name == "" {
		return Response{Content: "Provide a tool name.", Ephemeral: true}
	}
	input, err := parseToolInput(request.Options["input_json"])
	if err != nil {
		return Response{Content: "input_json must be a JSON object.", Ephemeral: true}
	}
	allowed, err := r.composed.CanInvoke(ctx, request.GuildID, name, r.toolAccess(ctx, request, toolsvc.ToolPolicyWriteConfirmed), composed.InvocationManual)
	if err != nil {
		return Response{Content: "Tool access lookup failed.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to run this composed tool.", Ephemeral: true}
	}
	result, runErr := r.composed.Run(ctx, composed.RunRequest{
		GuildID:        request.GuildID,
		ToolName:       name,
		InvocationType: composed.InvocationManual,
		InvokingUserID: request.UserID,
		Input:          input,
		DryRun:         simulate,
	})
	if runErr != nil {
		return Response{Content: security.SafeDiscordContent(fmt.Sprintf("Run `%d` %s: %s", result.RunID, firstNonEmpty(result.Status, "failed"), runErr.Error())), Ephemeral: true}
	}
	action := "Run"
	if simulate {
		action = "Simulation"
	}
	return Response{Content: security.SafeDiscordContent(fmt.Sprintf("%s `%d` %s.\nOutput: `%s`", action, result.RunID, result.Status, mustCompactJSON(result.Output))), Ephemeral: true}
}

func (r *Router) handleToolExport(ctx context.Context, request Request) Response {
	if denied := r.ensureComposedPermission(ctx, request, r.admin.CanAuditComposedTool, "You do not have permission to inspect composed tools."); denied.Content != "" {
		return denied
	}
	name := toolNameOption(request)
	if name == "" {
		return Response{Content: "Provide a tool name.", Ephemeral: true}
	}
	spec, ok, err := r.composed.ExportSpec(ctx, request.GuildID, name)
	if err != nil {
		return Response{Content: "Tool export failed.", Ephemeral: true}
	}
	if !ok {
		return Response{Content: "No approved version is available to export.", Ephemeral: true}
	}
	return Response{Content: security.SafeDiscordContent("```json\n" + mustIndentedJSON(spec) + "\n```"), Ephemeral: true}
}

func (r *Router) handleToolRollback(ctx context.Context, request Request) Response {
	if denied := r.ensureComposedPermission(ctx, request, r.admin.CanApproveComposedTool, "You do not have permission to roll back composed tools."); denied.Content != "" {
		return denied
	}
	name := toolNameOption(request)
	version := versionOption(request, 0)
	if name == "" || version <= 0 {
		return Response{Content: "Provide a tool name and approved version.", Ephemeral: true}
	}
	confirmationID := toolRollbackConfirmationID(request.UserID, name, strconv.Itoa(version))
	if !confirmed(request, confirmationID) {
		return destructiveConfirmation(confirmationID, "Roll back tool", fmt.Sprintf("Roll back `%s` to approved version %d.", name, version))
	}
	result, err := r.composed.Rollback(ctx, request.GuildID, name, version, request.UserID)
	if err != nil {
		return Response{Content: security.SafeDiscordContent("Rollback failed: " + err.Error()), Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Rolled `%s` back to version %d.", result.Tool, result.Version), Ephemeral: true}
}

func (r *Router) ensureComposedPermission(ctx context.Context, request Request, check func(context.Context, admin.AssistantAccessRequest) (bool, error), denial string) Response {
	allowed, err := check(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: denial, Ephemeral: true}
	}
	return Response{}
}

func toolNameOption(request Request) string {
	return strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["tool"], request.Options["name"])))
}

func versionOption(request Request, fallback int) int {
	value := strings.TrimSpace(request.Options["version"])
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseToolInput(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

func renderDraftResult(prefix string, result composed.DraftResult) string {
	lines := []string{
		fmt.Sprintf("%s `%s`.", prefix, result.Spec.Name),
		fmt.Sprintf("Risk: `%s`.", result.Validation.RiskLevel),
	}
	if result.Version > 0 {
		lines = append(lines, fmt.Sprintf("Version: `%d` pending approval.", result.Version))
	}
	if len(result.Validation.NativeTools) > 0 {
		lines = append(lines, "Allowed tools: `"+strings.Join(result.Validation.NativeTools, "`, `")+"`.")
	}
	if len(result.Validation.Writes) > 0 {
		lines = append(lines, "Writes: `"+strings.Join(result.Validation.Writes, "`, `")+"`.")
	}
	for _, warning := range result.Validation.Warnings {
		lines = append(lines, "Warning: "+warning)
	}
	return strings.Join(lines, "\n")
}

func renderToolDetails(tool store.ComposedTool, versions []store.ComposedToolVersion, runs []store.ComposedToolRun) string {
	lines := []string{
		fmt.Sprintf("`%s` status `%s` visibility `%s`.", tool.Name, tool.Status, tool.Visibility),
	}
	if tool.CurrentVersionID != nil {
		lines = append(lines, fmt.Sprintf("Current version id: `%d`.", *tool.CurrentVersionID))
	}
	if len(versions) > 0 {
		items := make([]string, 0, len(versions))
		for _, version := range versions {
			state := "draft"
			if version.ApprovedAt != nil {
				state = "approved"
			}
			items = append(items, fmt.Sprintf("v%d %s", version.VersionNumber, state))
		}
		lines = append(lines, "Versions: "+strings.Join(items, ", ")+".")
	}
	if len(runs) > 0 {
		lines = append(lines, "Recent runs:")
		for _, run := range runs {
			lines = append(lines, fmt.Sprintf("- #%d `%s` via `%s`", run.ID, run.Status, run.InvocationType))
		}
	}
	return strings.Join(lines, "\n")
}

func mustCompactJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func mustIndentedJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}
