package ops

import (
	"context"
	"sync/atomic"

	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/queue"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type Service struct {
	cfg      config.Config
	store    *store.Store
	configs  *repository.GuildConfigRepository
	jobs     *repository.JobRepository
	worker   *queue.Worker
	incident atomic.Bool
}

type Health struct {
	SQLite               string
	Discord              string
	Shards               string
	AIService            string
	DataDir              string
	QueuedJobs           int64
	ConfiguredGuildCount int64
	Draining             bool
	Incident             bool
}

func NewService(cfg config.Config, store *store.Store, configs *repository.GuildConfigRepository, jobs *repository.JobRepository, worker *queue.Worker) *Service {
	return &Service{cfg: cfg, store: store, configs: configs, jobs: jobs, worker: worker}
}

func (s *Service) Health(ctx context.Context) (Health, error) {
	status := Health{
		SQLite:    "ok",
		Discord:   configured(s.cfg.DiscordConfigured()),
		Shards:    shardStatus(s.cfg.DiscordConfigured()),
		AIService: configured(s.cfg.OpenRouterConfigured()),
		DataDir:   s.cfg.DataDir,
		Draining:  s.worker != nil && s.worker.IsDraining(),
		Incident:  s.incident.Load(),
	}
	if err := s.store.Ping(ctx); err != nil {
		status.SQLite = "error"
		return status, err
	}

	var err error
	status.QueuedJobs, err = s.jobs.QueueDepth(ctx, "")
	if err != nil {
		return status, err
	}
	status.ConfiguredGuildCount, err = s.configs.Count(ctx)
	return status, err
}

func (s *Service) Drain() {
	if s.worker != nil {
		s.worker.Drain()
	}
}

func (s *Service) Resume() {
	if s.worker != nil {
		s.worker.Resume()
	}
}

func (s *Service) EnableIncident() {
	s.incident.Store(true)
}

func (s *Service) DisableIncident() {
	s.incident.Store(false)
}

func (s *Service) Reload(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func configured(ok bool) string {
	if ok {
		return "configured"
	}
	return "missing"
}

func shardStatus(discordConfigured bool) string {
	if discordConfigured {
		return "single-gateway-v1"
	}
	return "disabled"
}
