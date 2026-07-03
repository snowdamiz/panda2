package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	stdhttp "net/http"

	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
	"github.com/sn0w/panda2/internal/features"
	"github.com/sn0w/panda2/internal/repository"
	setupsvc "github.com/sn0w/panda2/internal/setup"
	"github.com/sn0w/panda2/internal/store"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

func TestInstallFeaturesOmitSetupTemplatesAndIntentMetadataPassThrough(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	installHandler := &fakeSetupInstallHandler{
		result: discordbot.CreateInstallIntentResult{
			IntentID:     "intent-1",
			AuthorizeURL: "https://discord.example/authorize",
			ExpiresAt:    time.Now().UTC().Add(time.Hour),
			Selection: features.Selection{
				SelectedFeatureIDs:        []string{features.AssistantChat},
				ExpandedFeatureIDs:        []string{features.AssistantChat},
				DiscordPermissionNames:    []string{"SEND_MESSAGES"},
				DiscordPermissionBitfield: "2048",
				Scopes:                    []string{"bot"},
			},
		},
	}
	server := New(testHTTPConfig(), db).
		WithInstallHandler(installHandler)

	req, _ := stdhttp.NewRequest(stdhttp.MethodGet, "/install/features", nil)
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("install features: %v", err)
	}
	defer resp.Body.Close()
	var catalog map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if _, ok := catalog["setup_templates"]; ok {
		t.Fatalf("setup templates should not be exposed in install catalog: %+v", catalog["setup_templates"])
	}

	body := `{"feature_ids":["assistant_chat"],"source":"landing","metadata":{"path":"/","timezone":"America/Los_Angeles"}}`
	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/install/intents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("create install intent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	if installHandler.request.DesiredPlan != "" || installHandler.request.Metadata["path"] != "/" || installHandler.request.Metadata["setup_template_id"] != nil {
		t.Fatalf("unexpected install metadata: %+v", installHandler.request)
	}
}

func TestPortalSetupPreviewRequiresGuildOwnerOrInstaller(t *testing.T) {
	db, err := store.Open(t.Context(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	guilds := repository.NewGuildRepository(db.DB)
	if _, err := guilds.RecordAuthorizedInstall(t.Context(), repository.GuildInstall{
		GuildID:           "guild-1",
		Name:              "Panda Guild",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "installer-1",
		AuthorizedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}
	setupHandler := &fakeHTTPSetupHandler{}
	server := New(testHTTPConfig(), db).
		WithGuildRepository(guilds).
		WithSetupService(setupHandler)

	otherToken, err := server.signPortalToken(portalSession{UserID: "other-1"})
	if err != nil {
		t.Fatalf("sign other token: %v", err)
	}
	req, _ := stdhttp.NewRequest(stdhttp.MethodPost, "/portal/setup/preview", strings.NewReader(`{"guild_id":"guild-1","template_id":"minimal_community"}`))
	req.Header.Set("Authorization", "Bearer "+otherToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("forbidden preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusForbidden {
		t.Fatalf("expected 403 for non-owner preview, got %d", resp.StatusCode)
	}

	ownerToken, err := server.signPortalToken(portalSession{UserID: "owner-1"})
	if err != nil {
		t.Fatalf("sign owner token: %v", err)
	}
	req, _ = stdhttp.NewRequest(stdhttp.MethodPost, "/portal/setup/preview", strings.NewReader(`{"guild_id":"guild-1","template_id":"minimal_community","variables":{"member_role":"Member"}}`))
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = server.Test(req)
	if err != nil {
		t.Fatalf("owner preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("expected 200 for owner preview, got %d", resp.StatusCode)
	}
	if setupHandler.previewActor != "owner-1" || setupHandler.previewGuild != "guild-1" {
		t.Fatalf("unexpected preview request: actor=%q guild=%q", setupHandler.previewActor, setupHandler.previewGuild)
	}
}

func testHTTPConfig() config.Config {
	return config.Config{
		SQLitePath:          ":memory:",
		DataDir:             "/tmp",
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		OpenRouterModel:     "openrouter/auto",
		Port:                "8080",
		UserRateLimit:       5,
		UserRateLimitWindow: time.Minute,
		PortalSessionSecret: "portal-secret",
	}
}

type fakeSetupInstallHandler struct {
	request discordbot.CreateInstallIntentRequest
	result  discordbot.CreateInstallIntentResult
}

func (f *fakeSetupInstallHandler) CreateInstallIntent(_ context.Context, request discordbot.CreateInstallIntentRequest) (discordbot.CreateInstallIntentResult, error) {
	f.request = request
	return f.result, nil
}

func (f *fakeSetupInstallHandler) HandleOAuthCallback(context.Context, discordbot.InstallCallbackRequest) (discordbot.InstallCallbackResult, error) {
	return discordbot.InstallCallbackResult{}, nil
}

type fakeHTTPSetupHandler struct {
	previewActor string
	previewGuild string
}

func (f *fakeHTTPSetupHandler) Catalog(context.Context) ([]setupsvc.Template, error) {
	return []setupsvc.Template{{
		ID:               "minimal_community",
		SchemaVersion:    setupsvc.SchemaVersion,
		TemplateVersion:  1,
		Name:             "Minimal Community",
		Description:      "A test template",
		ReleaseState:     "stable",
		DefaultVariables: map[string]string{"member_role": "Member"},
	}}, nil
}

func (f *fakeHTTPSetupHandler) Preview(_ context.Context, request setupsvc.SetupRequest) (store.GuildSetupProject, setupsvc.Preview, error) {
	f.previewActor = request.ActorID
	f.previewGuild = request.GuildID
	return store.GuildSetupProject{
			ID:              "project-1",
			GuildID:         request.GuildID,
			TemplateID:      request.TemplateID,
			TemplateVersion: 1,
			SchemaVersion:   setupsvc.SchemaVersion,
			Status:          setupsvc.ProjectStatusPreviewed,
			ActorID:         request.ActorID,
			CreatedAt:       time.Now().UTC(),
		}, setupsvc.Preview{
			ProjectID:    "project-1",
			TemplateID:   request.TemplateID,
			TemplateName: "Minimal Community",
		}, nil
}

func (f *fakeHTTPSetupHandler) Confirm(context.Context, string, string, bool) (store.GuildSetupProject, error) {
	return store.GuildSetupProject{}, nil
}

func (f *fakeHTTPSetupHandler) RollbackProject(context.Context, string, string) (setupsvc.ApplyResult, error) {
	return setupsvc.ApplyResult{ProjectID: "project-1", Status: setupsvc.ProjectStatusRolledBack}, nil
}

func (f *fakeHTTPSetupHandler) ManageServerSetup(context.Context, toolsvc.ServerSetupManagementRequest) (any, error) {
	return map[string]any{"project": map[string]any{"id": "project-1"}}, nil
}

func (f *fakeHTTPSetupHandler) ManageTicket(context.Context, toolsvc.TicketManagementRequest) (any, error) {
	return map[string]any{"tickets": []any{}}, nil
}

func (f *fakeHTTPSetupHandler) ManageOnboarding(context.Context, toolsvc.OnboardingManagementRequest) (any, error) {
	return map[string]any{"flows": []any{}}, nil
}
