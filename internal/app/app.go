package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/alerts"
	"github.com/sn0w/panda2/internal/assistant"
	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/curation"
	discordbot "github.com/sn0w/panda2/internal/discord"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/feedback"
	pandahttp "github.com/sn0w/panda2/internal/http"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/maintenance"
	"github.com/sn0w/panda2/internal/memory"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/runtimecontrol"
	"github.com/sn0w/panda2/internal/scheduler"
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
	scheduler  *scheduler.Service
}

type commandInstallIntentAdapter struct {
	service *discordbot.InstallService
}

func (a commandInstallIntentAdapter) CreateFeatureInstallIntent(ctx context.Context, request commands.FeatureInstallIntentRequest) (commands.FeatureInstallIntentResult, error) {
	result, err := a.service.CreateInstallIntent(ctx, discordbot.CreateInstallIntentRequest{
		FeatureIDs: request.FeatureIDs,
		Source:     request.Source,
		Metadata:   request.Metadata,
	})
	if err != nil {
		return commands.FeatureInstallIntentResult{}, err
	}
	return commands.FeatureInstallIntentResult{
		AuthorizeURL: result.AuthorizeURL,
		ExpiresAt:    result.ExpiresAt,
		Selection:    result.Selection,
	}, nil
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
	billingRepo := repository.NewBillingRepository(dataStore.DB)
	dataRepo := repository.NewGuildDataRepository(dataStore.DB)
	members := repository.NewMemberRepository(dataStore.DB)
	jobs := repository.NewJobRepository(dataStore.DB)
	schedules := repository.NewScheduleRepository(dataStore.DB)
	alertRules := repository.NewAlertRuleRepository(dataStore.DB)
	feedbackRepo := repository.NewFeedbackRepository(dataStore.DB)
	musicRepo := repository.NewMusicRepository(dataStore.DB)
	featureRepo := repository.NewFeatureRepository(dataStore.DB)
	composedRepo := repository.NewComposedToolRepository(dataStore.DB)
	runtimeStatuses := repository.NewRuntimeStatusRepository(dataStore.DB)
	runtimeService := runtimecontrol.NewService(runtimeStatuses)
	featureService := features.NewService(featureRepo)
	openRouter := llm.NewOpenRouterClient(llm.OpenRouterConfig{
		APIKey:                         cfg.OpenRouterAPIKey,
		BaseURL:                        cfg.OpenRouterBaseURL,
		AppURL:                         cfg.OpenRouterAppURL,
		AppTitle:                       cfg.OpenRouterAppTitle,
		ProviderOrder:                  cfg.OpenRouterProviderOrder,
		AllowProviderFallbacks:         cfg.OpenRouterAllowProviderFallbacks,
		MaxRetries:                     2,
		CircuitBreakerFailureThreshold: cfg.OpenRouterCircuitBreakerFailureThreshold,
		CircuitBreakerCooldown:         cfg.OpenRouterCircuitBreakerCooldown,
	})
	openRouterImages := llm.NewOpenRouterImageClient(llm.OpenRouterImageConfig{
		APIKey:                         cfg.OpenRouterAPIKey,
		BaseURL:                        cfg.OpenRouterImageBaseURL,
		Model:                          cfg.OpenRouterImageModel,
		AppURL:                         cfg.OpenRouterAppURL,
		AppTitle:                       cfg.OpenRouterAppTitle,
		Timeout:                        cfg.OpenRouterImageTimeout,
		MaxRetries:                     2,
		MaxBytes:                       cfg.OpenRouterImageMaxBytes,
		CircuitBreakerFailureThreshold: cfg.OpenRouterCircuitBreakerFailureThreshold,
		CircuitBreakerCooldown:         cfg.OpenRouterCircuitBreakerCooldown,
	})
	memoryService := memory.NewServiceWithEmbeddings(knowledge, openRouter, cfg.OpenRouterEmbeddingModel)
	billingService := billing.NewService(billingRepo, billing.Config{
		PublicURL:              cfg.PublicAppURL,
		SolanaRPCURL:           cfg.SolanaRPCURL,
		SolanaCluster:          cfg.SolanaCluster,
		SolanaTreasuryWallet:   cfg.SolanaTreasuryWallet,
		SolanaConfirmation:     cfg.SolanaConfirmation,
		SolanaPlanLamports:     cfg.SolanaPlanLamports,
		SolanaOrderExpiration:  cfg.SolanaOrderExpiration,
		SolanaActivationKeyTTL: cfg.SolanaActivationKeyTTL,
	}).WithAuditRecorder(audit)
	curator := curation.NewService(memoryService).
		WithAuditRecorder(audit).
		WithBilling(billingService)
	maintenanceService := maintenance.NewService(conversations, attachments, dataStore)
	worker := queue.NewWorker(jobs, "panda-main")
	worker.Register("maintenance.cleanup", func(ctx context.Context, _ store.Job) error {
		if _, err := maintenanceService.Cleanup(ctx, time.Now().UTC()); err != nil {
			return err
		}
		_, err := curator.ExpireLowConfidence(ctx)
		return err
	})
	opsService := ops.NewService(cfg, dataStore, guildConfigs, jobs, worker)
	adminService := admin.NewService(guildConfigs, usage, audit, memoryService, access, budgets, members).
		WithGuildRepository(guilds).
		WithBilling(billingService)
	toolRegistry, err := tools.NewDefaultRegistry()
	if err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	toolExecutor := tools.NewExecutor(toolRegistry, memoryService, guildConfigs).
		WithAttachmentReader(attachments).
		WithAuditRecorder(audit).
		WithAdminOperations(adminService).
		WithImageGenerator(openRouterImages).
		WithImageAnalyzer(openRouterImages).
		WithBilling(billingService).
		WithOpsManager(opsService)
	composedService := composed.NewService(composedRepo, toolRegistry, toolExecutor, openRouter, cfg.OpenRouterModel).
		WithAuditRecorder(audit).
		WithBilling(billingService).
		WithFeatureService(featureService)
	toolExecutor.WithDynamicToolProvider(composedService).
		WithComposedToolManager(composedService)
	if cfg.BraveSearchConfigured() {
		toolExecutor.WithWebSearcher(websearch.NewBraveClient(websearch.Config{
			APIKey:  cfg.BraveSearchAPIKey,
			BaseURL: cfg.BraveSearchBaseURL,
		}))
	}
	schedulerService := scheduler.NewService(schedules, jobs).
		WithComposedService(composedService).
		WithDiscordEvents(discordEvents).
		WithAuditRecorder(audit).
		WithBilling(billingService).
		WithFeatureService(featureService)
	alertService := alerts.NewService(alertRules).WithAuditRecorder(audit)
	feedbackService := feedback.NewService(feedbackRepo)
	toolExecutor.WithScheduleManager(schedulerService)
	toolExecutor.WithReminderManager(schedulerService)
	assistantService := assistant.NewService(openRouter, usage, guildConfigs, memoryService, conversations, cfg.OpenRouterModel, cfg.OpenRouterFallbackModels).
		WithToolExecutor(toolExecutor).
		WithBilling(billingService).
		WithCurator(curator)
	installService := discordbot.NewInstallService(guilds, audit).
		WithBilling(billingService).
		WithFeatureRepository(featureRepo).
		WithGuildInstallVerifier(discordbot.NewDiscordInstallVerifier(cfg.DiscordBotToken)).
		WithInstallConfig(discordbot.InstallConfig{
			ApplicationID:   cfg.DiscordApplicationID,
			ClientSecret:    cfg.DiscordClientSecret,
			RedirectURI:     cfg.DiscordInstallRedirectURI,
			SuccessRedirect: installResultURL(cfg.PublicAppURL, "/install/success/"),
			FailureRedirect: installResultURL(cfg.PublicAppURL, "/install/failed/"),
		})
	router := commands.NewRouter(adminService, assistantService, opsService, ratelimit.New(cfg.UserRateLimit, cfg.UserRateLimitWindow)).
		WithRuntimeStatus(runtimeService).
		WithComposedService(composedService).
		WithAttachmentReader(attachments).
		WithScheduler(schedulerService).
		WithAlertService(alertService).
		WithBilling(billingService).
		WithDataRepository(dataRepo).
		WithFeedbackService(feedbackService).
		WithToolExecutor(toolExecutor).
		WithFeatureService(featureService).
		WithFeatureInstallIntents(commandInstallIntentAdapter{service: installService})

	discord, err := discordbot.New(cfg, router, logger)
	if err != nil {
		_ = dataStore.Close()
		return nil, err
	}
	discord.WithAttachmentRecorder(attachments).
		WithDiscordEventRecorder(discordEvents).
		WithJobQueue(jobs).
		WithAlertHandler(alertService).
		WithMusicRepository(musicRepo).
		WithInstallService(installService)
	toolExecutor.WithMusicManager(discord.MusicManager())
	schedulerService.WithDeliverySender(discord)
	alertService.WithDeliverySender(discord)
	router.WithSetupChecker(discord)
	if contextService := discord.ContextService(); contextService != nil {
		toolExecutor.WithContextReader(contextService)
	}
	if provider := discord.ToolProvider(discordEvents); provider != nil {
		toolExecutor.WithDiscordToolProvider(provider)
		composedService.WithDiscordResolver(provider)
	}
	worker.Register(discordbot.InteractionJobKind, discord.HandleInteractionJob)
	worker.Register(discordbot.NaturalMessageJobKind, discord.HandleNaturalMessageJob)
	worker.Register(composed.EventJobKind, composedService.HandleEventJob)
	worker.Register(scheduler.JobKind, schedulerService.HandleJob)

	return &App{
		cfg:    cfg,
		logger: logger,
		store:  dataStore,
		httpServer: pandahttp.New(cfg, dataStore).
			WithRuntimeStatus(runtimeService).
			WithDiscordWebhookHandler(installService).
			WithInstallHandler(installService).
			WithBillingService(billingService).
			WithGuildRepository(guilds),
		discord:   discord,
		worker:    worker,
		scheduler: schedulerService,
	}, nil
}

func installResultURL(publicURL, path string) string {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return publicURL + path
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
		if err := a.worker.Run(ctx, "", time.Second); err != nil {
			errs <- err
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.scheduler.Run(ctx, 5*time.Second); err != nil {
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
