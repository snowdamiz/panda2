package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/polls"
	"github.com/sn0w/panda2/internal/store"
)

type fakeAttachmentRecorder struct {
	records []store.Attachment
}

type fakeInteractionJobQueue struct {
	jobs []store.Job
}

type fakeDiscordEventRecorder struct {
	records []store.DiscordEvent
}

type fakeTypingSender struct {
	mu    sync.Mutex
	calls []snowflake.ID
	err   error
}

type fakeGuildGetter struct {
	guildID    snowflake.ID
	withCounts bool
	calls      int
	guild      *disgoDiscord.RestGuild
	err        error
}

type syncedGuildCommands struct {
	applicationID snowflake.ID
	guildID       snowflake.ID
	commands      []disgoDiscord.ApplicationCommandCreate
}

type fakeCommandSyncer struct {
	globalCommands        [][]disgoDiscord.ApplicationCommandCreate
	globalApplicationIDs  []snowflake.ID
	guildCommands         []syncedGuildCommands
	globalRegistrationErr error
	guildRegistrationErr  error
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

func (f *fakeDiscordEventRecorder) Record(_ context.Context, event store.DiscordEvent) (store.DiscordEvent, error) {
	event.ID = uint(len(f.records) + 1)
	event.CreatedAt = time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)
	f.records = append(f.records, event)
	return event, nil
}

func (f *fakeTypingSender) SendTyping(channelID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, channelID)
	return f.err
}

func (f *fakeTypingSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeGuildGetter) GetGuild(guildID snowflake.ID, withCounts bool, _ ...rest.RequestOpt) (*disgoDiscord.RestGuild, error) {
	f.calls++
	f.guildID = guildID
	f.withCounts = withCounts
	return f.guild, f.err
}

func (f *fakeCommandSyncer) SetGlobalCommands(applicationID snowflake.ID, commands []disgoDiscord.ApplicationCommandCreate, _ ...rest.RequestOpt) ([]disgoDiscord.ApplicationCommand, error) {
	f.globalApplicationIDs = append(f.globalApplicationIDs, applicationID)
	f.globalCommands = append(f.globalCommands, append([]disgoDiscord.ApplicationCommandCreate(nil), commands...))
	return nil, f.globalRegistrationErr
}

func (f *fakeCommandSyncer) SetGuildCommands(applicationID snowflake.ID, guildID snowflake.ID, commands []disgoDiscord.ApplicationCommandCreate, _ ...rest.RequestOpt) ([]disgoDiscord.ApplicationCommand, error) {
	f.guildCommands = append(f.guildCommands, syncedGuildCommands{
		applicationID: applicationID,
		guildID:       guildID,
		commands:      append([]disgoDiscord.ApplicationCommandCreate(nil), commands...),
	})
	return nil, f.guildRegistrationErr
}

func TestApplicationCommandsIncludeContextMenus(t *testing.T) {
	commands := applicationCommands()
	names := map[string]bool{}
	for _, command := range commands {
		names[command.CommandName()] = true
	}
	for _, name := range []string{"Explain with Panda", "Summarize with Panda", "help", "ping", "poll", "billing", "support", "data", "reminder"} {
		if !names[name] {
			t.Fatalf("expected command %q to be registered", name)
		}
	}
	for _, name := range []string{"ask", "chat", "summarize", "explain", "rewrite", "translate", "memory-consent", "search-memory", "mod", "tool"} {
		if names[name] {
			t.Fatalf("expected natural-language command %q not to be registered as a slash command", name)
		}
	}
}

func TestPollCommandIncludesNativePollOptions(t *testing.T) {
	var pollCommand *disgoDiscord.SlashCommandCreate
	for _, command := range applicationCommands() {
		slash, ok := command.(disgoDiscord.SlashCommandCreate)
		if ok && slash.Name == "poll" {
			pollCommand = &slash
			break
		}
	}
	if pollCommand == nil {
		t.Fatal("expected /poll command")
	}
	optionNames := map[string]bool{}
	for _, option := range pollCommand.Options {
		switch typed := option.(type) {
		case disgoDiscord.ApplicationCommandOptionString:
			optionNames[typed.Name] = true
			if (typed.Name == "question" || typed.Name == "answers") && !typed.Required {
				t.Fatalf("%s should be required", typed.Name)
			}
		case disgoDiscord.ApplicationCommandOptionInt:
			optionNames[typed.Name] = true
			if typed.Name == "duration_hours" && (typed.MinValue == nil || *typed.MinValue != 1 || typed.MaxValue == nil || *typed.MaxValue != polls.MaxDurationHours) {
				t.Fatalf("unexpected duration limits: %+v", typed)
			}
		case disgoDiscord.ApplicationCommandOptionBool:
			optionNames[typed.Name] = true
		}
	}
	for _, name := range []string{"question", "answers", "duration_hours", "allow_multiselect"} {
		if !optionNames[name] {
			t.Fatalf("expected /poll option %q", name)
		}
	}
}

