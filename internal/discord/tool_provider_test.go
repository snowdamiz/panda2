package discord

import (
	"sort"
	"strings"
	"testing"

	disgoDiscord "github.com/disgoorg/disgo/discord"
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

func TestPollFromArgumentsParsesNativePollPayload(t *testing.T) {
	poll, err := pollFromArguments(map[string]any{
		"question":          "Where should lunch be?",
		"answers":           []any{"Tacos", map[string]any{"text": "Pizza", "emoji": "123456789012345678"}},
		"answer_emojis":     []any{"taco"},
		"duration_hours":    float64(8),
		"allow_multiselect": true,
	})
	if err != nil {
		t.Fatalf("pollFromArguments: %v", err)
	}
	if poll.Question != "Where should lunch be?" || len(poll.Answers) != 2 || poll.DurationHours != 8 || !poll.AllowMultiselect {
		t.Fatalf("unexpected poll: %+v", poll)
	}
	if poll.Answers[0].Emoji != "taco" || poll.Answers[1].Emoji != "123456789012345678" {
		t.Fatalf("unexpected answer emojis: %+v", poll.Answers)
	}
}

func TestPollFromArgumentsRejectsInvalidNativePollPayload(t *testing.T) {
	_, err := pollFromArguments(map[string]any{
		"question": "Lunch?",
		"answers":  "Only one",
	})
	if err == nil || !strings.Contains(err.Error(), "at least 2 answers") {
		t.Fatalf("expected answer count validation, got %v", err)
	}
}

func TestMessageSummaryIncludesNativePollDetails(t *testing.T) {
	question := "Pick one"
	first := "Red"
	second := "Blue"
	firstID := 1
	secondID := 2
	message := disgoDiscord.Message{
		ID:        snowflake.MustParse("100000000000000001"),
		ChannelID: snowflake.MustParse("100000000000000002"),
		Author:    disgoDiscord.User{ID: snowflake.MustParse("100000000000000003"), Username: "panda"},
		Poll: &disgoDiscord.Poll{
			Question: disgoDiscord.PollMedia{Text: &question},
			Answers: []disgoDiscord.PollAnswer{
				{AnswerID: &firstID, PollMedia: disgoDiscord.PollMedia{Text: &first}},
				{AnswerID: &secondID, PollMedia: disgoDiscord.PollMedia{Text: &second}},
			},
			AllowMultiselect: true,
			Results: &disgoDiscord.PollResults{AnswerCounts: []disgoDiscord.PollAnswerCount{
				{ID: 1, Count: 3},
				{ID: 2, Count: 5},
			}},
		},
	}
	summary := messageSummary(message)
	pollSummary, ok := summary["poll"].(map[string]any)
	if !ok {
		t.Fatalf("expected poll summary, got %+v", summary)
	}
	if pollSummary["question"] != question || pollSummary["allow_multiselect"] != true {
		t.Fatalf("unexpected poll summary: %+v", pollSummary)
	}
}
