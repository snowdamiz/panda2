package features

import (
	"errors"
	"testing"

	disgoDiscord "github.com/disgoorg/disgo/discord"
)

func TestCalculateRejectsUnknownFeature(t *testing.T) {
	if _, err := Calculate([]string{"assistant_chat", "unknown"}, true); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("expected ErrUnknownFeature, got %v", err)
	}
}

func TestCalculateRejectsInternalPublicFeature(t *testing.T) {
	if _, err := Calculate([]string{OwnerOps}, true); !errors.Is(err, ErrInternalFeature) {
		t.Fatalf("expected ErrInternalFeature, got %v", err)
	}
}

func TestCalculateExpandsDependenciesAndBitfield(t *testing.T) {
	selection, err := Calculate([]string{Threads}, true)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	got := FeatureSet(selection.ExpandedFeatureIDs)
	for _, want := range []string{AssistantChat, Threads} {
		if !Has(got, want) {
			t.Fatalf("expected expanded feature %s in %+v", want, selection.ExpandedFeatureIDs)
		}
	}
	wantBits := int64(disgoDiscord.PermissionViewChannel |
		disgoDiscord.PermissionSendMessages |
		disgoDiscord.PermissionReadMessageHistory |
		disgoDiscord.PermissionEmbedLinks |
		disgoDiscord.PermissionCreatePublicThreads |
		disgoDiscord.PermissionSendMessagesInThreads |
		disgoDiscord.PermissionManageThreads)
	if selection.DiscordPermissionBitfield64 != wantBits {
		t.Fatalf("unexpected bitfield: got %d want %d (%+v)", selection.DiscordPermissionBitfield64, wantBits, selection.DiscordPermissionNames)
	}
}

func TestPublicCatalogExcludesOwnerOps(t *testing.T) {
	for _, feature := range PublicCatalog() {
		if feature.ID == OwnerOps {
			t.Fatal("owner ops must not be public-selectable")
		}
	}
}

func TestDefaultInstallPresetIncludesAdminAndAutomationFeatures(t *testing.T) {
	defaults := FeatureSet(DefaultInstallPreset())
	for _, want := range []string{Threads, AdminSetup, AdminAccessControl, AdminAudit, ComposedTools} {
		if !Has(defaults, want) {
			t.Fatalf("expected default install preset to include %s, got %+v", want, DefaultInstallPreset())
		}
	}
	if _, err := Calculate(DefaultInstallPreset(), true); err != nil {
		t.Fatalf("default install preset must be public-selectable: %v", err)
	}
}

func TestDefaultInstallScopesSupportOAuthInstallVerification(t *testing.T) {
	scopes := FeatureSet(DefaultInstallScopes())
	for _, want := range []string{"bot", "applications.commands", "identify", "guilds"} {
		if !Has(scopes, want) {
			t.Fatalf("expected default install scopes to include %s, got %+v", want, DefaultInstallScopes())
		}
	}
}
