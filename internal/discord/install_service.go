package discord

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/sn0w/panda2/internal/billing"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/urlutil"
)

const (
	webhookEventApplicationAuthorized = "APPLICATION_AUTHORIZED"
	integrationTypeGuildInstall       = 0
	defaultInstallIntentTTL           = 30 * time.Minute
	defaultWebhookDetectionTimeout    = 3 * time.Second
	installWebhookDetectionPoll       = 100 * time.Millisecond
	installAuthorizationClockSkew     = 2 * time.Minute
	discordOAuthTokenURL              = "https://discord.com/api/v10/oauth2/token"
)

type WebhookEvent struct {
	Type      string
	Timestamp string
	Data      json.RawMessage
}

type InstallService struct {
	guilds                  *repository.GuildRepository
	features                *features.Service
	featureRepo             *repository.FeatureRepository
	audit                   *repository.AuditRepository
	billing                 *billing.Service
	oauth                   OAuthClient
	applicationID           string
	clientSecret            string
	redirectURI             string
	successRedirect         string
	failureRedirect         string
	intentTTL               time.Duration
	webhookDetectionTimeout time.Duration
	guildVerifier           GuildInstallVerifier
}

type CreateInstallIntentRequest struct {
	FeatureIDs  []string
	Source      string
	DesiredPlan string
	Referrer    string
	Campaign    string
	Metadata    map[string]any
}

type CreateInstallIntentResult struct {
	IntentID     string
	AuthorizeURL string
	ExpiresAt    time.Time
	Selection    features.Selection
}

type InstallCallbackResult struct {
	GuildID         string
	InstallerUserID string
	IntentID        string
	FeatureIDs      []string
	RedirectURL     string
}

type InstallCallbackRequest struct {
	State              string
	Code               string
	GuildID            string
	PermissionBitfield string
}

type OAuthClient interface {
	ExchangeInstallCode(ctx context.Context, code string) (OAuthInstallAuthorization, error)
}

type OAuthInstallAuthorization struct {
	AccessToken        string
	GuildID            string
	GuildName          string
	GuildOwnerUserID   string
	InstallerUserID    string
	Locale             string
	Scopes             []string
	Permissions        []string
	PermissionBitfield string
	AuthorizedAt       time.Time
}

type InstallConfig struct {
	ApplicationID   string
	ClientSecret    string
	RedirectURI     string
	SuccessRedirect string
	FailureRedirect string
	IntentTTL       time.Duration
}

type GuildInstallVerifier interface {
	VerifyGuildInstall(ctx context.Context, request GuildInstallVerificationRequest) (VerifiedGuildInstall, bool, error)
}

type GuildInstallVerificationRequest struct {
	GuildID         string
	InstallerUserID string
	UserAccessToken string
	AuthorizedAt    time.Time
}

type VerifiedGuildInstall struct {
	GuildID           string
	Name              string
	OwnerUserID       string
	InstalledByUserID string
	Locale            string
	AuthorizedAt      time.Time
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
	return &InstallService{
		guilds:                  guilds,
		audit:                   audit,
		intentTTL:               defaultInstallIntentTTL,
		webhookDetectionTimeout: defaultWebhookDetectionTimeout,
	}
}

func (s *InstallService) WithBilling(billingService *billing.Service) *InstallService {
	s.billing = billingService
	return s
}

func (s *InstallService) WithFeatureRepository(repo *repository.FeatureRepository) *InstallService {
	s.featureRepo = repo
	s.features = features.NewService(repo)
	return s
}

func (s *InstallService) WithInstallConfig(cfg InstallConfig) *InstallService {
	s.applicationID = strings.TrimSpace(cfg.ApplicationID)
	s.clientSecret = strings.TrimSpace(cfg.ClientSecret)
	s.redirectURI = strings.TrimSpace(cfg.RedirectURI)
	s.successRedirect = strings.TrimSpace(cfg.SuccessRedirect)
	s.failureRedirect = strings.TrimSpace(cfg.FailureRedirect)
	if cfg.IntentTTL > 0 {
		s.intentTTL = cfg.IntentTTL
	}
	if s.oauth == nil && s.applicationID != "" && s.clientSecret != "" && s.redirectURI != "" {
		s.oauth = &discordOAuthHTTPClient{
			client:       http.DefaultClient,
			clientID:     s.applicationID,
			clientSecret: s.clientSecret,
			redirectURI:  s.redirectURI,
		}
	}
	return s
}

