package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/store"
)

type fakeAttachmentRecorder struct {
	records []store.Attachment
}

type fakeInteractionJobQueue struct {
	jobs []store.Job
}

func (f *fakeAttachmentRecorder) Record(_ context.Context, attachment store.Attachment) (store.Attachment, error) {
	attachment.ID = uint(len(f.records) + 1)
	f.records = append(f.records, attachment)
	return attachment, nil
}

func (f *fakeInteractionJobQueue) Enqueue(_ context.Context, job store.Job) (store.Job, error) {
	job.ID = uint(len(f.jobs) + 1)
	f.jobs = append(f.jobs, job)
	return job, nil
}

func TestApplicationCommandsIncludeContextMenus(t *testing.T) {
	commands := applicationCommands()
	names := map[string]bool{}
	for _, command := range commands {
		names[command.CommandName()] = true
	}
	for _, name := range []string{"Explain with Panda", "Summarize with Panda", "admin", "ops", "help", "ping"} {
		if !names[name] {
			t.Fatalf("expected command %q to be registered", name)
		}
	}
	for _, name := range []string{"ask", "chat", "summarize", "explain", "rewrite", "translate", "memory-consent", "search-memory", "mod"} {
		if names[name] {
			t.Fatalf("expected natural-language command %q not to be registered as a slash command", name)
		}
	}
}

func TestMessageMentionsUserUsesMentionsAndContentFallback(t *testing.T) {
	userID := snowflake.MustParse("100000000000000001")
	if !messageMentionsUser(disgoDiscord.Message{Mentions: []disgoDiscord.User{{ID: userID}}}, userID.String()) {
		t.Fatal("expected explicit mention metadata to match")
	}
	if !messageMentionsUser(disgoDiscord.Message{Content: "<@!100000000000000001> hello"}, userID.String()) {
		t.Fatal("expected mention content fallback to match")
	}
	if messageMentionsUser(disgoDiscord.Message{Content: "<@!100000000000000002> hello"}, userID.String()) {
		t.Fatal("expected other user mention not to match")
	}
}

func TestContainsPandaWordUsesStandaloneWord(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "plain", content: "Panda is deploy Friday?", want: true},
		{name: "lowercase", content: "hey panda, is deploy Friday?", want: true},
		{name: "possessive", content: "Panda's answer?", want: true},
		{name: "hyphenated", content: "red-panda facts", want: true},
		{name: "prefix", content: "pandanotes are ready", want: false},
		{name: "suffix", content: "Ask PandaBot", want: false},
		{name: "absent", content: "ambient channel chatter", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := containsPandaWord(test.content); got != test.want {
				t.Fatalf("containsPandaWord(%q) = %t; want %t", test.content, got, test.want)
			}
		})
	}
}

func TestCaptureAttachmentsRecordsSafeExtractedText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notes.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("deploy notes from attachment"))
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("not text"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	recorder := &fakeAttachmentRecorder{}
	bot := (&Bot{httpClient: server.Client()}).WithAttachmentRecorder(recorder)
	guildID := snowflake.MustParse("100000000000000001")
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000003")
	textType := "text/plain"
	imageType := "image/png"

	bot.captureAttachments(context.Background(), disgoDiscord.Message{
		ID:        messageID,
		GuildID:   &guildID,
		ChannelID: channelID,
		Attachments: []disgoDiscord.Attachment{
			{Filename: "notes.txt", ContentType: &textType, URL: server.URL + "/notes.txt", Size: 28},
			{Filename: "image.png", ContentType: &imageType, URL: server.URL + "/image.png", Size: 8},
		},
	})

	if len(recorder.records) != 1 {
		t.Fatalf("expected one extracted attachment record, got %+v", recorder.records)
	}
	record := recorder.records[0]
	if record.GuildID != guildID.String() || record.ChannelID != channelID.String() || record.MessageID != messageID.String() || record.Filename != "notes.txt" || record.ExtractedText != "deploy notes from attachment" || record.TempPath != "" {
		t.Fatalf("unexpected attachment record: %+v", record)
	}
}

