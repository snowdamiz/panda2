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
	calls                 []string
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
	f.calls = append(f.calls, "global")
	f.globalApplicationIDs = append(f.globalApplicationIDs, applicationID)
	f.globalCommands = append(f.globalCommands, append([]disgoDiscord.ApplicationCommandCreate(nil), commands...))
	return nil, f.globalRegistrationErr
}

func (f *fakeCommandSyncer) SetGuildCommands(applicationID snowflake.ID, guildID snowflake.ID, commands []disgoDiscord.ApplicationCommandCreate, _ ...rest.RequestOpt) ([]disgoDiscord.ApplicationCommand, error) {
	f.calls = append(f.calls, "guild")
	f.guildCommands = append(f.guildCommands, syncedGuildCommands{
		applicationID: applicationID,
		guildID:       guildID,
		commands:      append([]disgoDiscord.ApplicationCommandCreate(nil), commands...),
	})
	return nil, f.guildRegistrationErr
}

func TestApplicationCommandsExposeOnlyBillingSlashCommandAndContextMenus(t *testing.T) {
	commands := applicationCommands()
	slashNames := map[string]bool{}
	contextNames := map[string]bool{}
	for _, command := range commands {
		switch typed := command.(type) {
		case disgoDiscord.SlashCommandCreate:
			slashNames[typed.Name] = true
		case disgoDiscord.MessageCommandCreate:
			contextNames[typed.Name] = true
		default:
			t.Fatalf("unexpected command type %T", command)
		}
	}

	if len(slashNames) != 1 || !slashNames["billing"] {
		t.Fatalf("expected only /billing to be registered as a slash command, got %+v", slashNames)
	}
	for _, name := range []string{"Explain with Panda", "Summarize with Panda"} {
		if !contextNames[name] {
			t.Fatalf("expected message command %q to be registered", name)
		}
	}
	if len(contextNames) != 2 {
		t.Fatalf("expected exactly two message commands, got %+v", contextNames)
	}
	if !registeredSlashCommandName("billing") {
		t.Fatal("expected /billing to be accepted by the stale-command guard")
	}
	for _, removed := range []string{"admin", "poll", "reminder", "schedule", "ops", "help", "ping", "support", "data"} {
		if registeredSlashCommandName(removed) {
			t.Fatalf("expected removed slash command %q to be rejected by the stale-command guard", removed)
		}
	}
	response := removedSlashCommandResponse("admin")
	if !response.Ephemeral || !strings.Contains(response.Content, "natural Panda chat") || !strings.Contains(response.Content, "/billing") {
		t.Fatalf("unexpected removed command response: %+v", response)
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

func TestGuildCommandRegistrationClearsGlobalCommandsAfterGuildSync(t *testing.T) {
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
		t.Fatalf("expected global commands to be cleared after guild sync, got %d commands", len(syncer.globalCommands[0]))
	}
	if len(syncer.guildCommands) != 1 {
		t.Fatalf("expected one guild sync, got %d", len(syncer.guildCommands))
	}
	if got, want := strings.Join(syncer.calls, ","), "guild,global"; got != want {
		t.Fatalf("expected guild sync before global clear, got %q", got)
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

func TestProductionGuildCommandRegistrationAccessErrorIsRecoverable(t *testing.T) {
	bot := &Bot{cfg: config.Config{Environment: "production", DiscordGuildID: "100000000000000001"}}
	err := &rest.Error{Code: rest.JSONErrorCodeMissingAccess, Message: "Missing Access"}

	if !bot.canContinueAfterCommandRegistrationError(err) {
		t.Fatal("expected production guild command access error to be recoverable")
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

func TestNaturalMessageQueueRunsSameChannelFIFOAndOtherChannelsIndependently(t *testing.T) {
	queue := newNaturalMessageQueue()
	key := naturalMessageKey(snowflake.MustParse("100000000000000002"), commands.Request{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
	})
	otherKey := naturalMessageKey(snowflake.MustParse("100000000000000003"), commands.Request{
		GuildID:   "guild-1",
		ChannelID: "channel-2",
	})

	started := make(chan int, 3)
	done := make(chan int, 3)
	releaseFirst := make(chan struct{})

	queue.enqueue(key, func() {
		started <- 1
		<-releaseFirst
		done <- 1
	})

	if got := waitForInt(t, started); got != 1 {
		t.Fatalf("expected first same-channel task to start first, got %d", got)
	}

	queue.enqueue(key, func() {
		started <- 2
		done <- 2
	})
	queue.enqueue(otherKey, func() {
		started <- 3
		done <- 3
	})

	if got := waitForInt(t, started); got != 3 {
		t.Fatalf("expected other channel to run independently, got %d", got)
	}
	if got := waitForInt(t, done); got != 3 {
		t.Fatalf("expected other channel to finish while first channel is blocked, got %d", got)
	}

	select {
	case got := <-started:
		t.Fatalf("same-channel task started before first task finished: %d", got)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	if got := waitForInt(t, done); got != 1 {
		t.Fatalf("expected first same-channel task to finish before second starts, got %d", got)
	}
	if got := waitForInt(t, started); got != 2 {
		t.Fatalf("expected second same-channel task to start after first finished, got %d", got)
	}
	if got := waitForInt(t, done); got != 2 {
		t.Fatalf("expected second same-channel task to finish last, got %d", got)
	}
}

func waitForInt(t *testing.T, values <-chan int) int {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for queued task")
		return 0
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

func TestBillingCommandIncludesActivationOptions(t *testing.T) {
	billingCommand := slashCommand(t, "billing")
	action := slashStringOption(t, billingCommand, "action")
	if action.Required {
		t.Fatal("billing action should be optional so /billing shows status")
	}
	for _, expected := range []string{"status", "activate"} {
		if !stringOptionHasChoice(action, expected) {
			t.Fatalf("expected billing action choice %q", expected)
		}
	}
	if len(action.Choices) != 2 {
		t.Fatalf("expected billing action to expose exactly two choices, got %+v", action.Choices)
	}
	if stringOptionHasChoice(action, "revoke") {
		t.Fatal("did not expect billing command to expose operator revocation")
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
	if slashHasStringOption(billingCommand, "order_id") {
		t.Fatal("did not expect billing command to include operator order id option")
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

func TestConfirmationResponseRendersMultipleButtons(t *testing.T) {
	response := commands.Response{
		Content: "Confirm setup changes.",
		Confirmations: []commands.Confirmation{
			{
				ID:           "p2t:cs:admin:100000000000000123:allow",
				ConfirmLabel: "Set rule",
				CancelID:     commands.ConfirmationCancelID,
				CancelLabel:  "Cancel",
				Danger:       true,
			},
			{
				ID:           "p2t:ra:admin:100000000000000456:moderator",
				ConfirmLabel: "Set role profile",
				CancelID:     commands.ConfirmationCancelID,
				CancelLabel:  "Cancel",
				Danger:       true,
			},
		},
	}

	message := messageCreateFromResponse(response)
	if len(message.Components) != 1 {
		t.Fatalf("expected one action row, got %+v", message.Components)
	}
	row, ok := message.Components[0].(disgoDiscord.ActionRowComponent)
	if !ok || len(row.Components) != 3 {
		t.Fatalf("expected action row with two confirmations and cancel, got %+v", message.Components[0])
	}
	for index, want := range response.Confirmations {
		button, ok := row.Components[index].(disgoDiscord.ButtonComponent)
		if !ok || button.CustomID != want.ID || button.Style != disgoDiscord.ButtonStyleDanger {
			t.Fatalf("unexpected confirmation button %d: %+v", index, row.Components[index])
		}
	}
	cancel, ok := row.Components[2].(disgoDiscord.ButtonComponent)
	if !ok || cancel.CustomID != commands.ConfirmationCancelID || cancel.Style != disgoDiscord.ButtonStyleSecondary {
		t.Fatalf("unexpected cancel button: %+v", row.Components[2])
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
