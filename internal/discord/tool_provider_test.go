package discord

import (
	"sort"
	"strings"
	"testing"

	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/tools"
)

func TestDiscordToolProviderCoversRegisteredDiscordTools(t *testing.T) {
	registry, err := tools.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	handlers := (&ToolProvider{}).discordToolHandlers()
	var missing []string
	for _, definition := range registry.Definitions() {
		if !strings.HasPrefix(definition.Name, "discord.") {
			continue
		}
		if _, ok := handlers[definition.Name]; !ok {
			missing = append(missing, definition.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("registered Discord tools missing provider handlers: %s", strings.Join(missing, ", "))
	}
}

func TestDiscordToolPreflightRequiresGuildForPermissionChecks(t *testing.T) {
	provider := &ToolProvider{botUserID: snowflake.MustParse("100000000000000001")}
	err := provider.preflight(tools.DiscordToolRequest{
		Arguments:   map[string]any{},
		Permissions: []string{"SEND_MESSAGES"},
	})
	if err == nil || !strings.Contains(err.Error(), "guild_id is required") {
		t.Fatalf("expected missing guild preflight error, got %v", err)
	}
}