func TestGlobalCommandRegistrationSetsGlobalCommands(t *testing.T) {
	syncer := &fakeCommandSyncer{}
	applicationID := snowflake.MustParse("100000000000000010")
	commands := applicationCommands()

	if err := syncApplicationCommands(syncer, applicationID, "", commands); err != nil {
		t.Fatalf("syncApplicationCommands: %v", err)
	}

	if len(syncer.globalCommands) != 1 {
		t.Fatalf("expected one global sync, got %d", len(syncer.globalCommands))
	}
	if len(syncer.globalCommands[0]) != len(commands) {
		t.Fatalf("expected %d global commands, got %d", len(commands), len(syncer.globalCommands[0]))
	}
	if len(syncer.guildCommands) != 0 {
		t.Fatalf("expected no guild syncs, got %+v", syncer.guildCommands)
	}
	if len(syncer.globalApplicationIDs) != 1 || syncer.globalApplicationIDs[0] != applicationID {
		t.Fatalf("unexpected global application ids: %+v", syncer.globalApplicationIDs)
	}
}

func TestGuildCommandRegistrationClearsGlobalCommandsBeforeGuildSync(t *testing.T) {
	syncer := &fakeCommandSyncer{}
	applicationID := snowflake.MustParse("100000000000000010")
	guildID := snowflake.MustParse("100000000000000011")
	commands := applicationCommands()

	if err := syncApplicationCommands(syncer, applicationID, guildID.String(), commands); err != nil {
		t.Fatalf("syncApplicationCommands: %v", err)
	}

	if len(syncer.globalCommands) != 1 {
		t.Fatalf("expected one global clear, got %d", len(syncer.globalCommands))
	}
	if len(syncer.globalCommands[0]) != 0 {
		t.Fatalf("expected global commands to be cleared before guild sync, got %d commands", len(syncer.globalCommands[0]))
	}
	if len(syncer.guildCommands) != 1 {
		t.Fatalf("expected one guild sync, got %d", len(syncer.guildCommands))
	}
	guildSync := syncer.guildCommands[0]
	if guildSync.applicationID != applicationID || guildSync.guildID != guildID {
		t.Fatalf("unexpected guild sync target: %+v", guildSync)
	}
	if len(guildSync.commands) != len(commands) {
		t.Fatalf("expected %d guild commands, got %d", len(commands), len(guildSync.commands))
	}
}

func TestDevelopmentGuildCommandRegistrationAccessErrorIsRecoverable(t *testing.T) {
	bot := &Bot{cfg: config.Config{Environment: "development", DiscordGuildID: "100000000000000001"}}
	err := fmt.Errorf("register commands: %w", &rest.Error{
		Code:    rest.JSONErrorCodeMissingAccess,
		Message: "Missing Access",
	})

	if !bot.canContinueAfterCommandRegistrationError(err) {
		t.Fatal("expected development guild command access error to be recoverable")
	}
}

