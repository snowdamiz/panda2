package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	featurecatalog "github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

type fakeOAuthClient struct {
	code          string
	authorization OAuthInstallAuthorization
	err           error
}

func (f *fakeOAuthClient) ExchangeInstallCode(_ context.Context, code string) (OAuthInstallAuthorization, error) {
	f.code = code
	return f.authorization, f.err
}

type fakeGuildInstallVerifier struct {
	request GuildInstallVerificationRequest
	install VerifiedGuildInstall
	ok      bool
	err     error
}

func (f *fakeGuildInstallVerifier) VerifyGuildInstall(_ context.Context, request GuildInstallVerificationRequest) (VerifiedGuildInstall, bool, error) {
	f.request = request
	return f.install, f.ok, f.err
}

func TestCallbackRedirectURLStripsNonLocalRuntimePort(t *testing.T) {
	service := NewInstallService(nil, nil).WithInstallConfig(InstallConfig{
		SuccessRedirect: "https://pandaclanker.xyz:8080/install/success/",
		FailureRedirect: "https://pandaclanker.xyz:8080/install/failed/",
	})

	if got := service.callbackRedirectURL(true, "guild-1"); got != "https://pandaclanker.xyz/install/success/?guild_id=guild-1&status=success" {
		t.Fatalf("unexpected success redirect: %q", got)
	}
	if got := service.callbackRedirectURL(false, ""); got != "https://pandaclanker.xyz/install/failed/?status=failed" {
		t.Fatalf("unexpected failure redirect: %q", got)
	}
}

func TestCallbackRedirectURLKeepsLocalDevelopmentPort(t *testing.T) {
	service := NewInstallService(nil, nil).WithInstallConfig(InstallConfig{
		SuccessRedirect: "http://localhost:4321/install/success",
	})

	if got := service.callbackRedirectURL(true, "guild-1"); got != "http://localhost:4321/install/success?guild_id=guild-1&status=success" {
		t.Fatalf("unexpected local redirect: %q", got)
	}
}

func TestInstallServiceAcceptsServerOwnerInstall(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB))
	if err := service.HandleWebhookEvent(ctx, authorizedEvent(t, "owner-1", "owner-1")); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	guild, ok, err := guilds.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get guild: ok=%t err=%v", ok, err)
	}
	if guild.InstallStatus != repository.GuildInstallStatusActive || guild.InstalledByUserID != "owner-1" || guild.OwnerUserID != "owner-1" {
		t.Fatalf("unexpected accepted guild: %+v", guild)
	}
	assertAuditAction(t, db, "discord.install.authorized")
}

func TestInstallServiceAcceptsNonOwnerInstallerAsPandaOwner(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB))
	if err := service.HandleWebhookEvent(ctx, authorizedEvent(t, "installer-1", "owner-1")); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	guild, ok, err := guilds.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get guild: ok=%t err=%v", ok, err)
	}
	if guild.InstallStatus != repository.GuildInstallStatusActive || guild.InstalledByUserID != "installer-1" || guild.OwnerUserID != "owner-1" || guild.LeftAt != nil {
		t.Fatalf("unexpected accepted guild: %+v", guild)
	}
	assertAuditAction(t, db, "discord.install.authorized")
}

func TestInstallServiceDoesNotRestrictChannelsByDefault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	access := repository.NewAccessRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB))
	if err := service.HandleWebhookEvent(ctx, authorizedEvent(t, "owner-1", "owner-1")); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	rules, err := access.ListChannelRules(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListChannelRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected no default channel restrictions, got %+v", rules)
	}
	assertAuditAction(t, db, "discord.install.authorized")
}

