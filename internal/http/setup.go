package http

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/sn0w/panda2/internal/repository"
	setupsvc "github.com/sn0w/panda2/internal/setup"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

type SetupHandler interface {
	Catalog(ctx context.Context) ([]setupsvc.Template, error)
	Preview(ctx context.Context, request setupsvc.SetupRequest) (store.GuildSetupProject, setupsvc.Preview, error)
	Confirm(ctx context.Context, projectID, actorID string, enqueue bool) (store.GuildSetupProject, error)
	RollbackProject(ctx context.Context, projectID, actorID string) (setupsvc.ApplyResult, error)
	ManageServerSetup(ctx context.Context, request toolsvc.ServerSetupManagementRequest) (any, error)
	ManageTicket(ctx context.Context, request toolsvc.TicketManagementRequest) (any, error)
	ManageOnboarding(ctx context.Context, request toolsvc.OnboardingManagementRequest) (any, error)
}

type setupPreviewRequest struct {
	GuildID    string            `json:"guild_id"`
	TemplateID string            `json:"template_id"`
	Variables  map[string]string `json:"variables"`
}

type setupApplyRequest struct {
	GuildID string `json:"guild_id"`
}

func (s *Server) WithSetupService(service SetupHandler) *Server {
	s.setup = service
	return s
}

func (s *Server) setupTemplates(c *fiber.Ctx) error {
	return c.JSON(map[string]any{"templates": s.setupTemplateCatalog(c.Context())})
}

func (s *Server) portalSetupPreview(c *fiber.Ctx) error {
	if s.setup == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "setup_unavailable"})
	}
	var request setupPreviewRequest
	if err := c.BodyParser(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "bad_request"})
	}
	session, _, err := s.requireSetupGuild(c, request.GuildID)
	if err != nil {
		return err
	}
	project, preview, err := s.setup.Preview(c.Context(), setupsvc.SetupRequest{
		GuildID:    request.GuildID,
		ActorID:    session.UserID,
		TemplateID: request.TemplateID,
		Variables:  request.Variables,
		DryRun:     true,
	})
	if err != nil {
		return writeSetupError(c, err)
	}
	return c.JSON(map[string]any{
		"project": setupProjectPayload(project),
		"preview": preview,
	})
}

func (s *Server) portalSetupProject(c *fiber.Ctx) error {
	if s.setup == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "setup_unavailable"})
	}
	guildID := strings.TrimSpace(c.Query("guild_id"))
	if _, _, err := s.requireSetupGuild(c, guildID); err != nil {
		return err
	}
	result, err := s.setup.ManageServerSetup(c.Context(), toolsvc.ServerSetupManagementRequest{
		GuildID:   guildID,
		ProjectID: c.Params("project_id"),
		Action:    "status",
	})
	if err != nil {
		return writeSetupError(c, err)
	}
	return c.JSON(result)
}

func (s *Server) portalSetupApply(c *fiber.Ctx) error {
	if s.setup == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "setup_unavailable"})
	}
	var request setupApplyRequest
	_ = c.BodyParser(&request)
	if strings.TrimSpace(request.GuildID) == "" {
		request.GuildID = strings.TrimSpace(c.Query("guild_id"))
	}
	session, _, err := s.requireSetupGuild(c, request.GuildID)
	if err != nil {
		return err
	}
	if _, err := s.setup.ManageServerSetup(c.Context(), toolsvc.ServerSetupManagementRequest{
		GuildID:   request.GuildID,
		ProjectID: c.Params("project_id"),
		Action:    "status",
	}); err != nil {
		return writeSetupError(c, err)
	}
	project, err := s.setup.Confirm(c.Context(), c.Params("project_id"), session.UserID, true)
	if err != nil {
		return writeSetupError(c, err)
	}
	return c.JSON(map[string]any{"project": setupProjectPayload(project)})
}

func (s *Server) portalSetupRollback(c *fiber.Ctx) error {
	if s.setup == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "setup_unavailable"})
	}
	var request setupApplyRequest
	_ = c.BodyParser(&request)
	if strings.TrimSpace(request.GuildID) == "" {
		request.GuildID = strings.TrimSpace(c.Query("guild_id"))
	}
	session, _, err := s.requireSetupGuild(c, request.GuildID)
	if err != nil {
		return err
	}
	result, err := s.setup.RollbackProject(c.Context(), c.Params("project_id"), session.UserID)
	if err != nil {
		return writeSetupError(c, err)
	}
	return c.JSON(map[string]any{"result": result})
}