func TestProductionGuildCommandRegistrationAccessErrorIsFatal(t *testing.T) {
	bot := &Bot{cfg: config.Config{Environment: "production", DiscordGuildID: "100000000000000001"}}
	err := &rest.Error{Code: rest.JSONErrorCodeMissingAccess, Message: "Missing Access"}

	if bot.canContinueAfterCommandRegistrationError(err) {
		t.Fatal("expected production guild command access error to stay fatal")
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

func TestContainsPandaWordUsesStandaloneWakeWord(t *testing.T) {
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

func TestShouldHandleNaturalMessageUsesWakeWordMentionOrReply(t *testing.T) {
	if !shouldHandleNaturalMessage("what can you do panda", nil) {
		t.Fatal("expected trailing Panda mention to be handled")
	}
	if !shouldHandleNaturalMessage("can you list those by tool name", map[string]string{"reply_author_is_bot": "true"}) {
		t.Fatal("expected reply to Panda to be handled without wake word")
	}
	if !shouldHandleNaturalMessage("can you help", map[string]string{"bot_mentioned": "true"}) {
		t.Fatal("expected direct mention to be handled without wake word")
	}
	if shouldHandleNaturalMessage("ambient channel chatter", nil) {
		t.Fatal("expected ambient message without wake word, mention, or Panda reply to be ignored")
	}
	if shouldHandleNaturalMessage("   ", nil) {
		t.Fatal("expected empty message to be ignored")
	}
}

func TestTypingIndicatorSendsImmediatelyAndRefreshes(t *testing.T) {
	sender := &fakeTypingSender{}
	channelID := snowflake.MustParse("100000000000000002")

	stop := startTypingIndicator(context.Background(), sender, nil, channelID, "message-1", 5*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(100 * time.Millisecond)
	for sender.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sender.count() < 2 {
		t.Fatalf("expected immediate and refreshed typing calls, got %d", sender.count())
	}
}

func TestTypingIndicatorNoopsWithoutSender(t *testing.T) {
	stop := startTypingIndicator(context.Background(), nil, nil, snowflake.MustParse("100000000000000002"), "message-1", time.Millisecond)
	stop()
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

func TestGuildMemberJoinEnqueuesComposedEvent(t *testing.T) {
	queue := &fakeInteractionJobQueue{}
	recorder := &fakeDiscordEventRecorder{}
	bot := (&Bot{}).WithDiscordEventRecorder(recorder).WithJobQueue(queue)
	guildID := snowflake.MustParse("100000000000000001")
	userID := snowflake.MustParse("100000000000000002")

	bot.onGuildMemberJoin(&events.GuildMemberJoin{
		GenericGuildMember: &events.GenericGuildMember{
			GuildID: guildID,
			Member: disgoDiscord.Member{
				User: disgoDiscord.User{ID: userID, Username: "snow"},
			},
		},
	})

	if len(recorder.records) != 1 || recorder.records[0].EventType != composed.EventGuildMemberJoined || recorder.records[0].UserID != userID.String() {
		t.Fatalf("expected recorded member join event, got %+v", recorder.records)
	}
	if len(queue.jobs) != 1 || queue.jobs[0].Kind != composed.EventJobKind || queue.jobs[0].GuildID != guildID.String() {
		t.Fatalf("expected composed event job, got %+v", queue.jobs)
	}
	var payload composed.EventJobPayload
	if err := json.Unmarshal([]byte(queue.jobs[0].Payload), &payload); err != nil {
		t.Fatalf("decode composed event payload: %v", err)
	}
	if payload.EventType != composed.EventGuildMemberJoined || payload.UserID != userID.String() || payload.Metadata["username"] != "snow" {
		t.Fatalf("unexpected composed event payload: %+v", payload)
	}
}

func TestSupportedDiscordEventEnqueuesComposedEvent(t *testing.T) {
	queue := &fakeInteractionJobQueue{}
	recorder := &fakeDiscordEventRecorder{}
	bot := (&Bot{}).WithDiscordEventRecorder(recorder).WithJobQueue(queue)
	guildID := snowflake.MustParse("100000000000000001")
	channelID := snowflake.MustParse("100000000000000002")

	bot.recordChannelEvent(composed.EventChannelCreated, guildID, channelID, "announcements")

	if len(recorder.records) != 1 || recorder.records[0].EventType != composed.EventChannelCreated {
		t.Fatalf("expected recorded channel event, got %+v", recorder.records)
	}
	if len(queue.jobs) != 1 || queue.jobs[0].Kind != composed.EventJobKind {
		t.Fatalf("expected composed event job, got %+v", queue.jobs)
	}
	var payload composed.EventJobPayload
	if err := json.Unmarshal([]byte(queue.jobs[0].Payload), &payload); err != nil {
		t.Fatalf("decode composed event payload: %v", err)
	}
	if payload.EventType != composed.EventChannelCreated || payload.ChannelID != channelID.String() || payload.Metadata["name"] != "announcements" {
		t.Fatalf("unexpected composed event payload: %+v", payload)
	}
}

func TestUnsupportedDiscordEventDoesNotEnqueueComposedEvent(t *testing.T) {
	queue := &fakeInteractionJobQueue{}
	recorder := &fakeDiscordEventRecorder{}
	bot := (&Bot{}).WithDiscordEventRecorder(recorder).WithJobQueue(queue)

	bot.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   "guild-1",
		EventType: "message_create",
		Summary:   "Message activity",
	})

	if len(recorder.records) != 1 {
		t.Fatalf("expected unsupported event to remain recorded, got %+v", recorder.records)
	}
	if len(queue.jobs) != 0 {
		t.Fatalf("unsupported event should not enqueue composed job, got %+v", queue.jobs)
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

func TestAdminBehaviorCommandIncludesRuntimeOptions(t *testing.T) {
	var behaviorCommand *disgoDiscord.ApplicationCommandOptionSubCommand
	foundModelCommand := false
	for _, command := range applicationCommands() {
		slash, ok := command.(disgoDiscord.SlashCommandCreate)
		if !ok || slash.Name != "admin" {
			continue
		}
		for _, option := range slash.Options {
			subcommand, ok := option.(disgoDiscord.ApplicationCommandOptionSubCommand)
			if !ok {
				continue
			}
			if subcommand.Name == "behavior" {
				behaviorCommand = &subcommand
				break
			}
			if subcommand.Name == "model" {
				foundModelCommand = true
			}
		}
	}
	if foundModelCommand {
		t.Fatal("did not expect legacy /admin model subcommand")
	}
	if behaviorCommand == nil {
		t.Fatal("expected /admin behavior subcommand")
	}

	optionNames := map[string]bool{}
	for _, option := range behaviorCommand.Options {
		switch typed := option.(type) {
		case disgoDiscord.ApplicationCommandOptionString:
			optionNames[typed.Name] = true
		}
	}
	for _, name := range []string{"answer_length", "tool_policy"} {
		if !optionNames[name] {
			t.Fatalf("expected /admin behavior option %q", name)
		}
	}
	for _, legacy := range []string{"model", "classifier_model", "fallback_models"} {
		if optionNames[legacy] {
			t.Fatalf("did not expect legacy model option %q", legacy)
		}
	}
}

func TestBillingCommandIncludesActivationOptions(t *testing.T) {
	billingCommand := slashCommand(t, "billing")
	action := slashStringOption(t, billingCommand, "action")
	if action.Required {
		t.Fatal("billing action should be optional so /billing shows status")
	}
	for _, expected := range []string{"status", "activate", "revoke"} {
		if !stringOptionHasChoice(action, expected) {
			t.Fatalf("expected billing action choice %q", expected)
		}
	}
	if len(action.Choices) != 3 {
		t.Fatalf("expected billing action to expose exactly three choices, got %+v", action.Choices)
	}
	for _, legacy := range []string{"plan", "discount_lamports", "coupon_code", "coupon", "expires_at", "note"} {
		if slashHasStringOption(billingCommand, legacy) {
			t.Fatalf("did not expect billing command to include coupon option %q", legacy)
		}
	}
	if slashHasStringOption(billingCommand, "pack") {
		t.Fatal("did not expect billing command to include pack option")
	}
	if slashHasStringOption(billingCommand, "email") {
		t.Fatal("did not expect billing command to include purchase email option")
	}
	if !slashHasStringOption(billingCommand, "api_key") {
		t.Fatal("expected billing command to include activation api key option")
	}
	if !slashHasStringOption(billingCommand, "order_id") {
		t.Fatal("expected billing command to include operator order id option")
	}
}

func TestAdminRoleCommandIncludesProfileControls(t *testing.T) {
	roleCommand := adminSubcommand(t, adminSlashCommand(t), "role")
	action := subcommandStringOption(t, roleCommand, "action")
	if !action.Required {
		t.Fatal("role action should be required")
	}
	if !subcommandHasStringOption(roleCommand, "profile") {
		t.Fatal("expected /admin role to include profile option")
	}
	option, ok := findSubcommandRoleOption(roleCommand, "role")
	if !ok {
		t.Fatal("expected /admin role to include role picker")
	}
	if option.Required {
		t.Fatal("role should be optional so action=list can omit it")
	}
}

func TestAdminMemberRoleCommandIncludesUserAndRolePickers(t *testing.T) {
	memberRole := adminSubcommand(t, adminSlashCommand(t), "member-role")
	action := subcommandStringOption(t, memberRole, "action")
	if !action.Required {
		t.Fatal("member-role action should be required")
	}
	user, ok := findSubcommandUserOption(memberRole, "user")
	if !ok {
		t.Fatal("expected /admin member-role to include user picker")
	}
	if !user.Required {
		t.Fatal("member-role user should be required")
	}
	role, ok := findSubcommandRoleOption(memberRole, "role")
	if !ok {
		t.Fatal("expected /admin member-role to include role picker")
	}
	if !role.Required {
		t.Fatal("member-role role should be required")
	}
}

func TestAdminToolCommandIncludesAccessOptions(t *testing.T) {
	tool := adminSubcommand(t, adminSlashCommand(t), "tool")
	action := subcommandStringOption(t, tool, "action")
	if !action.Required {
		t.Fatal("action should be required")
	}
	if !subcommandHasStringOption(tool, "tool_name") {
		t.Fatal("expected /admin tool to include tool_name")
	}
	role, ok := findSubcommandRoleOption(tool, "role")
	if !ok {
		t.Fatal("expected /admin tool to include role picker")
	}
	if role.Required {
		t.Fatal("role should be optional so action=list can omit it")
	}
}

func TestAdminChannelCommandIncludesAccessOptions(t *testing.T) {
	channel := adminSubcommand(t, adminSlashCommand(t), "channel")
	action := subcommandStringOption(t, channel, "action")
	if !action.Required {
		t.Fatal("channel action should be required")
	}
	option, ok := findSubcommandChannelOption(channel, "channel")
	if !ok {
		t.Fatal("expected /admin channel to include channel picker")
	}
	if option.Required {
		t.Fatal("channel should be optional so action=list can omit it")
	}
	if len(option.ChannelTypes) == 0 {
		t.Fatal("channel picker should be limited to guild message channels")
	}
	if !subcommandHasBoolOption(channel, "dry_run") {
		t.Fatal("expected /admin channel to include dry_run option")
	}
}

func TestGuildOwnerCountsAsGuildAdmin(t *testing.T) {
	ownerID := snowflake.MustParse("100000000000000001")
	guildID := snowflake.MustParse("200000000000000001")
	guild := disgoDiscord.Guild{OwnerID: ownerID}
	if !userOwnsGuild(ownerID, guild, true) {
		t.Fatal("expected guild owner to count as guild admin")
	}
	if userOwnsGuild(snowflake.MustParse("100000000000000002"), guild, true) {
		t.Fatal("expected non-owner to not count as guild admin")
	}
	if userOwnsGuild(ownerID, guild, false) {
		t.Fatal("expected uncached guild to not count as owned")
	}

	getter := &fakeGuildGetter{guild: &disgoDiscord.RestGuild{Guild: guild}}
	if !userOwnsGuildFromREST(getter, guildID, ownerID) {
		t.Fatal("expected REST guild owner lookup to count as guild admin")
	}
	if getter.calls != 1 || getter.guildID != guildID || getter.withCounts {
		t.Fatalf("unexpected REST lookup metadata: calls=%d guildID=%s withCounts=%t", getter.calls, getter.guildID, getter.withCounts)
	}
	if userOwnsGuildFromREST(getter, guildID, snowflake.MustParse("100000000000000002")) {
		t.Fatal("expected REST lookup to reject non-owner")
	}
}

func TestMemberHasAdministratorRole(t *testing.T) {
	guildID := snowflake.MustParse("200000000000000001")
	adminRoleID := snowflake.MustParse("300000000000000001")
	regularRoleID := snowflake.MustParse("300000000000000002")
	roles := map[snowflake.ID]disgoDiscord.Role{
		guildID:       {ID: guildID, GuildID: guildID, Permissions: disgoDiscord.PermissionsNone},
		adminRoleID:   {ID: adminRoleID, GuildID: guildID, Permissions: disgoDiscord.PermissionAdministrator},
		regularRoleID: {ID: regularRoleID, GuildID: guildID, Permissions: disgoDiscord.PermissionsNone},
	}
	lookup := func(guildID, roleID snowflake.ID) (disgoDiscord.Role, bool) {
		role, ok := roles[roleID]
		return role, ok && role.GuildID == guildID
	}

	if !memberHasAdministratorRole(&disgoDiscord.Member{RoleIDs: []snowflake.ID{regularRoleID, adminRoleID}}, guildID, lookup) {
		t.Fatal("expected administrator role to count as guild admin for message events")
	}
	if memberHasAdministratorRole(&disgoDiscord.Member{RoleIDs: []snowflake.ID{regularRoleID}}, guildID, lookup) {
		t.Fatal("expected regular role to not count as guild admin")
	}

	roles[guildID] = disgoDiscord.Role{ID: guildID, GuildID: guildID, Permissions: disgoDiscord.PermissionAdministrator}
	if !memberHasAdministratorRole(&disgoDiscord.Member{RoleIDs: nil}, guildID, lookup) {
		t.Fatal("expected administrator @everyone permissions to count as guild admin")
	}
}

func TestMessageEventGuildIDFallsBackToMessageGuildID(t *testing.T) {
	wrapperGuildID := snowflake.MustParse("200000000000000001")
	messageGuildID := snowflake.MustParse("200000000000000002")

	if got := messageEventGuildID(&events.MessageCreate{GenericMessage: &events.GenericMessage{
		GuildID: &wrapperGuildID,
		Message: disgoDiscord.Message{GuildID: &messageGuildID},
	}}); got == nil || *got != wrapperGuildID {
		t.Fatalf("expected wrapper guild id, got %v", got)
	}

	if got := messageEventGuildID(&events.MessageCreate{GenericMessage: &events.GenericMessage{
		Message: disgoDiscord.Message{GuildID: &messageGuildID},
	}}); got == nil || *got != messageGuildID {
		t.Fatalf("expected message guild id fallback, got %v", got)
	}
}

func TestAdminToggleCommandsIncludeSafetyOptions(t *testing.T) {
	adminCommand := adminSlashCommand(t)
	disable := adminSubcommand(t, adminCommand, "disable")
	if !subcommandHasStringOption(disable, "confirm") {
		t.Fatal("expected /admin disable to include confirm option")
	}
	for _, subcommandName := range []string{"behavior", "prompt", "soul", "enable", "disable"} {
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

func TestSplitDiscordContentKeepsChunksWithinLimit(t *testing.T) {
	content := strings.Repeat("- `discord_fetch_message`\n", 120)
	chunks := splitDiscordContent(content)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > discordContentLimit {
			t.Fatalf("chunk exceeds Discord limit: %d\n%s", utf8.RuneCountInString(chunk), chunk)
		}
	}
	joined := strings.Join(chunks, "\n")
	if !strings.Contains(joined, "discord_fetch_message") {
		t.Fatalf("split lost content: %q", joined)
	}
}

func TestSplitDiscordContentSplitsLongUnbrokenText(t *testing.T) {
	content := strings.Repeat("x", discordContentLimit+25)
	chunks := splitDiscordContent(content)
	if len(chunks) != 2 {
		t.Fatalf("expected two chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > discordContentLimit {
			t.Fatalf("chunk exceeds Discord limit: %d", utf8.RuneCountInString(chunk))
		}
	}
}

func TestResponsePartOnlyRendersComponentsWhenRequested(t *testing.T) {
	response := commands.Response{
		Content: "Confirm this.",
		Confirmation: &commands.Confirmation{
			ID:           "p2c:md:admin:1",
			ConfirmLabel: "Approve",
			CancelID:     commands.ConfirmationCancelID,
			CancelLabel:  "Cancel",
		},
	}

	withoutComponents := channelMessageCreateFromResponsePart(response, "part one", false)
	if len(withoutComponents.Components) != 0 {
		t.Fatalf("expected no components on non-final chunk, got %+v", withoutComponents.Components)
	}
	withComponents := channelMessageCreateFromResponsePart(response, "part two", true)
	if len(withComponents.Components) != 1 {
		t.Fatalf("expected components on final chunk, got %+v", withComponents.Components)
	}
}

func TestResponseRendersNativePoll(t *testing.T) {
	poll, err := polls.New("Pick one", []polls.Answer{{Text: "Red"}, {Text: "Blue", Emoji: "123456789012345678"}}, 6, true)
	if err != nil {
		t.Fatalf("poll setup: %v", err)
	}
	message := messageCreateFromResponse(commands.Response{Poll: &poll})
	if message.Poll == nil {
		t.Fatalf("expected poll payload, got %+v", message)
	}
	if len(message.Embeds) != 0 || message.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("native poll response should not use Panda embeds or suppress flags: embeds=%+v flags=%v", message.Embeds, message.Flags)
	}
	if message.Poll.Duration != 6 || !message.Poll.AllowMultiselect {
		t.Fatalf("unexpected poll settings: %+v", message.Poll)
	}
	if message.Poll.Question.Text == nil || *message.Poll.Question.Text != "Pick one" {
		t.Fatalf("unexpected poll question: %+v", message.Poll.Question)
	}
	if len(message.Poll.Answers) != 2 || message.Poll.Answers[1].Emoji == nil || message.Poll.Answers[1].Emoji.ID == nil {
		t.Fatalf("unexpected poll answers: %+v", message.Poll.Answers)
	}
}

func TestPollOnlyResponseIsDispatchable(t *testing.T) {
	poll, err := polls.New("Pick one", []polls.Answer{{Text: "Red"}, {Text: "Blue"}}, 1, false)
	if err != nil {
		t.Fatalf("poll setup: %v", err)
	}
	if !hasChannelResponsePayload(commands.Response{Poll: &poll}) {
		t.Fatal("poll-only natural response should be dispatched")
	}
	if hasChannelResponsePayload(commands.Response{}) {
		t.Fatal("empty response should not be dispatched")
	}
}

func TestResponseMessageCreatesPandaEmbed(t *testing.T) {
	response := commands.Response{
		Content: "### Panda Help\n\nUse `Panda play <song>` or ask a question.",
		Presentation: commands.Presentation{
			Title:  "Panda Help",
			Accent: commands.AccentInfo,
			Footer: "Hosted Discord assistant",
		},
		Actions: []commands.Action{
			{Label: "Commands", URL: "https://example.com/commands"},
			{Label: "Ignored", URL: "javascript:alert(1)"},
		},
	}

	interactionMessage := messageCreateFromResponsePart(response, response.Content, true)
	if interactionMessage.Content != "" || interactionMessage.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("expected interaction message to use an embed without suppressed flags, got content=%q flags=%v", interactionMessage.Content, interactionMessage.Flags)
	}
	if len(interactionMessage.Embeds) != 1 {
		t.Fatalf("expected one embed, got %+v", interactionMessage.Embeds)
	}
	embed := interactionMessage.Embeds[0]
	if embed.Title != "Panda Help" || embed.Description != "Use `Panda play <song>` or ask a question." || embed.Color != infoEmbedColor {
		t.Fatalf("unexpected embed: %+v", embed)
	}
	if embed.Footer == nil || embed.Footer.Text != "Hosted Discord assistant" {
		t.Fatalf("expected footer, got %+v", embed.Footer)
	}
	if len(interactionMessage.Components) != 1 {
		t.Fatalf("expected link action row, got %+v", interactionMessage.Components)
	}
	row, ok := interactionMessage.Components[0].(disgoDiscord.ActionRowComponent)
	if !ok || len(row.Components) != 1 {
		t.Fatalf("expected one valid link button, got %+v", interactionMessage.Components)
	}
	button, ok := row.Components[0].(disgoDiscord.ButtonComponent)
	if !ok || button.Style != disgoDiscord.ButtonStyleLink || button.URL != "https://example.com/commands" {
		t.Fatalf("unexpected link button: %+v", row.Components[0])
	}
}

func TestResponseMessageUpdatesPandaEmbed(t *testing.T) {
	response := commands.Response{Content: "Source: https://example.com/article", Presentation: commands.Presentation{Title: "Reference"}}

	webhookUpdate := webhookMessageUpdateFromResponsePart(response, response.Content, true)
	if webhookUpdate.Content == nil || *webhookUpdate.Content != "" {
		t.Fatalf("expected webhook update to clear top-level content, got %v", webhookUpdate.Content)
	}
	if webhookUpdate.Flags == nil || webhookUpdate.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("expected webhook update to keep custom embeds visible, got flags %v", webhookUpdate.Flags)
	}
	if webhookUpdate.Embeds == nil || len(*webhookUpdate.Embeds) != 1 || (*webhookUpdate.Embeds)[0].Description != response.Content {
		t.Fatalf("expected webhook update embed, got %+v", webhookUpdate.Embeds)
	}

	messageUpdate := messageUpdateFromResponse(response)
	if messageUpdate.Embeds == nil || len(*messageUpdate.Embeds) != 1 {
		t.Fatalf("expected message update embed, got %+v", messageUpdate.Embeds)
	}
}

func TestPlainResponseDoesNotInferPresentation(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "admin success",
			content: "Assigned `Pickle` (`role-pickle`) to `Snow` (`user-target`).",
		},
		{
			name:    "permission warning",
			content: "You do not have permission to use Panda here.",
		},
		{
			name:    "ops info",
			content: "Health: sqlite=ok discord=ok shards=ok ai_service=ok queued_jobs=0 guild_configs=1 draining=false incident=false data_dir=`data`.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := messageCreateFromResponse(commands.Response{Content: test.content})
			if message.Content != test.content || len(message.Embeds) != 0 {
				t.Fatalf("expected plain content without inferred embed, got %+v", message)
			}
			if !message.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
				t.Fatalf("expected plain content to suppress external embeds, got flags %v", message.Flags)
			}
		})
	}
}