func TestInstallServiceBindsFeaturesFromOAuthAndVerifiedBotGuild(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	featureRepo := repository.NewFeatureRepository(db.DB)
	oauth := &fakeOAuthClient{
		authorization: OAuthInstallAuthorization{
			AccessToken:        "access-1",
			InstallerUserID:    "installer-1",
			Scopes:             []string{"bot", "applications.commands", "identify", "guilds"},
			PermissionBitfield: "1024",
			AuthorizedAt:       time.Date(2026, 6, 22, 18, 0, 0, 0, time.UTC),
		},
	}
	verifier := &fakeGuildInstallVerifier{
		ok: true,
		install: VerifiedGuildInstall{
			GuildID:           "guild-1",
			Name:              "Verified Guild",
			OwnerUserID:       "owner-1",
			InstalledByUserID: "installer-1",
			Locale:            "en-US",
			AuthorizedAt:      time.Date(2026, 6, 22, 18, 0, 0, 0, time.UTC),
		},
	}
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB)).
		WithFeatureRepository(featureRepo).
		WithOAuthClient(oauth).
		WithGuildInstallVerifier(verifier).
		WithInstallConfig(InstallConfig{
			ApplicationID:   "app-1",
			RedirectURI:     "https://api.example.test/discord/install/callback",
			SuccessRedirect: "https://panda.example.test/install/success",
		})

	intent, err := service.CreateInstallIntent(ctx, CreateInstallIntentRequest{
		FeatureIDs: []string{featurecatalog.AssistantChat},
		Source:     "landing",
	})
	if err != nil {
		t.Fatalf("CreateInstallIntent: %v", err)
	}
	state := installStateFromAuthorizeURL(t, intent.AuthorizeURL)

	result, err := service.HandleOAuthCallback(ctx, InstallCallbackRequest{
		State:              state,
		Code:               "code-1",
		GuildID:            "guild-1",
		PermissionBitfield: "1024",
	})
	if err != nil {
		t.Fatalf("HandleOAuthCallback: %v", err)
	}
	if oauth.code != "code-1" {
		t.Fatalf("expected OAuth exchange code to be used, got %q", oauth.code)
	}
	if verifier.request.GuildID != "guild-1" || verifier.request.InstallerUserID != "installer-1" || verifier.request.UserAccessToken != "access-1" {
		t.Fatalf("unexpected verifier request: %+v", verifier.request)
	}
	if result.GuildID != "guild-1" || result.InstallerUserID != "installer-1" || result.IntentID != intent.IntentID {
		t.Fatalf("unexpected callback result: %+v", result)
	}

	guild, ok, err := guilds.Get(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("Get guild: ok=%t err=%v", ok, err)
	}
	if guild.Name != "Verified Guild" || guild.OwnerUserID != "owner-1" || guild.InstalledByUserID != "installer-1" {
		t.Fatalf("unexpected verified guild: %+v", guild)
	}
	consumed, ok, err := featureRepo.GetInstallIntentByStateHash(ctx, hashInstallState(state))
	if err != nil || !ok {
		t.Fatalf("GetInstallIntentByStateHash: ok=%t err=%v", ok, err)
	}
	if consumed.Status != repository.InstallIntentStatusConsumed || consumed.GuildID != "guild-1" {
		t.Fatalf("expected verified install intent to be consumed: %+v", consumed)
	}
	assertAuditAction(t, db, "discord.install.authorized")
	assertAuditAction(t, db, "guild_features.install_bound")
}

func TestInstallServiceKeepsOAuthErrorWhenGuildCannotBeVerified(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	featureRepo := repository.NewFeatureRepository(db.DB)
	oauthErr := errors.New("discord oauth exchange failed with status 400")
	service := NewInstallService(repository.NewGuildRepository(db.DB), repository.NewAuditRepository(db.DB)).
		WithFeatureRepository(featureRepo).
		WithOAuthClient(&fakeOAuthClient{err: oauthErr}).
		WithGuildInstallVerifier(&fakeGuildInstallVerifier{ok: false}).
		WithInstallConfig(InstallConfig{
			ApplicationID: "app-1",
			RedirectURI:   "https://api.example.test/discord/install/callback",
		})

	intent, err := service.CreateInstallIntent(ctx, CreateInstallIntentRequest{
		FeatureIDs: []string{featurecatalog.AssistantChat},
		Source:     "landing",
	})
	if err != nil {
		t.Fatalf("CreateInstallIntent: %v", err)
	}
	state := installStateFromAuthorizeURL(t, intent.AuthorizeURL)

	_, err = service.HandleOAuthCallback(ctx, InstallCallbackRequest{
		State:   state,
		Code:    "code-1",
		GuildID: "guild-1",
	})
	if !errors.Is(err, oauthErr) {
		t.Fatalf("expected original OAuth error, got %v", err)
	}
}

