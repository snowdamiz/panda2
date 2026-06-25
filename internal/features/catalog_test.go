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

func TestDefaultInstallPresetIncludesAdminAutomationAndChannelMessages(t *testing.T) {
	defaults := FeatureSet(DefaultInstallPreset())
	for _, want := range []string{Threads, WebSearch, ImageGeneration, AdminSetup, AdminAccessControl, AdminAudit, ComposedTools, DiscordMessages} {
		if !Has(defaults, want) {
			t.Fatalf("expected default install preset to include %s, got %+v", want, DefaultInstallPreset())
		}
	}
	if Has(defaults, DiscordMessageActions) {
		t.Fatalf("default install preset should not include server message management, got %+v", DefaultInstallPreset())
	}
	selection, err := Calculate(DefaultInstallPreset(), true)
	if err != nil {
		t.Fatalf("default install preset must be public-selectable: %v", err)
	}
	permissions := FeatureSet(selection.DiscordPermissionNames)
	for _, heavy := range []string{"MANAGE_MESSAGES", "PIN_MESSAGES", "ADD_REACTIONS"} {
		if Has(permissions, heavy) {
			t.Fatalf("default channel messages should not request %s, got %+v", heavy, selection.DiscordPermissionNames)
		}
	}
}

func TestImageGenerationFeatureRequestsAttachFiles(t *testing.T) {
	selection, err := Calculate([]string{ImageGeneration}, true)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	features := FeatureSet(selection.ExpandedFeatureIDs)
	for _, want := range []string{AssistantChat, ImageGeneration} {
		if !Has(features, want) {
			t.Fatalf("expected expanded feature %s in %+v", want, selection.ExpandedFeatureIDs)
		}
	}
	permissions := FeatureSet(selection.DiscordPermissionNames)
	for _, want := range []string{"VIEW_CHANNEL", "SEND_MESSAGES", "ATTACH_FILES"} {
		if !Has(permissions, want) {
			t.Fatalf("expected image generation to request %s, got %+v", want, selection.DiscordPermissionNames)
		}
	}
	if selection.DiscordPermissionBitfield64&int64(disgoDiscord.PermissionAttachFiles) == 0 {
		t.Fatalf("expected Attach Files bit in %d", selection.DiscordPermissionBitfield64)
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