func (s *Server) portalSetupTickets(c *fiber.Ctx) error {
	if s.setup == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "setup_unavailable"})
	}
	guildID := strings.TrimSpace(c.Query("guild_id"))
	session, _, err := s.requireSetupGuild(c, guildID)
	if err != nil {
		return err
	}
	result, err := s.setup.ManageTicket(c.Context(), toolsvc.TicketManagementRequest{
		GuildID:       guildID,
		ActorID:       session.UserID,
		Action:        "list",
		IncludeClosed: queryBool(c, "include_closed"),
		Limit:         queryInt(c, "limit", 50),
	})
	if err != nil {
		return writeSetupError(c, err)
	}
	return c.JSON(result)
}

func (s *Server) portalSetupOnboarding(c *fiber.Ctx) error {
	if s.setup == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "setup_unavailable"})
	}
	guildID := strings.TrimSpace(c.Query("guild_id"))
	session, _, err := s.requireSetupGuild(c, guildID)
	if err != nil {
		return err
	}
	result, err := s.setup.ManageOnboarding(c.Context(), toolsvc.OnboardingManagementRequest{
		GuildID: guildID,
		ActorID: session.UserID,
		Action:  "list",
		Limit:   queryInt(c, "limit", 50),
	})
	if err != nil {
		return writeSetupError(c, err)
	}
	return c.JSON(result)
}

func (s *Server) setupTemplateCatalog(ctx context.Context) []map[string]any {
	templates := setupsvc.BuiltInTemplates()
	if s.setup != nil {
		if synced, err := s.setup.Catalog(ctx); err == nil && len(synced) > 0 {
			templates = synced
		}
	}
	result := make([]map[string]any, 0, len(templates))
	for _, template := range templates {
		result = append(result, map[string]any{
			"id":                 template.ID,
			"name":               template.Name,
			"description":        template.Description,
			"release_state":      template.ReleaseState,
			"default_variables":  template.DefaultVariables,
			"editable_variables": template.EditableVariables,
			"feature_ids":        template.FeatureIDs,
		})
	}
	return result
}

func (s *Server) requireSetupGuild(c *fiber.Ctx, guildID string) (portalSession, store.Guild, error) {
	session, err := s.requireUser(c)
	if err != nil {
		return portalSession{}, store.Guild{}, err
	}
	if s.guilds == nil {
		return portalSession{}, store.Guild{}, c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "guild_store_unavailable"})
	}
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return portalSession{}, store.Guild{}, c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "guild_id_required"})
	}
	guild, ok, err := s.guilds.Get(c.Context(), guildID)
	if err != nil {
		return portalSession{}, store.Guild{}, c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "internal_error"})
	}
	if !ok || strings.TrimSpace(guild.InstallStatus) != repository.GuildInstallStatusActive || guild.LeftAt != nil {
		return portalSession{}, store.Guild{}, c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "guild_not_found"})
	}
	if session.UserID != guild.OwnerUserID && session.UserID != guild.InstalledByUserID {
		return portalSession{}, store.Guild{}, c.Status(fiber.StatusForbidden).JSON(map[string]string{"error": "setup_forbidden"})
	}
	return session, guild, nil
}

func setupProjectPayload(project store.GuildSetupProject) map[string]any {
	return map[string]any{
		"id":               project.ID,
		"guild_id":         project.GuildID,
		"template_id":      project.TemplateID,
		"template_version": project.TemplateVersion,
		"schema_version":   project.SchemaVersion,
		"status":           project.Status,
		"actor_id":         project.ActorID,
		"current_step":     project.CurrentStep,
		"last_error":       project.LastError,
		"created_at":       setupHTTPTime(project.CreatedAt),
		"confirmed_at":     setupHTTPTimePtr(project.ConfirmedAt),
		"started_at":       setupHTTPTimePtr(project.StartedAt),
		"finished_at":      setupHTTPTimePtr(project.FinishedAt),
	}
}

func writeSetupError(c *fiber.Ctx, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrNotFound):
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "setup_not_found"})
	default:
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "rate limit") {
			return c.Status(fiber.StatusTooManyRequests).JSON(map[string]string{"error": "setup_rate_limited"})
		}
		if strings.Contains(message, "requires") || strings.Contains(message, "permission") || strings.Contains(message, "confirmation") {
			return c.Status(fiber.StatusForbidden).JSON(map[string]string{"error": "setup_forbidden"})
		}
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{"error": "setup_failed", "message": err.Error()})
	}
}

func queryBool(c *fiber.Ctx, key string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(c.Query(key)))
	return err == nil && value
}

func queryInt(c *fiber.Ctx, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(c.Query(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func setupHTTPTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func setupHTTPTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return setupHTTPTime(*value)
}