func TestDiscordRESTInstallVerifierFetchesInstallerUserFromAccessToken(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Fatalf("missing authorization header for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/users/@me":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				t.Fatalf("unexpected user auth header: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"id":"installer-1"}`))
		case "/users/@me/guilds":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				t.Fatalf("unexpected guild list auth header: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`[{"id":"guild-1","name":"Verified Guild","owner":false,"permissions":"32"}]`))
		case "/guilds/guild-1":
			if r.Header.Get("Authorization") != "Bot bot-token" {
				t.Fatalf("unexpected bot auth header: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"id":"guild-1","name":"Verified Guild","owner_id":"owner-1","preferred_locale":"en-US"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	verifier := &discordRESTInstallVerifier{
		client:   server.Client(),
		botToken: "bot-token",
		baseURL:  server.URL,
	}
	install, ok, err := verifier.VerifyGuildInstall(ctx, GuildInstallVerificationRequest{
		GuildID:         "guild-1",
		UserAccessToken: "access-1",
		AuthorizedAt:    time.Date(2026, 6, 22, 18, 0, 0, 0, time.UTC),
	})
	if err != nil || !ok {
		t.Fatalf("VerifyGuildInstall: ok=%t err=%v", ok, err)
	}
	if install.GuildID != "guild-1" || install.OwnerUserID != "owner-1" || install.InstalledByUserID != "installer-1" {
		t.Fatalf("unexpected verified install: %+v", install)
	}
}

func TestInstallServiceBindsFeaturesFromRecordedWebhookWhenBotRedirectHasNoCode(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	featureRepo := repository.NewFeatureRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB)).
		WithFeatureRepository(featureRepo).
		WithInstallConfig(InstallConfig{
			ApplicationID:   "app-1",
			RedirectURI:     "https://api.example.test/discord/install/callback",
			SuccessRedirect: "https://panda.example.test/install/success",
			FailureRedirect: "https://panda.example.test/install/failed",
		})
	service.webhookDetectionTimeout = 0

	intent, err := service.CreateInstallIntent(ctx, CreateInstallIntentRequest{
		FeatureIDs: []string{featurecatalog.AssistantChat},
		Source:     "landing",
	})
	if err != nil {
		t.Fatalf("CreateInstallIntent: %v", err)
	}
	state := installStateFromAuthorizeURL(t, intent.AuthorizeURL)

	event := authorizedEvent(t, "installer-1", "owner-1")
	event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	if err := service.HandleWebhookEvent(ctx, event); err != nil {
		t.Fatalf("HandleWebhookEvent: %v", err)
	}

	result, err := service.HandleOAuthCallback(ctx, InstallCallbackRequest{
		State:              state,
		GuildID:            "guild-1",
		PermissionBitfield: "1024",
	})
	if err != nil {
		t.Fatalf("HandleOAuthCallback: %v", err)
	}
	if result.GuildID != "guild-1" || result.InstallerUserID != "installer-1" || result.IntentID != intent.IntentID {
		t.Fatalf("unexpected callback result: %+v", result)
	}
	if !strings.HasPrefix(result.RedirectURL, "https://panda.example.test/install/success?") || !strings.Contains(result.RedirectURL, "guild_id=guild-1") {
		t.Fatalf("unexpected success redirect: %q", result.RedirectURL)
	}

	consumed, ok, err := featureRepo.GetInstallIntentByStateHash(ctx, hashInstallState(state))
	if err != nil || !ok {
		t.Fatalf("GetInstallIntentByStateHash: ok=%t err=%v", ok, err)
	}
	if consumed.Status != repository.InstallIntentStatusConsumed || consumed.GuildID != "guild-1" || consumed.InstallerUserID != "installer-1" {
		t.Fatalf("unexpected consumed intent: %+v", consumed)
	}
	enabled, err := featureRepo.EnabledFeatureSet(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnabledFeatureSet: %v", err)
	}
	if _, ok := enabled[featurecatalog.AssistantChat]; !ok {
		t.Fatalf("expected assistant_chat to be enabled, got %+v", enabled)
	}
}

func TestInstallServiceRejectsStaleGuildHintWithoutCurrentInstall(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	featureRepo := repository.NewFeatureRepository(db.DB)
	service := NewInstallService(guilds, repository.NewAuditRepository(db.DB)).
		WithFeatureRepository(featureRepo).
		WithInstallConfig(InstallConfig{
			ApplicationID: "app-1",
			RedirectURI:   "https://api.example.test/discord/install/callback",
		})
	service.webhookDetectionTimeout = 0

	if _, err := guilds.RecordAuthorizedInstall(ctx, repository.GuildInstall{
		GuildID:           "guild-1",
		Name:              "Old Guild",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "installer-1",
		Locale:            "en-US",
		AuthorizedAt:      time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}

	intent, err := service.CreateInstallIntent(ctx, CreateInstallIntentRequest{
		FeatureIDs: []string{featurecatalog.AssistantChat},
		Source:     "landing",
	})
	if err != nil {
		t.Fatalf("CreateInstallIntent: %v", err)
	}
	state := installStateFromAuthorizeURL(t, intent.AuthorizeURL)

	if _, err := service.HandleOAuthCallback(ctx, InstallCallbackRequest{
		State:              state,
		GuildID:            "guild-1",
		PermissionBitfield: "1024",
	}); err == nil || !strings.Contains(err.Error(), "discord oauth code is required") {
		t.Fatalf("expected missing code error for stale guild hint, got %v", err)
	}

	stored, ok, err := featureRepo.GetInstallIntentByStateHash(ctx, hashInstallState(state))
	if err != nil || !ok {
		t.Fatalf("GetInstallIntentByStateHash: ok=%t err=%v", ok, err)
	}
	if stored.Status != repository.InstallIntentStatusPending || stored.GuildID != "" {
		t.Fatalf("stale hint should not consume intent: %+v", stored)
	}
}

func authorizedEvent(t *testing.T, installerID, ownerID string) WebhookEvent {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"integration_type": 0,
		"scopes":           []string{"bot", "applications.commands"},
		"user": map[string]any{
			"id":       installerID,
			"username": "installer",
		},
		"guild": map[string]any{
			"id":               "guild-1",
			"name":             "Test Guild",
			"owner_id":         ownerID,
			"preferred_locale": "en-US",
		},
	})
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	return WebhookEvent{
		Type:      webhookEventApplicationAuthorized,
		Timestamp: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Data:      data,
	}
}

func installStateFromAuthorizeURL(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	state := strings.TrimSpace(parsed.Query().Get("state"))
	if state == "" {
		t.Fatalf("authorize URL did not include state: %s", rawURL)
	}
	return state
}

func assertAuditAction(t *testing.T, db *store.Store, action string) {
	t.Helper()
	var count int64
	if err := db.DB.Model(&store.AuditEvent{}).Where("action = ?", action).Count(&count).Error; err != nil {
		t.Fatalf("count audit action %s: %v", action, err)
	}
	if count != 1 {
		t.Fatalf("expected one %s audit event, got %d", action, count)
	}
}
