package features

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/repository"
)

var ErrDisabled = errors.New("feature not enabled for guild")

type Store interface {
	EnabledFeatureSet(ctx context.Context, guildID string) (map[string]struct{}, error)
	SetGuildFeatures(ctx context.Context, guildID string, featureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error
	ListGuildFeatures(ctx context.Context, guildID string) ([]repository.GuildFeatureState, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) EnabledSet(ctx context.Context, guildID string) (map[string]struct{}, error) {
	if s == nil || s.store == nil || strings.TrimSpace(guildID) == "" {
		return map[string]struct{}{}, nil
	}
	return s.store.EnabledFeatureSet(ctx, guildID)
}

func (s *Service) Enabled(ctx context.Context, guildID, featureID string) (bool, error) {
	set, err := s.EnabledSet(ctx, guildID)
	if err != nil {
		return false, err
	}
	return Has(set, featureID), nil
}

func (s *Service) Require(ctx context.Context, guildID, featureID string) error {
	enabled, err := s.Enabled(ctx, guildID, featureID)
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("%w: %s", ErrDisabled, featureID)
	}
	return nil
}

func (s *Service) ReplaceGuildFeatures(ctx context.Context, guildID string, featureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error {
	if s == nil || s.store == nil {
		return errors.New("feature store is not configured")
	}
	if len(featureIDs) == 0 {
		return s.store.SetGuildFeatures(ctx, guildID, nil, sourceInstallIntentID, actorID, now)
	}
	expanded, err := Expand(featureIDs, false)
	if err != nil {
		return err
	}
	return s.store.SetGuildFeatures(ctx, guildID, expanded, sourceInstallIntentID, actorID, now)
}

func (s *Service) Status(ctx context.Context, guildID string) ([]repository.GuildFeatureState, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("feature store is not configured")
	}
	states, err := s.store.ListGuildFeatures(ctx, guildID)
	if err != nil {
		return nil, err
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].FeatureID < states[j].FeatureID
	})
	return states, nil
}