func TestPlainWebhookUpdateClearsPreviousEmbeds(t *testing.T) {
	update := webhookMessageUpdateFromResponse(commands.Response{Content: "final answer"})

	if update.Content == nil || *update.Content != "final answer" {
		t.Fatalf("expected plain update content, got %+v", update.Content)
	}
	if update.Embeds == nil || len(*update.Embeds) != 0 {
		t.Fatalf("expected update to clear existing embeds, got %+v", update.Embeds)
	}
	if update.Flags == nil || !update.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("expected plain update to suppress external embeds, got flags %v", update.Flags)
	}
}

func TestMusicPresentationCreatesStrategicEmbed(t *testing.T) {
	response := commands.Response{
		Content: "Playing **Digital Love** `5:01`.",
		Presentation: commands.Presentation{
			Title:  "Now playing",
			Accent: commands.AccentMusic,
			URL:    "https://example.com/track",
			Fields: []commands.Field{
				{Name: "Duration", Value: "5:01", Inline: true},
			},
		},
	}

	message := messageCreateFromResponse(response)
	if message.Content != "" || len(message.Embeds) != 1 || message.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("expected music card embed, got %+v", message)
	}
	embed := message.Embeds[0]
	if embed.Title != "Now playing" || embed.Color != musicEmbedColor || embed.Description != response.Content || embed.URL != "https://example.com/track" {
		t.Fatalf("unexpected music embed: %+v", embed)
	}
	if len(embed.Fields) != 1 || embed.Fields[0].Name != "Duration" || embed.Fields[0].Value != "5:01" {
		t.Fatalf("expected music field, got %+v", embed.Fields)
	}
}