func TestQueueBackgroundInteractionStoresPayload(t *testing.T) {
	queue := &fakeInteractionJobQueue{}
	bot := (&Bot{}).WithJobQueue(queue)
	applicationID := snowflake.MustParse("100000000000000010")

	response := bot.queueBackgroundInteraction(context.Background(), applicationID, "token-1", "guild-1", commands.BackgroundTask{
		GuildID:   "guild-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Command:   "summarize",
		Input:     "long text",
	})
	if !strings.Contains(response.Content, "job #1") {
		t.Fatalf("expected queued job response, got %+v", response)
	}
	if len(queue.jobs) != 1 || queue.jobs[0].Kind != InteractionJobKind || queue.jobs[0].GuildID != "guild-1" {
		t.Fatalf("unexpected queued jobs: %+v", queue.jobs)
	}
	var payload interactionJobPayload
	if err := json.Unmarshal([]byte(queue.jobs[0].Payload), &payload); err != nil {
		t.Fatalf("decode job payload: %v", err)
	}
	if payload.ApplicationID != applicationID.String() || payload.Token != "token-1" || payload.Task.Command != "summarize" || payload.Task.Input != "long text" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestDeferredProgressContentUsesCommandAction(t *testing.T) {
	if got := deferredProgressContent("summarize", 1); got != "Summarizing..." {
		t.Fatalf("unexpected summarize progress %q", got)
	}
	if got := deferredProgressContent("ask", 2); got != "Thinking... still working" {
		t.Fatalf("unexpected ask progress %q", got)
	}
}

func TestAdminModelCommandIncludesRuntimeOptions(t *testing.T) {
	var modelCommand *disgoDiscord.ApplicationCommandOptionSubCommand
	for _, command := range applicationCommands() {
		slash, ok := command.(disgoDiscord.SlashCommandCreate)
		if !ok || slash.Name != "admin" {
			continue
		}
		for _, option := range slash.Options {
			subcommand, ok := option.(disgoDiscord.ApplicationCommandOptionSubCommand)
			if ok && subcommand.Name == "model" {
				modelCommand = &subcommand
				break
			}
		}
	}
	if modelCommand == nil {
		t.Fatal("expected /admin model subcommand")
	}

	optionNames := map[string]bool{}
	for _, option := range modelCommand.Options {
		switch typed := option.(type) {
		case disgoDiscord.ApplicationCommandOptionString:
			optionNames[typed.Name] = true
			if typed.Name == "model" && typed.Required {
				t.Fatal("model option should be optional so runtime settings can be updated independently")
			}
		}
	}
	for _, name := range []string{"model", "fallback_models", "temperature", "max_response_tokens", "tool_policy"} {
		if !optionNames[name] {
			t.Fatalf("expected /admin model option %q", name)
		}
	}
}

func TestAdminToggleCommandsIncludeSafetyOptions(t *testing.T) {
	adminCommand := adminSlashCommand(t)
	disable := adminSubcommand(t, adminCommand, "disable")
	if !subcommandHasStringOption(disable, "confirm") {
		t.Fatal("expected /admin disable to include confirm option")
	}
	for _, subcommandName := range []string{"model", "prompt", "enable", "disable"} {
		subcommand := adminSubcommand(t, adminCommand, subcommandName)
		if !subcommandHasBoolOption(subcommand, "dry_run") {
			t.Fatalf("expected /admin %s to include dry_run option", subcommandName)
		}
	}
}

func TestConfirmationResponseRendersButtons(t *testing.T) {
	response := commands.Response{
		Content:   "Danger ahead.",
		Ephemeral: true,
		Confirmation: &commands.Confirmation{
			ID:           "p2c:md:admin:1",
			ConfirmLabel: "Delete document",
			CancelID:     commands.ConfirmationCancelID,
			CancelLabel:  "Cancel",
			Danger:       true,
		},
	}

	message := messageCreateFromResponse(response)
	if len(message.Components) != 1 {
		t.Fatalf("expected one action row, got %+v", message.Components)
	}
	row, ok := message.Components[0].(disgoDiscord.ActionRowComponent)
	if !ok || len(row.Components) != 2 {
		t.Fatalf("expected action row with two buttons, got %+v", message.Components[0])
	}
	confirm, ok := row.Components[0].(disgoDiscord.ButtonComponent)
	if !ok || confirm.CustomID != response.Confirmation.ID || confirm.Style != disgoDiscord.ButtonStyleDanger {
		t.Fatalf("unexpected confirm button: %+v", row.Components[0])
	}
	cancel, ok := row.Components[1].(disgoDiscord.ButtonComponent)
	if !ok || cancel.CustomID != commands.ConfirmationCancelID || cancel.Style != disgoDiscord.ButtonStyleSecondary {
		t.Fatalf("unexpected cancel button: %+v", row.Components[1])
	}
}

func TestModalResponseRendersModal(t *testing.T) {
	response := &commands.Modal{
		ID:    "p2m:prompt:admin",
		Title: "Server Prompt",
		Inputs: []commands.ModalInput{
			{
				ID:          commands.ModalPromptInput,
				Label:       "Prompt",
				Placeholder: "Server-specific assistant instructions",
				Required:    true,
				MaxLength:   4000,
				Paragraph:   true,
			},
		},
	}

	modal := modalCreateFromResponse(response)
	if modal.CustomID != response.ID || modal.Title != response.Title || len(modal.Components) != 1 {
		t.Fatalf("unexpected modal create: %+v", modal)
	}
	label, ok := modal.Components[0].(disgoDiscord.LabelComponent)
	if !ok || label.Label != "Prompt" {
		t.Fatalf("expected prompt label component, got %+v", modal.Components[0])
	}
	input, ok := label.Component.(disgoDiscord.TextInputComponent)
	if !ok || input.CustomID != commands.ModalPromptInput || input.Style != disgoDiscord.TextInputStyleParagraph || input.MaxLength != 4000 || !input.Required {
		t.Fatalf("unexpected prompt input: %+v", label.Component)
	}
}

func TestAdminPromptCommandCanOpenModal(t *testing.T) {
	prompt := adminSubcommand(t, adminSlashCommand(t), "prompt")
	option := subcommandStringOption(t, prompt, "prompt")
	if option.Required {
		t.Fatal("prompt option should be optional so the modal flow can open")
	}
}

func TestThreadNoticeMentionsThread(t *testing.T) {
	got := threadNotice(commands.Response{ThreadID: "12345", ThreadName: "Panda chat"})
	if got != "Continued this chat in <#12345> (`Panda chat`)." {
		t.Fatalf("unexpected thread notice %q", got)
	}
}

func adminSlashCommand(t *testing.T) disgoDiscord.SlashCommandCreate {
	t.Helper()
	return slashCommand(t, "admin")
}

func slashCommand(t *testing.T, name string) disgoDiscord.SlashCommandCreate {
	t.Helper()
	for _, command := range applicationCommands() {
		slash, ok := command.(disgoDiscord.SlashCommandCreate)
		if ok && slash.Name == name {
			return slash
		}
	}
	t.Fatalf("expected /%s command", name)
	return disgoDiscord.SlashCommandCreate{}
}

func adminSubcommand(t *testing.T, command disgoDiscord.SlashCommandCreate, name string) disgoDiscord.ApplicationCommandOptionSubCommand {
	t.Helper()
	return commandSubcommand(t, command, name)
}

func commandSubcommand(t *testing.T, command disgoDiscord.SlashCommandCreate, name string) disgoDiscord.ApplicationCommandOptionSubCommand {
	t.Helper()
	for _, option := range command.Options {
		subcommand, ok := option.(disgoDiscord.ApplicationCommandOptionSubCommand)
		if ok && subcommand.Name == name {
			return subcommand
		}
	}
	t.Fatalf("expected /%s %s subcommand", command.Name, name)
	return disgoDiscord.ApplicationCommandOptionSubCommand{}
}

func subcommandHasStringOption(subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) bool {
	_, ok := findSubcommandStringOption(subcommand, name)
	return ok
}

func subcommandStringOption(t *testing.T, subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) disgoDiscord.ApplicationCommandOptionString {
	t.Helper()
	option, ok := findSubcommandStringOption(subcommand, name)
	if !ok {
		t.Fatalf("expected string option %q on subcommand %s", name, subcommand.Name)
	}
	return option
}

func findSubcommandStringOption(subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) (disgoDiscord.ApplicationCommandOptionString, bool) {
	for _, option := range subcommand.Options {
		stringOption, ok := option.(disgoDiscord.ApplicationCommandOptionString)
		if ok && stringOption.Name == name {
			return stringOption, true
		}
	}
	return disgoDiscord.ApplicationCommandOptionString{}, false
}

func subcommandHasBoolOption(subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) bool {
	for _, option := range subcommand.Options {
		boolOption, ok := option.(disgoDiscord.ApplicationCommandOptionBool)
		if ok && boolOption.Name == name {
			return true
		}
	}
	return false
}