func (s *InstallService) WithOAuthClient(client OAuthClient) *InstallService {
	s.oauth = client
	return s
}

func (s *InstallService) WithGuildInstallVerifier(verifier GuildInstallVerifier) *InstallService {
	s.guildVerifier = verifier
	return s
}

func (s *InstallService) CreateInstallIntent(ctx context.Context, request CreateInstallIntentRequest) (CreateInstallIntentResult, error) {
	if s.featureRepo == nil {
		return CreateInstallIntentResult{}, errors.New("feature repository is not configured")
	}
	if strings.TrimSpace(s.applicationID) == "" {
		return CreateInstallIntentResult{}, errors.New("discord application id is not configured")
	}
	if strings.TrimSpace(s.redirectURI) == "" {
		return CreateInstallIntentResult{}, errors.New("discord install redirect uri is not configured")
	}
	selection, err := features.Calculate(request.FeatureIDs, true)
	if err != nil {
		return CreateInstallIntentResult{}, err
	}
	state, err := randomInstallToken("st")
	if err != nil {
		return CreateInstallIntentResult{}, err
	}
	intentID, err := randomInstallToken("ii")
	if err != nil {
		return CreateInstallIntentResult{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(s.intentTTL)
	selectedJSON, _ := json.Marshal(selection.SelectedFeatureIDs)
	expandedJSON, _ := json.Marshal(selection.ExpandedFeatureIDs)
	permissionsJSON, _ := json.Marshal(selection.DiscordPermissionNames)
	metadataJSON, _ := json.Marshal(request.Metadata)
	if len(metadataJSON) == 0 || string(metadataJSON) == "null" {
		metadataJSON = []byte("{}")
	}
	intent, err := s.featureRepo.CreateInstallIntent(ctx, store.InstallIntent{
		IntentID:                    intentID,
		StateHash:                   hashInstallState(state),
		SelectedFeatureIDs:          string(selectedJSON),
		ExpandedFeatureIDs:          string(expandedJSON),
		RequestedDiscordPermissions: string(permissionsJSON),
		RequestedPermissionBitfield: selection.DiscordPermissionBitfield,
		Source:                      firstNonEmpty(strings.TrimSpace(request.Source), "landing"),
		DesiredPlan:                 strings.TrimSpace(request.DesiredPlan),
		Referrer:                    strings.TrimSpace(request.Referrer),
		Campaign:                    strings.TrimSpace(request.Campaign),
		InstallerSessionMetadata:    string(metadataJSON),
		Status:                      repository.InstallIntentStatusPending,
		ExpiresAt:                   expiresAt,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	})
	if err != nil {
		return CreateInstallIntentResult{}, err
	}
	authorizeURL, err := s.authorizeURL(state, selection)
	if err != nil {
		return CreateInstallIntentResult{}, err
	}
	_ = s.recordInstallIntentAudit(ctx, "discord.install_intent.created", intent, map[string]any{
		"selected_features":             selection.SelectedFeatureIDs,
		"expanded_features":             selection.ExpandedFeatureIDs,
		"requested_discord_permissions": selection.DiscordPermissionNames,
		"requested_permission_bitfield": selection.DiscordPermissionBitfield,
		"source":                        intent.Source,
	})
	return CreateInstallIntentResult{
		IntentID:     intent.IntentID,
		AuthorizeURL: authorizeURL,
		ExpiresAt:    intent.ExpiresAt,
		Selection:    selection,
	}, nil
}

func (s *InstallService) HandleOAuthCallback(ctx context.Context, request InstallCallbackRequest) (InstallCallbackResult, error) {
	if s.featureRepo == nil || s.features == nil {
		return InstallCallbackResult{}, errors.New("feature repository is not configured")
	}
	stateHash := hashInstallState(request.State)
	intent, ok, err := s.featureRepo.GetInstallIntentByStateHash(ctx, stateHash)
	if err != nil {
		return InstallCallbackResult{}, err
	}
	if !ok {
		return InstallCallbackResult{}, repository.ErrNotFound
	}
	now := time.Now().UTC()
	if intent.Status != repository.InstallIntentStatusPending {
		if result, recovered, err := s.completedInstallCallbackResult(ctx, intent); recovered || err != nil {
			return result, err
		}
		return InstallCallbackResult{}, repository.ErrInstallIntentUnavailable
	}
	if !intent.ExpiresAt.IsZero() && !intent.ExpiresAt.After(now) {
		if _, err := s.featureRepo.ConsumeInstallIntent(ctx, stateHash, "", "", "[]", "[]", now); !errors.Is(err, repository.ErrInstallIntentExpired) {
			return InstallCallbackResult{}, err
		}
		return InstallCallbackResult{}, repository.ErrInstallIntentExpired
	}
	code := strings.TrimSpace(request.Code)
	var authorization OAuthInstallAuthorization
	var oauthErr error
	if code != "" {
		if s.oauth == nil {
			oauthErr = errors.New("discord oauth client is not configured")
		} else {
			authorization, oauthErr = s.oauth.ExchangeInstallCode(ctx, code)
		}
	}
	install, alreadyAccepted, err := s.guildInstallForOAuthCallback(ctx, intent, request, authorization, code == "", oauthErr)
	if err != nil {
		return InstallCallbackResult{}, err
	}
	if !alreadyAccepted {
		if err := s.acceptGuildInstall(ctx, install, authorization.Scopes); err != nil {
			return InstallCallbackResult{}, err
		}
	}
	grantedPermissions, grantedBitfield := grantedPermissionsForCallback(authorization, request.PermissionBitfield)
	grantedPermissionsJSON, _ := json.Marshal(grantedPermissions)
	grantedScopesJSON, _ := json.Marshal(authorization.Scopes)
	consumed, err := s.featureRepo.ConsumeInstallIntentAndSetGuildFeatures(ctx, stateHash, install.GuildID, install.InstalledByUserID, string(grantedPermissionsJSON), string(grantedScopesJSON), now)
	if err != nil {
		return InstallCallbackResult{}, err
	}
	expanded := stringListFromJSON(consumed.ExpandedFeatureIDs)
	_ = s.recordInstallAudit(ctx, "guild_features.install_bound", install, map[string]any{
		"intent_id":                     consumed.IntentID,
		"selected_features":             stringListFromJSON(consumed.SelectedFeatureIDs),
		"expanded_features":             expanded,
		"requested_discord_permissions": stringListFromJSON(consumed.RequestedDiscordPermissions),
		"requested_permission_bitfield": consumed.RequestedPermissionBitfield,
		"granted_discord_permissions":   grantedPermissions,
		"granted_permission_bitfield":   grantedBitfield,
		"scopes":                        authorization.Scopes,
	})
	return InstallCallbackResult{
		GuildID:         install.GuildID,
		InstallerUserID: install.InstalledByUserID,
		IntentID:        consumed.IntentID,
		FeatureIDs:      expanded,
		RedirectURL:     s.callbackRedirectURL(true, install.GuildID),
	}, nil
}

func (s *InstallService) completedInstallCallbackResult(ctx context.Context, intent store.InstallIntent) (InstallCallbackResult, bool, error) {
	if intent.Status != repository.InstallIntentStatusConsumed {
		return InstallCallbackResult{}, false, nil
	}
	guildID := strings.TrimSpace(intent.GuildID)
	if guildID == "" || s.guilds == nil {
		return InstallCallbackResult{}, false, nil
	}
	guild, ok, err := s.guilds.Get(ctx, guildID)
	if err != nil {
		return InstallCallbackResult{}, false, err
	}
	if !ok || !storedGuildInstallIsActive(guild) {
		return InstallCallbackResult{}, false, nil
	}
	installerUserID := firstNonEmpty(intent.InstallerUserID, guild.InstalledByUserID)
	return InstallCallbackResult{
		GuildID:         guild.GuildID,
		InstallerUserID: installerUserID,
		IntentID:        intent.IntentID,
		FeatureIDs:      stringListFromJSON(intent.ExpandedFeatureIDs),
		RedirectURL:     s.callbackRedirectURL(true, guild.GuildID),
	}, true, nil
}

func (s *InstallService) guildInstallForOAuthCallback(ctx context.Context, intent store.InstallIntent, request InstallCallbackRequest, authorization OAuthInstallAuthorization, codeMissing bool, oauthErr error) (repository.GuildInstall, bool, error) {
	if install, err := guildInstallFromOAuthAuthorization(authorization, time.Now().UTC()); err == nil {
		return install, false, nil
	}
	hintedGuildID := firstNonEmpty(authorization.GuildID, request.GuildID)
	if hintedGuildID != "" {
		if install, ok, err := s.detectRecordedGuildInstall(ctx, hintedGuildID, intent); ok || err != nil {
			return install, ok, err
		}
		if install, ok, err := s.verifyGuildInstall(ctx, hintedGuildID, authorization); ok || err != nil {
			return install, false, err
		}
	}
	if oauthErr != nil {
		return repository.GuildInstall{}, false, oauthErr
	}
	if codeMissing {
		return repository.GuildInstall{}, false, errors.New("discord oauth code is required")
	}
	return repository.GuildInstall{}, false, errors.New("discord oauth callback could not verify the installed guild")
}

func (s *InstallService) verifyGuildInstall(ctx context.Context, guildID string, authorization OAuthInstallAuthorization) (repository.GuildInstall, bool, error) {
	if s.guildVerifier == nil {
		return repository.GuildInstall{}, false, nil
	}
	if strings.TrimSpace(authorization.AccessToken) == "" {
		return repository.GuildInstall{}, false, nil
	}
	verified, ok, err := s.guildVerifier.VerifyGuildInstall(ctx, GuildInstallVerificationRequest{
		GuildID:         guildID,
		InstallerUserID: authorization.InstallerUserID,
		UserAccessToken: authorization.AccessToken,
		AuthorizedAt:    authorization.AuthorizedAt,
	})
	if err != nil || !ok {
		return repository.GuildInstall{}, ok, err
	}
	install := repository.GuildInstall{
		GuildID:           strings.TrimSpace(verified.GuildID),
		Name:              strings.TrimSpace(verified.Name),
		OwnerUserID:       strings.TrimSpace(verified.OwnerUserID),
		InstalledByUserID: strings.TrimSpace(firstNonEmpty(verified.InstalledByUserID, authorization.InstallerUserID)),
		Locale:            strings.TrimSpace(verified.Locale),
		AuthorizedAt:      verified.AuthorizedAt.UTC(),
	}
	if install.AuthorizedAt.IsZero() {
		install.AuthorizedAt = time.Now().UTC()
	}
	switch {
	case install.GuildID == "":
		return repository.GuildInstall{}, false, errors.New("verified guild install is missing guild id")
	case install.OwnerUserID == "":
		return repository.GuildInstall{}, false, errors.New("verified guild install is missing guild owner id")
	case install.InstalledByUserID == "":
		return repository.GuildInstall{}, false, errors.New("verified guild install is missing installer user id")
	default:
		return install, true, nil
	}
}

func (s *InstallService) detectRecordedGuildInstall(ctx context.Context, guildID string, intent store.InstallIntent) (repository.GuildInstall, bool, error) {
	if s.guilds == nil {
		return repository.GuildInstall{}, false, nil
	}
	deadline := time.Now().Add(s.webhookDetectionTimeout)
	for {
		install, ok, err := s.recordedGuildInstall(ctx, guildID, intent)
		if ok || err != nil || s.webhookDetectionTimeout <= 0 || time.Now().After(deadline) {
			return install, ok, err
		}
		wait := installWebhookDetectionPoll
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return repository.GuildInstall{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *InstallService) recordedGuildInstall(ctx context.Context, guildID string, intent store.InstallIntent) (repository.GuildInstall, bool, error) {
	guild, ok, err := s.guilds.Get(ctx, guildID)
	if err != nil || !ok {
		return repository.GuildInstall{}, false, err
	}
	if !storedGuildInstallIsActive(guild) || !storedGuildInstallMatchesIntent(guild, intent) {
		return repository.GuildInstall{}, false, nil
	}
	install := repository.GuildInstall{
		GuildID:           strings.TrimSpace(guild.GuildID),
		Name:              strings.TrimSpace(guild.Name),
		OwnerUserID:       strings.TrimSpace(guild.OwnerUserID),
		InstalledByUserID: strings.TrimSpace(guild.InstalledByUserID),
		Locale:            strings.TrimSpace(guild.Locale),
		AuthorizedAt:      guild.JoinedAt.UTC(),
	}
	if install.GuildID == "" || install.OwnerUserID == "" || install.InstalledByUserID == "" {
		return repository.GuildInstall{}, false, nil
	}
	return install, true, nil
}

func storedGuildInstallIsActive(guild store.Guild) bool {
	return strings.EqualFold(strings.TrimSpace(guild.InstallStatus), repository.GuildInstallStatusActive) && guild.LeftAt == nil
}

func storedGuildInstallMatchesIntent(guild store.Guild, intent store.InstallIntent) bool {
	if guild.JoinedAt.IsZero() || intent.CreatedAt.IsZero() {
		return false
	}
	return !guild.JoinedAt.UTC().Before(intent.CreatedAt.UTC().Add(-installAuthorizationClockSkew))
}

func grantedPermissionsForCallback(authorization OAuthInstallAuthorization, permissionBitfieldHint string) ([]string, string) {
	bitfield := strings.TrimSpace(authorization.PermissionBitfield)
	hint := strings.TrimSpace(permissionBitfieldHint)
	if bitfield == "" || bitfield == "0" {
		bitfield = hint
	}
	if bitfield == "" {
		bitfield = "0"
	}
	permissions := append([]string(nil), authorization.Permissions...)
	if len(permissions) == 0 {
		permissions = features.PermissionNamesFromBitfield(bitfield)
	}
	return permissions, bitfield
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

func (s *InstallService) RecordGatewayGuild(ctx context.Context, guild disgoDiscord.GatewayGuild) error {
	if s.guilds == nil {
		return errors.New("guild install repository is not configured")
	}
	if guild.Unavailable {
		return nil
	}
	install, err := guildInstallFromGatewayGuild(guild, time.Now().UTC())
	if err != nil {
		return err
	}
	if _, err := s.guilds.RecordObservedInstall(ctx, install); err != nil {
		return err
	}
	if s.billing != nil {
		if _, _, err := s.billing.EnsureTrialIfMissing(ctx, billing.TrialSeed{
			GuildID:            install.GuildID,
			BillingOwnerUserID: install.InstalledByUserID,
			AuthorizedAt:       install.AuthorizedAt,
		}); err != nil {
			return err
		}
	}
	return nil
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

func (s *InstallService) recordInstallIntentAudit(ctx context.Context, action string, intent store.InstallIntent, metadata map[string]any) error {
	if s.audit == nil {
		return nil
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["intent_id"] = intent.IntentID
	metadata["status"] = intent.Status
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return s.audit.Record(ctx, store.AuditEvent{
		GuildID:    intent.GuildID,
		ActorID:    intent.InstallerUserID,
		Action:     action,
		TargetType: "install_intent",
		TargetID:   intent.IntentID,
		Metadata:   string(data),
	})
}

func (s *InstallService) authorizeURL(state string, selection features.Selection) (string, error) {
	values := url.Values{}
	values.Set("client_id", s.applicationID)
	values.Set("scope", strings.Join(selection.Scopes, " "))
	values.Set("permissions", selection.DiscordPermissionBitfield)
	values.Set("integration_type", "0")
	values.Set("state", state)
	values.Set("response_type", "code")
	values.Set("redirect_uri", s.redirectURI)
	values.Set("prompt", "consent")
	return "https://discord.com/oauth2/authorize?" + values.Encode(), nil
}

func (s *InstallService) callbackRedirectURL(success bool, guildID string) string {
	base := strings.TrimSpace(s.failureRedirect)
	status := "failed"
	if success {
		base = strings.TrimSpace(s.successRedirect)
		status = "success"
	}
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	urlutil.StripNonLocalPort(u)
	urlutil.EnsurePathTrailingSlash(u, "/install/success", "/install/failed")
	q := u.Query()
	q.Set("status", status)
	if guildID = strings.TrimSpace(guildID); guildID != "" {
		q.Set("guild_id", guildID)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func isGuildInstall(data applicationAuthorizedData) bool {
	if data.Guild == nil {
		return false
	}
	return data.IntegrationType == nil || *data.IntegrationType == integrationTypeGuildInstall
}

func guildInstallFromGatewayGuild(guild disgoDiscord.GatewayGuild, observedAt time.Time) (repository.GuildInstall, error) {
	authorizedAt := guild.JoinedAt
	if authorizedAt.IsZero() {
		authorizedAt = observedAt
	}
	install := repository.GuildInstall{
		GuildID:           guild.ID.String(),
		Name:              strings.TrimSpace(guild.Name),
		OwnerUserID:       guild.OwnerID.String(),
		InstalledByUserID: guild.OwnerID.String(),
		Locale:            strings.TrimSpace(guild.PreferredLocale),
		AuthorizedAt:      authorizedAt.UTC(),
	}
	switch {
	case guild.ID == 0:
		return repository.GuildInstall{}, errors.New("gateway guild event is missing guild id")
	case guild.OwnerID == 0:
		return repository.GuildInstall{}, errors.New("gateway guild event is missing guild owner id")
	default:
		return install, nil
	}
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

func guildInstallFromOAuthAuthorization(authorization OAuthInstallAuthorization, authorizedAt time.Time) (repository.GuildInstall, error) {
	if authorization.AuthorizedAt.IsZero() {
		authorization.AuthorizedAt = authorizedAt
	}
	install := repository.GuildInstall{
		GuildID:           strings.TrimSpace(authorization.GuildID),
		Name:              strings.TrimSpace(authorization.GuildName),
		OwnerUserID:       strings.TrimSpace(authorization.GuildOwnerUserID),
		InstalledByUserID: strings.TrimSpace(authorization.InstallerUserID),
		Locale:            strings.TrimSpace(authorization.Locale),
		AuthorizedAt:      authorization.AuthorizedAt.UTC(),
	}
	switch {
	case install.GuildID == "":
		return repository.GuildInstall{}, errors.New("oauth install authorization is missing guild id")
	case install.OwnerUserID == "":
		return repository.GuildInstall{}, errors.New("oauth install authorization is missing guild owner id")
	case install.InstalledByUserID == "":
		return repository.GuildInstall{}, errors.New("oauth install authorization is missing installer user id")
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

type discordOAuthHTTPClient struct {
	client       *http.Client
	clientID     string
	clientSecret string
	redirectURI  string
}

type discordOAuthTokenResponse struct {
	AccessToken string             `json:"access_token"`
	TokenType   string             `json:"token_type"`
	Scope       string             `json:"scope"`
	Guild       *discordOAuthGuild `json:"guild"`
	User        *discordOAuthUser  `json:"user"`
	Permissions json.RawMessage    `json:"permissions"`
}

type discordOAuthGuild struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	OwnerID         string `json:"owner_id"`
	PreferredLocale string `json:"preferred_locale"`
}

type discordOAuthUser struct {
	ID string `json:"id"`
}

type discordCurrentUser struct {
	ID string `json:"id"`
}

type discordRESTInstallVerifier struct {
	client   *http.Client
	botToken string
	baseURL  string
}

type discordUserGuild struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Owner       bool            `json:"owner"`
	Permissions json.RawMessage `json:"permissions"`
}

type discordRESTGuild struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	OwnerID         string `json:"owner_id"`
	PreferredLocale string `json:"preferred_locale"`
}

func NewDiscordInstallVerifier(botToken string) GuildInstallVerifier {
	return &discordRESTInstallVerifier{
		client:   http.DefaultClient,
		botToken: strings.TrimSpace(botToken),
		baseURL:  "https://discord.com/api/v10",
	}
}

func (v *discordRESTInstallVerifier) VerifyGuildInstall(ctx context.Context, request GuildInstallVerificationRequest) (VerifiedGuildInstall, bool, error) {
	guildID := strings.TrimSpace(request.GuildID)
	installerUserID := strings.TrimSpace(request.InstallerUserID)
	userAccessToken := strings.TrimSpace(request.UserAccessToken)
	if guildID == "" {
		return VerifiedGuildInstall{}, false, errors.New("discord install verification requires guild id")
	}
	if userAccessToken == "" {
		return VerifiedGuildInstall{}, false, errors.New("discord install verification requires OAuth access token")
	}
	if installerUserID == "" {
		user, err := v.currentUser(ctx, userAccessToken)
		if err != nil {
			return VerifiedGuildInstall{}, false, err
		}
		installerUserID = strings.TrimSpace(user.ID)
	}
	if installerUserID == "" {
		return VerifiedGuildInstall{}, false, errors.New("discord install verification could not identify OAuth user")
	}
	if strings.TrimSpace(v.botToken) == "" {
		return VerifiedGuildInstall{}, false, errors.New("discord install verification requires bot token")
	}
	canManage, err := v.userCanManageGuild(ctx, userAccessToken, guildID)
	if err != nil || !canManage {
		return VerifiedGuildInstall{}, false, err
	}
	guild, ok, err := v.botGuild(ctx, guildID)
	if err != nil || !ok {
		return VerifiedGuildInstall{}, ok, err
	}
	authorizedAt := request.AuthorizedAt.UTC()
	if authorizedAt.IsZero() {
		authorizedAt = time.Now().UTC()
	}
	return VerifiedGuildInstall{
		GuildID:           guild.ID,
		Name:              guild.Name,
		OwnerUserID:       guild.OwnerID,
		InstalledByUserID: installerUserID,
		Locale:            guild.PreferredLocale,
		AuthorizedAt:      authorizedAt,
	}, true, nil
}

func (v *discordRESTInstallVerifier) currentUser(ctx context.Context, userAccessToken string) (discordCurrentUser, error) {
	var user discordCurrentUser
	status, err := v.getJSON(ctx, strings.TrimRight(v.baseURL, "/")+"/users/@me", "Bearer "+userAccessToken, &user)
	if err != nil {
		return discordCurrentUser{}, err
	}
	if status < 200 || status >= 300 {
		return discordCurrentUser{}, fmt.Errorf("discord user identity verification failed with status %d", status)
	}
	return user, nil
}

func (v *discordRESTInstallVerifier) userCanManageGuild(ctx context.Context, userAccessToken, guildID string) (bool, error) {
	before := ""
	for page := 0; page < 10; page++ {
		endpoint, err := url.Parse(strings.TrimRight(v.baseURL, "/") + "/users/@me/guilds")
		if err != nil {
			return false, err
		}
		query := endpoint.Query()
		query.Set("limit", "200")
		if before != "" {
			query.Set("before", before)
		}
		endpoint.RawQuery = query.Encode()

		var guilds []discordUserGuild
		status, err := v.getJSON(ctx, endpoint.String(), "Bearer "+userAccessToken, &guilds)
		if err != nil {
			return false, err
		}
		if status < 200 || status >= 300 {
			return false, fmt.Errorf("discord user guild verification failed with status %d", status)
		}
		for _, guild := range guilds {
			if strings.TrimSpace(guild.ID) != guildID {
				continue
			}
			if guild.Owner || discordPermissionsCanManageGuild(guild.Permissions) {
				return true, nil
			}
			return false, errors.New("discord OAuth user cannot manage the installed guild")
		}
		if len(guilds) < 200 {
			return false, errors.New("discord OAuth user is not a member of the installed guild")
		}
		before = strings.TrimSpace(guilds[len(guilds)-1].ID)
		if before == "" {
			break
		}
	}
	return false, errors.New("discord OAuth user guild verification did not find the installed guild")
}

func (v *discordRESTInstallVerifier) botGuild(ctx context.Context, guildID string) (discordRESTGuild, bool, error) {
	var guild discordRESTGuild
	status, err := v.getJSON(ctx, strings.TrimRight(v.baseURL, "/")+"/guilds/"+url.PathEscape(guildID), "Bot "+strings.TrimSpace(v.botToken), &guild)
	if err != nil {
		return discordRESTGuild{}, false, err
	}
	switch {
	case status >= 200 && status < 300:
		return guild, true, nil
	case status == http.StatusForbidden || status == http.StatusNotFound:
		return discordRESTGuild{}, false, errors.New("discord bot is not installed in the selected guild")
	default:
		return discordRESTGuild{}, false, fmt.Errorf("discord bot guild verification failed with status %d", status)
	}
}

func (v *discordRESTInstallVerifier) getJSON(ctx context.Context, endpoint, authorization string, target any) (int, error) {
	client := v.client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func discordPermissionsCanManageGuild(raw json.RawMessage) bool {
	bitfield := permissionBitfieldFromRaw(raw)
	bits, err := strconv.ParseUint(bitfield, 10, 64)
	if err != nil {
		return false
	}
	const (
		discordPermissionAdministrator = 1 << 3
		discordPermissionManageGuild   = 1 << 5
	)
	return bits&discordPermissionAdministrator != 0 || bits&discordPermissionManageGuild != 0
}

func (c *discordOAuthHTTPClient) ExchangeInstallCode(ctx context.Context, code string) (OAuthInstallAuthorization, error) {
	if c.client == nil {
		c.client = http.DefaultClient
	}
	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", c.redirectURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthInstallAuthorization{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return OAuthInstallAuthorization{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return OAuthInstallAuthorization{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OAuthInstallAuthorization{}, fmt.Errorf("discord oauth exchange failed with status %d", resp.StatusCode)
	}
	var token discordOAuthTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return OAuthInstallAuthorization{}, err
	}
	bitfield := permissionBitfieldFromRaw(token.Permissions)
	var guildID, guildName, ownerUserID, locale string
	if token.Guild != nil {
		guildID = token.Guild.ID
		guildName = token.Guild.Name
		ownerUserID = token.Guild.OwnerID
		locale = token.Guild.PreferredLocale
	}
	var installerUserID string
	if token.User != nil {
		installerUserID = token.User.ID
	}
	return OAuthInstallAuthorization{
		AccessToken:        token.AccessToken,
		GuildID:            guildID,
		GuildName:          guildName,
		GuildOwnerUserID:   ownerUserID,
		InstallerUserID:    installerUserID,
		Locale:             locale,
		Scopes:             strings.Fields(token.Scope),
		Permissions:        features.PermissionNamesFromBitfield(bitfield),
		PermissionBitfield: bitfield,
		AuthorizedAt:       time.Now().UTC(),
	}, nil
}

func permissionBitfieldFromRaw(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return "0"
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return "0"
}

func randomInstallToken(prefix string) (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func hashInstallState(state string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(state)))
	return hex.EncodeToString(sum[:])
}

func stringListFromJSON(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	result := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