func TestChannelMessageCreateWithReferenceRepliesWithoutPingingInvoker(t *testing.T) {
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000003")
	reference := &disgoDiscord.MessageReference{
		MessageID: &messageID,
		ChannelID: &channelID,
	}

	message := channelMessageCreateFromResponsePartWithReference(commands.Response{Content: "hello"}, "hello", true, reference)

	if message.MessageReference != reference {
		t.Fatalf("expected outbound message to include reply reference, got %+v", message.MessageReference)
	}
	if message.AllowedMentions == nil {
		t.Fatal("expected explicit allowed mentions for reply")
	}
	if message.AllowedMentions.RepliedUser {
		t.Fatal("expected reply not to ping the invoking user")
	}
	if len(message.AllowedMentions.Parse) != 3 {
		t.Fatalf("expected normal content mention parsing to be preserved, got %+v", message.AllowedMentions.Parse)
	}
}

func TestChannelMessageCreateWithoutReferenceStaysPlainChannelMessage(t *testing.T) {
	message := channelMessageCreateFromResponsePartWithReference(commands.Response{Content: "hello"}, "hello", true, nil)

	if message.MessageReference != nil {
		t.Fatalf("expected no reply reference for plain channel message, got %+v", message.MessageReference)
	}
	if message.AllowedMentions != nil {
		t.Fatalf("expected plain channel message to use REST default allowed mentions, got %+v", message.AllowedMentions)
	}
}

