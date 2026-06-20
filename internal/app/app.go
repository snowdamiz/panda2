package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/assistant"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
	pandahttp "github.com/sn0w/panda2/internal/http"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/maintenance"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/tools"
	"github.com/sn0w/panda2/internal/websearch"
)

type App struct {
	cfg        config.Config
	logger     *slog.Logger
	store      *store.Store
	httpServer *pandahttp.Server
	discord    *discordbot.Bot
	worker     *queue.Worker
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	dataStore, err := store.Open(ctx, cfg.SQLitePath)
	if err != nil {
		return nil, err
	}

	guilds := repository.NewGuildRepository(dataStore.DB)
	guildConfigs := repository.NewGuildConfigRepository(dataStore.DB)
	usage := repository.NewUsageRepository(dataStore.DB)
	audit := repository.NewAuditRepository(dataStore.DB)
	knowledge := repository.NewKnowledgeRepository(dataStore.DB)
	conversations := repository.NewConversationRepository(dataStore.DB)
	attachments := repository.NewAttachmentRepository(dataStore.DB)
	discordEvents := repository.NewDiscordEventRepository(dataStore.DB)
	access := repository.NewAccessRepository(dataStore.DB)
	budgets := repository.NewBudgetRepository(dataStore.DB)
	members := repository.NewMemberRepository(dataStore.DB)
	jobs := repository.NewJobRepository(dataStore.DB)
	composedTools := repository.NewComposedToolRepository(dataStore.DB)
	openRouter := llm.NewOpenRouterClient(llm.OpenRouterConfig{
		APIKey:                         cfg.OpenRouterAPIKey,
		BaseURL:                        cfg.OpenRouterBaseURL,
		AppURL:                         cfg.OpenRouterAppURL,
		AppTitle:                       cfg.OpenRouterAppTitle,
		MaxRetries:                     2,
		CircuitBreakerFailureThreshold: cfg.OpenRouterCircuitBreakerFailureThreshold,
		CircuitBreakerCooldown:         cfg.OpenRouterCircuitBreakerCooldown,
	})
	memoryService := memory.NewServiceWithEmbeddings(knowledge, openRouter, cfg.OpenRouterEmbeddingModel)
	maintenanceService := maintenance.NewService(conversations, attachments, dataStore)
	worker := queue.NewWorker(jobs, "panda-main")
	worker.Register("maintenance.cleanup", func(ctx context.Context, _ store.Job) error {
		_, err := maintenanceService.Cleanup(ctx, time.Now().UTC())
		return err
	})
	opsService := ops.NewService(cfg, dataStore, guildConfigs, jobs, worker)
	adminService := admin.NewService(guildConfigs, usage, audit, memoryService, access, budgets, openRouter, cfg.OpenRouterModel, members).
		WithGuildRepository(guilds)
	toolRegistry, err := tools.NewDefaultRegistry()
	if err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	toolExecutor := tools.NewExecutor(toolRegistry, memoryService, guildConfigs).
		WithAttachmentReader(attachments).
		WithAuditRecorder(audit).
		WithAdminOperations(adminService)
	if cfg.BraveSearchConfigured() {
		toolExecutor.WithWebSearcher(websearch.NewBraveClient(websearch.Config{
			APIKey:  cfg.BraveSearchAPIKey,
			BaseURL: cfg.BraveSearchBaseURL,
		}))
	}
	composedService := composed.NewService(composedTools, toolRegistry, toolExecutor, openRouter, cfg.OpenRouterModel).
		WithAuditRecorder(audit)
	toolExecutor.WithDynamicToolProvider(composedService)
	assistantService := assistant.NewService(openRouter, usage, guildConfigs, memoryService, conversations, cfg.OpenRouterModel, cfg.OpenRouterFallbackModels).
		WithToolExecutor(toolExecutor)
	router := commands.NewRouter(adminService, assistantService, opsService, ratelimit.New(cfg.UserRateLimit, cfg.UserRateLimitWindow)).
		WithAttachmentReader(attachments).
		WithComposedService(composedService)

	discord, err := discordbot.New(cfg, router, logger)
	if err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	discord.WithAttachmentRecorder(attachments).WithDiscordEventRecorder(discordEvents).WithJobQueue(jobs)
	if contextService := discord.ContextService(); contextService != nil {
		toolExecutor.WithContextReader(contextService)
	}
	if provider := discord.ToolProvider(discordEvents); provider != nil {
		toolExecutor.WithDiscordToolProvider(provider)
		composedService.WithDiscordResolver(provider)
	}
	installService := discordbot.NewInstallService(guilds, audit, discord)
	worker.Register(discordbot.InteractionJobKind, discord.HandleInteractionJob)
	worker.Register(composed.EventJobKind, composedService.HandleEventJob)
	worker.Register(composed.RunJobKind, composedService.HandleRunJob)

	return &App{
		cfg:        cfg,
		logger:     logger,
		store:      dataStore,
		httpServer: pandahttp.New(cfg, dataStore).WithDiscordWebhookHandler(installService),
		discord:    discord,
		worker:     worker,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		addr := pandahttp.Address(a.cfg.Port)
		a.logger.Info("http server listening", slog.String("addr", addr))
		if err := a.httpServer.Listen(addr); err != nil {
			errs <- err
		}
	}()

	if err := a.discord.Start(ctx); err != nil {
		return err
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.worker.Run(ctx, "", 5*time.Second); err != nil {
			errs <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = a.httpServer.Shutdown(shutdownCtx)
	a.discord.Close(shutdownCtx)
	wg.Wait()
	return ctx.Err()
}

func (a *App) Close(ctx context.Context) {
	if a.discord != nil {
		a.discord.Close(ctx)
	}
	if a.httpServer != nil {
		_ = a.httpServer.Shutdown(ctx)
	}
	if a.store != nil {
		_ = a.store.Close()
	}
}
