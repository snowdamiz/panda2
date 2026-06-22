package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const (
	webhookEventApplicationAuthorized = "APPLICATION_AUTHORIZED"
	integrationTypeGuildInstall       = 0
)

type WebhookEvent struct {
	Type      string
	Timestamp string
	Data      json.RawMessage
}

type InstallService struct {
	guilds  *repository.GuildRepository
	audit   *repository.AuditRepository
	billing *billing.Service
}

type applicationAuthorizedData struct {
	IntegrationType *int                 `json:"integration_type"`
	User            webhookUser          `json:"user"`
	Scopes          []string             `json:"scopes"`
	Guild           *webhookGuildInstall `json:"guild"`
}

type webhookUser struct {
	ID         string  `json:"id"`
	Username   string  `json:"username"`
	GlobalName *string `json:"global_name"`
}

type webhookGuildInstall struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	OwnerID         string `json:"owner_id"`
	PreferredLocale string `json:"preferred_locale"`
}

func NewInstallService(guilds *repository.GuildRepository, audit *repository.AuditRepository) *InstallService {
	return &InstallService{guilds: guilds, audit: audit}
}

func (s *InstallService) WithBilling(billingService *billing.Service) *InstallService {
	s.billing = billingService
	return s
}

func (s *InstallService) HandleWebhookEvent(ctx context.Context, event WebhookEvent) error {
	if !strings.EqualFold(event.Type, webhookEventApplicationAuthorized) {
		return nil
	}
	if s.guilds == nil {
		return errors.New("guild install repository is not configured")
	}

	var data applicationAuthorizedData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return fmt.Errorf("decode application authorized event: %w", err)
	}
	if !isGuildInstall(data) {
		return nil
	}
	install, err := guildInstallFromAuthorizedData(data, eventTime(event.Timestamp))
	if err != nil {
		return err
	}
	return s.acceptGuildInstall(ctx, install, data.Scopes)
}

func (s *InstallService) acceptGuildInstall(ctx context.Context, install repository.GuildInstall, scopes []string) error {
	if _, err := s.guilds.RecordAuthorizedInstall(ctx, install); err != nil {
		return err
	}
	if s.billing != nil {
		if _, err := s.billing.EnsureTrial(ctx, billing.TrialSeed{
			GuildID:            install.GuildID,
			BillingOwnerUserID: install.InstalledByUserID,
			AuthorizedAt:       install.AuthorizedAt,
		}); err != nil {
			return err
		}
	}
	if err := s.recordInstallAudit(ctx, "discord.install.authorized", install, map[string]any{
		"status": "active",
		"scopes": scopes,
	}); err != nil {
		return err
	}
	return nil
}

func (s *InstallService) recordInstallAudit(ctx context.Context, action string, install repository.GuildInstall, metadata map[string]any) error {
	if s.audit == nil {
		return nil
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["guild_owner_user_id"] = install.OwnerUserID
	metadata["installed_by_user_id"] = install.InstalledByUserID
	metadata["guild_name"] = install.Name
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return s.audit.Record(ctx, store.AuditEvent{
		GuildID:    install.GuildID,
		ActorID:    install.InstalledByUserID,
		Action:     action,
		TargetType: "guild",
		TargetID:   install.GuildID,
		Metadata:   string(data),
	})
}

func isGuildInstall(data applicationAuthorizedData) bool {
	if data.Guild == nil {
		return false
	}
	return data.IntegrationType == nil || *data.IntegrationType == integrationTypeGuildInstall
}

func guildInstallFromAuthorizedData(data applicationAuthorizedData, authorizedAt time.Time) (repository.GuildInstall, error) {
	if data.Guild == nil {
		return repository.GuildInstall{}, errors.New("authorized guild install event is missing guild data")
	}
	install := repository.GuildInstall{
		GuildID:           strings.TrimSpace(data.Guild.ID),
		Name:              strings.TrimSpace(data.Guild.Name),
		OwnerUserID:       strings.TrimSpace(data.Guild.OwnerID),
		InstalledByUserID: strings.TrimSpace(data.User.ID),
		Locale:            strings.TrimSpace(data.Guild.PreferredLocale),
		AuthorizedAt:      authorizedAt,
	}
	switch {
	case install.GuildID == "":
		return repository.GuildInstall{}, errors.New("authorized guild install event is missing guild id")
	case install.OwnerUserID == "":
		return repository.GuildInstall{}, errors.New("authorized guild install event is missing guild owner id")
	case install.InstalledByUserID == "":
		return repository.GuildInstall{}, errors.New("authorized guild install event is missing authorizing user id")
	default:
		return install, nil
	}
}

func eventTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Now().UTC()
	}
	return parsed.UTC()
}
