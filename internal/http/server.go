package http

import (
	"context"
	"fmt"
	stdhttp "net/http"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/store"
)

type Server struct {
	app   *fiber.App
	cfg   config.Config
	store *store.Store
}

type healthResponse struct {
	Status     string                     `json:"status"`
	Checks     map[string]componentStatus `json:"checks"`
	SQLitePath string                     `json:"sqlite_path"`
}

type componentStatus struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func New(cfg config.Config, store *store.Store) *Server {
	server := &Server{
		app: fiber.New(fiber.Config{
			AppName:     "panda-assistant",
			ReadTimeout: 5 * time.Second,
		}),
		cfg:   cfg,
		store: store,
	}
	server.routes()
	return server
}

func (s *Server) Listen(addr string) error {
	return s.app.Listen(addr)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.app.ShutdownWithContext(ctx)
}

func (s *Server) Test(req *stdhttp.Request, timeout ...int) (*stdhttp.Response, error) {
	return s.app.Test(req, timeout...)
}

func (s *Server) routes() {
	s.app.Get("/healthz", s.health)
	s.app.Get("/readyz", s.ready)
	s.app.Get("/livez", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	s.app.Get("/metrics", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextPlainCharsetUTF8)
		return c.SendString(s.metrics(c.Context()))
	})
}

func (s *Server) health(c *fiber.Ctx) error {
	response, statusCode := s.healthPayload(c.Context())
	return c.Status(statusCode).JSON(response)
}

func (s *Server) ready(c *fiber.Ctx) error {
	response, statusCode := s.healthPayload(c.Context())
	if statusCode == fiber.StatusOK && response.Checks["sqlite"].Status != "ok" {
		statusCode = fiber.StatusServiceUnavailable
		response.Status = "degraded"
	}
	return c.Status(statusCode).JSON(response)
}

func (s *Server) healthPayload(ctx context.Context) (healthResponse, int) {
	checks := map[string]componentStatus{
		"config":        {Status: "ok"},
		"fiber":         {Status: "ok"},
		"discord":       configuredStatus(s.cfg.DiscordConfigured(), "credentials missing; gateway disabled"),
		"openrouter":    configuredStatus(s.cfg.OpenRouterConfigured(), "api key missing; /ask disabled"),
		"local_storage": localStorageStatus(s.cfg.DataDir),
	}

	if checks["local_storage"].Status == "error" {
		return healthResponse{Status: "degraded", Checks: checks, SQLitePath: s.cfg.SQLitePath}, fiber.StatusServiceUnavailable
	}

	if err := s.store.Ping(ctx); err != nil {
		checks["sqlite"] = componentStatus{Status: "error", Message: err.Error()}
		return healthResponse{Status: "degraded", Checks: checks, SQLitePath: s.cfg.SQLitePath}, fiber.StatusServiceUnavailable
	}
	checks["sqlite"] = componentStatus{Status: "ok"}

	return healthResponse{Status: "ok", Checks: checks, SQLitePath: s.cfg.SQLitePath}, fiber.StatusOK
}

func configuredStatus(ok bool, message string) componentStatus {
	if ok {
		return componentStatus{Status: "configured"}
	}
	return componentStatus{Status: "missing", Message: message}
}

func (s *Server) metrics(ctx context.Context) string {
	var builder strings.Builder
	sqliteUp := 1
	if err := s.store.Ping(ctx); err != nil {
		sqliteUp = 0
	}
	writeGauge(&builder, "panda_sqlite_up", "SQLite ping status", sqliteUp)
	writeGauge(&builder, "panda_discord_configured", "Discord credentials configured", boolInt(s.cfg.DiscordConfigured()))
	writeGauge(&builder, "panda_openrouter_configured", "OpenRouter API key configured", boolInt(s.cfg.OpenRouterConfigured()))

	var migrationVersion int
	_ = s.store.DB.Raw("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&migrationVersion).Error
	writeGauge(&builder, "panda_schema_migration_version", "Latest applied schema migration", migrationVersion)

	var queueDepth int64
	_ = s.store.DB.Table("jobs").Where("status = ?", "queued").Count(&queueDepth).Error
	writeGauge(&builder, "panda_queue_depth", "Queued background jobs", queueDepth)

	var usageTotal int64
	_ = s.store.DB.Table("usage_events").Count(&usageTotal).Error
	writeGauge(&builder, "panda_usage_events_total", "Recorded assistant usage events", usageTotal)

	var usageFailures int64
	_ = s.store.DB.Table("usage_events").Where("success = ?", false).Count(&usageFailures).Error
	writeGauge(&builder, "panda_usage_events_failed_total", "Failed assistant usage events", usageFailures)
	return builder.String()
}

func writeGauge(builder *strings.Builder, name, help string, value any) {
	fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
	fmt.Fprintf(builder, "# TYPE %s gauge\n", name)
	fmt.Fprintf(builder, "%s %v\n", name, value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func localStorageStatus(dir string) componentStatus {
	if dir == "" {
		return componentStatus{Status: "missing", Message: "DATA_DIR is not configured"}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return componentStatus{Status: "error", Message: err.Error()}
	}
	if !info.IsDir() {
		return componentStatus{Status: "error", Message: "DATA_DIR is not a directory"}
	}
	file, err := os.CreateTemp(dir, ".panda-health-*")
	if err != nil {
		return componentStatus{Status: "error", Message: err.Error()}
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return componentStatus{Status: "error", Message: err.Error()}
	}
	if err := os.Remove(name); err != nil {
		return componentStatus{Status: "error", Message: err.Error()}
	}
	return componentStatus{Status: "ok"}
}

func Address(port string) string {
	return fmt.Sprintf("0.0.0.0:%s", port)
}