func TestMessageReferenceFromMessageTargetsIncomingMessage(t *testing.T) {
	guildID := snowflake.MustParse("100000000000000001")
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000003")

	reference := messageReferenceFromMessage(disgoDiscord.Message{
		ID:        messageID,
		ChannelID: channelID,
		GuildID:   &guildID,
	})

	if reference == nil || reference.MessageID == nil || *reference.MessageID != messageID {
		t.Fatalf("expected reply reference to target message %s, got %+v", messageID, reference)
	}
	if reference.ChannelID == nil || *reference.ChannelID != channelID {
		t.Fatalf("expected reply reference to target channel %s, got %+v", channelID, reference)
	}
	if reference.GuildID == nil || *reference.GuildID != guildID {
		t.Fatalf("expected reply reference to include guild %s, got %+v", guildID, reference)
	}
	if reference.FailIfNotExists {
		t.Fatal("expected missing original message not to make Panda's response fail")
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

func TestAdminSoulCommandCanOpenModal(t *testing.T) {
	soul := adminSubcommand(t, adminSlashCommand(t), "soul")
	option := subcommandStringOption(t, soul, "soul")
	if option.Required {
		t.Fatal("soul option should be optional so the modal flow can open")
	}
}

func TestThreadNoticeMentionsThread(t *testing.T) {
	got := threadNotice(commands.Response{ThreadID: "12345", ThreadName: "Panda chat"})
	if got != "Continued this chat in <#12345> (`Panda chat`)." {
		t.Fatalf("unexpected thread notice %q", got)
	}
}

func TestSafeThreadNameTruncatesUTF8Safely(t *testing.T) {
	name := safeThreadName(strings.Repeat("🐼", 23))
	if len(name) > 90 {
		t.Fatalf("thread name exceeds byte budget: %d", len(name))
	}
	if !utf8.ValidString(name) {
		t.Fatalf("thread name is not valid UTF-8: %q", name)
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

func slashHasStringOption(command disgoDiscord.SlashCommandCreate, name string) bool {
	_, ok := findSlashStringOption(command, name)
	return ok
}

func slashStringOption(t *testing.T, command disgoDiscord.SlashCommandCreate, name string) disgoDiscord.ApplicationCommandOptionString {
	t.Helper()
	option, ok := findSlashStringOption(command, name)
	if !ok {
		t.Fatalf("expected /%s option %q", command.Name, name)
	}
	return option
}

func findSlashStringOption(command disgoDiscord.SlashCommandCreate, name string) (disgoDiscord.ApplicationCommandOptionString, bool) {
	for _, option := range command.Options {
		stringOption, ok := option.(disgoDiscord.ApplicationCommandOptionString)
		if ok && stringOption.Name == name {
			return stringOption, true
		}
	}
	return disgoDiscord.ApplicationCommandOptionString{}, false
}

func stringOptionHasChoice(option disgoDiscord.ApplicationCommandOptionString, value string) bool {
	for _, choice := range option.Choices {
		if choice.Value == value {
			return true
		}
	}
	return false
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

func subcommandHasRoleOption(subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) bool {
	_, ok := findSubcommandRoleOption(subcommand, name)
	return ok
}

func findSubcommandRoleOption(subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) (disgoDiscord.ApplicationCommandOptionRole, bool) {
	for _, option := range subcommand.Options {
		roleOption, ok := option.(disgoDiscord.ApplicationCommandOptionRole)
		if ok && roleOption.Name == name {
			return roleOption, true
		}
	}
	return disgoDiscord.ApplicationCommandOptionRole{}, false
}

func findSubcommandUserOption(subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) (disgoDiscord.ApplicationCommandOptionUser, bool) {
	for _, option := range subcommand.Options {
		userOption, ok := option.(disgoDiscord.ApplicationCommandOptionUser)
		if ok && userOption.Name == name {
			return userOption, true
		}
	}
	return disgoDiscord.ApplicationCommandOptionUser{}, false
}

func findSubcommandChannelOption(subcommand disgoDiscord.ApplicationCommandOptionSubCommand, name string) (disgoDiscord.ApplicationCommandOptionChannel, bool) {
	for _, option := range subcommand.Options {
		channelOption, ok := option.(disgoDiscord.ApplicationCommandOptionChannel)
		if ok && channelOption.Name == name {
			return channelOption, true
		}
	}
	return disgoDiscord.ApplicationCommandOptionChannel{}, false
}
