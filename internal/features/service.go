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
	SetGuildFeatures(ctx context.Context, guildID string, featureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error
	SetGuildFeatureStates(ctx context.Context, guildID string, enabledFeatureIDs []string, disabledFeatureIDs []string, sourceInstallIntentID, actorID string, now time.Time) error
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
	states, err := s.Status(ctx, guildID)
	if err != nil {
		return nil, err
	}
	enabled := map[string]struct{}{}
	for _, state := range states {
		if !state.Enabled {
			continue
		}
		featureID := normalizeID(state.FeatureID)
		if featureID != "" {
			enabled[featureID] = struct{}{}
		}
	}
	return enabled, nil
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
	var expanded []string
	var err error
	if len(featureIDs) == 0 {
		expanded = nil
	} else {
		expanded, err = Expand(featureIDs, false)
		if err != nil {
			return err
		}
	}
	return s.store.SetGuildFeatureStates(ctx, guildID, expanded, defaultEnabledFeaturesMissingFrom(expanded), sourceInstallIntentID, actorID, now)
}

func (s *Service) Status(ctx context.Context, guildID string) ([]repository.GuildFeatureState, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("feature store is not configured")
	}
	states, err := s.store.ListGuildFeatures(ctx, guildID)
	if err != nil {
		return nil, err
	}
	states = applyDefaultEnabledFeatureStates(states)
	sort.Slice(states, func(i, j int) bool {
		return states[i].FeatureID < states[j].FeatureID
	})
	return states, nil
}

func applyDefaultEnabledFeatureStates(states []repository.GuildFeatureState) []repository.GuildFeatureState {
	stateEnabled := map[string]bool{}
	for _, state := range states {
		if featureID := normalizeID(state.FeatureID); featureID != "" {
			stateEnabled[featureID] = state.Enabled
		}
	}
	for _, featureID := range defaultEnabledUnlessDisabledFeatureIDs() {
		if _, ok := stateEnabled[featureID]; ok {
			continue
		}
		if !defaultFeatureDependenciesEnabled(featureID, stateEnabled) {
			continue
		}
		states = append(states, repository.GuildFeatureState{
			FeatureID: featureID,
			Enabled:   true,
		})
		stateEnabled[featureID] = true
	}
	return states
}

func defaultFeatureDependenciesEnabled(featureID string, stateEnabled map[string]bool) bool {
	feature, ok := Lookup(featureID)
	if !ok {
		return false
	}
	for _, dependency := range feature.Dependencies {
		if !stateEnabled[normalizeID(dependency)] {
			return false
		}
	}
	return true
}

func defaultEnabledFeaturesMissingFrom(enabledFeatureIDs []string) []string {
	enabled := FeatureSet(enabledFeatureIDs)
	disabled := []string{}
	for _, featureID := range defaultEnabledUnlessDisabledFeatureIDs() {
		if !Has(enabled, featureID) {
			disabled = append(disabled, featureID)
		}
	}
	return disabled
}

func defaultEnabledUnlessDisabledFeatureIDs() []string {
	return []string{YouTubeClipping}
}
