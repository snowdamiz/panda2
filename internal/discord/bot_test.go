package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	disgoBot "github.com/disgoorg/disgo/bot"
	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/config"
	"github.com/sn0w/panda2/internal/generated"
	"github.com/sn0w/panda2/internal/pandainfo"
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

type fakeMemberGetter struct {
	guildID snowflake.ID
	userID  snowflake.ID
	calls   int
	member  *disgoDiscord.Member
	err     error
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

func (f *fakeMemberGetter) GetMember(guildID snowflake.ID, userID snowflake.ID, _ ...rest.RequestOpt) (*disgoDiscord.Member, error) {
	f.calls++
	f.guildID = guildID
	f.userID = userID
	return f.member, f.err
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

func TestStringSelectInteractionQueuesSelection(t *testing.T) {
	userID := "100000000000000101"
	selection := commands.PrepareSelectionForUser(userID, &commands.Selection{
		Options: []commands.SelectionOption{{
			Label:          "Selected track",
			Value:          "track_1",
			Command:        "chat",
			Prompt:         "Play this exact YouTube result: https://www.youtube.com/watch?v=track",
			VoiceChannelID: "100000000000000404",
		}},
	})
	if selection == nil {
		t.Fatal("expected prepared selection")
	}

	applicationID := snowflake.MustParse("100000000000000200")
	event := selectionComponentInteractionEvent(t, selection.ID, "track_1", applicationID, snowflake.MustParse("100000000000000300"), snowflake.MustParse("100000000000000301"), snowflake.MustParse(userID))
	var responseTypes []disgoDiscord.InteractionResponseType
	event.Respond = func(responseType disgoDiscord.InteractionResponseType, _ disgoDiscord.InteractionResponseData, _ ...rest.RequestOpt) error {
		responseTypes = append(responseTypes, responseType)
		return nil
	}

	queue := &fakeInteractionJobQueue{}
	var updates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPatch {
			t.Fatalf("expected queued selection update to patch original response, got %s %s", r.Method, r.URL.Path)
		}
		updates++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"100000000000000501","channel_id":"100000000000000301","author":{"id":"100000000000000200","username":"Panda","discriminator":"0001","bot":true},"timestamp":"2026-06-27T00:00:00Z","type":0,"content":""}`)
	}))
	defer server.Close()
	restClient := rest.NewClient("test-token", rest.WithHTTPClient(server.Client()), rest.WithRateLimiter(rest.NewNoopRateLimiter()), rest.WithURL(server.URL))
	bot := &Bot{
		cfg:  config.Config{OwnerUserIDs: map[string]struct{}{userID: {}}},
		jobs: queue,
		client: &disgoBot.Client{
			ApplicationID: applicationID,
			Rest:          rest.New(restClient),
		},
	}

	bot.onStringSelectInteraction(event)

	if len(responseTypes) != 1 || responseTypes[0] != disgoDiscord.InteractionResponseTypeDeferredUpdateMessage {
		t.Fatalf("expected one deferred update response, got %+v", responseTypes)
	}
	if updates != 1 {
		t.Fatalf("expected queued response update, got %d", updates)
	}
	if len(queue.jobs) != 1 || queue.jobs[0].Kind != InteractionJobKind || queue.jobs[0].GuildID != "100000000000000300" || queue.jobs[0].MaxAttempts != 3 {
		t.Fatalf("unexpected queued selection job: %+v", queue.jobs)
	}
	var payload interactionJobPayload
	if err := json.Unmarshal([]byte(queue.jobs[0].Payload), &payload); err != nil {
		t.Fatalf("decode queued selection payload: %v", err)
	}
	if payload.Kind != "request" || payload.Request == nil {
		t.Fatalf("expected queued request payload, got %+v", payload)
	}
	request := *payload.Request
	if request.Command != "chat" || request.Options["question"] != "Play this exact YouTube result: https://www.youtube.com/watch?v=track" || request.VoiceChannelID != "100000000000000404" {
		t.Fatalf("unexpected selected request: %+v", request)
	}
}

func TestButtonInteractionRejectsScopedCancelFromOtherUser(t *testing.T) {
	applicationID := snowflake.MustParse("100000000000000200")
	ownerID := snowflake.MustParse("100000000000000101")
	otherID := snowflake.MustParse("100000000000000102")
	event := buttonComponentInteractionEvent(t, commands.ConfirmationCancelID(ownerID.String()), applicationID, snowflake.MustParse("100000000000000300"), snowflake.MustParse("100000000000000301"), otherID)

	var responseTypes []disgoDiscord.InteractionResponseType
	var messages []disgoDiscord.MessageCreate
	event.Respond = func(responseType disgoDiscord.InteractionResponseType, data disgoDiscord.InteractionResponseData, _ ...rest.RequestOpt) error {
		responseTypes = append(responseTypes, responseType)
		if message, ok := data.(disgoDiscord.MessageCreate); ok {
			messages = append(messages, message)
		}
		return nil
	}

	bot := &Bot{cfg: config.Config{OwnerUserIDs: map[string]struct{}{otherID.String(): {}}}}
	bot.onButtonInteraction(event)

	if len(responseTypes) != 1 || responseTypes[0] != disgoDiscord.InteractionResponseTypeCreateMessage {
		t.Fatalf("expected one ephemeral create-message rejection, got %+v", responseTypes)
	}
	if len(messages) != 1 || !messages[0].Flags.Has(disgoDiscord.MessageFlagEphemeral) {
		t.Fatalf("expected unauthorized cancel rejection to be ephemeral, got %+v", messages)
	}
}

func selectionComponentInteractionEvent(t *testing.T, customID string, value string, applicationID snowflake.ID, guildID snowflake.ID, channelID snowflake.ID, userID snowflake.ID) *events.ComponentInteractionCreate {
	return componentInteractionEvent(t, int(disgoDiscord.ComponentTypeStringSelectMenu), customID, value, applicationID, guildID, channelID, userID)
}

func buttonComponentInteractionEvent(t *testing.T, customID string, applicationID snowflake.ID, guildID snowflake.ID, channelID snowflake.ID, userID snowflake.ID) *events.ComponentInteractionCreate {
	return componentInteractionEvent(t, int(disgoDiscord.ComponentTypeButton), customID, "", applicationID, guildID, channelID, userID)
}

func componentInteractionEvent(t *testing.T, componentType int, customID string, value string, applicationID snowflake.ID, guildID snowflake.ID, channelID snowflake.ID, userID snowflake.ID) *events.ComponentInteractionCreate {
	t.Helper()
	values := ""
	if strings.TrimSpace(value) != "" {
		values = fmt.Sprintf(`,"values":[%q]`, value)
	}
	raw := fmt.Sprintf(`{
		"id":"100000000000000500",
		"application_id":%q,
		"type":3,
		"token":"interaction-token",
		"version":1,
		"guild_id":%q,
		"channel":{"id":%q,"type":0,"name":"bot-test","permissions":"0"},
		"member":{"user":{"id":%q,"username":"tester","discriminator":"0001","avatar":null},"roles":[],"permissions":"0"},
		"data":{"component_type":%d,"custom_id":%q%s},
		"message":{
			"id":"100000000000000501",
			"channel_id":%q,
			"author":{"id":%q,"username":"Panda","discriminator":"0001","avatar":null,"bot":true},
			"content":"",
			"timestamp":"2026-06-27T00:00:00Z",
			"edited_timestamp":null,
			"tts":false,
			"mention_everyone":false,
			"mentions":[],
			"mention_roles":[],
			"attachments":[],
			"embeds":[],
			"components":[],
			"pinned":false,
			"type":0
		}
	}`, applicationID.String(), guildID.String(), channelID.String(), userID.String(), componentType, customID, values, channelID.String(), applicationID.String())
	interaction, err := disgoDiscord.UnmarshalInteraction([]byte(raw))
	if err != nil {
		t.Fatalf("UnmarshalInteraction: %v", err)
	}
	component, ok := interaction.(disgoDiscord.ComponentInteraction)
	if !ok {
		t.Fatalf("expected component interaction, got %T", interaction)
	}
	return &events.ComponentInteractionCreate{
		GenericEvent:         events.NewGenericEvent(&disgoBot.Client{}, 0, 0),
		ComponentInteraction: component,
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
	if !messageMentionsUser(disgoDiscord.Message{Content: "<@100000000000000001> hello"}, userID.String()) {
		t.Fatal("expected standard mention content fallback to match")
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

func TestSetupWizardStartIntentRequiresWakeWordAndSetupServerIntent(t *testing.T) {
	if !isSetupWizardStartIntent("panda setup wizard for this server", nil) {
		t.Fatal("expected explicit setup wizard request to start the wizard")
	}
	if !isSetupWizardStartIntent("set up this server", map[string]string{"bot_mentioned": "true"}) {
		t.Fatal("expected direct mention with setup server request to start the wizard")
	}
	if isSetupWizardStartIntent("server setup sounds useful", nil) {
		t.Fatal("expected setup wording without wake word, mention, or reply context to be ignored")
	}
	if isSetupWizardStartIntent("panda what setup templates exist?", nil) {
		t.Fatal("expected generic template question not to start the wizard")
	}
}

func TestTypingIndicatorSendsImmediatelyAndRefreshes(t *testing.T) {
	sender := &fakeTypingSender{}
	channelID := snowflake.MustParse("100000000000000002")

	stop := startTypingIndicator(context.Background(), sender, channelID, 5*time.Millisecond)
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
	stop := startTypingIndicator(context.Background(), nil, snowflake.MustParse("100000000000000002"), time.Millisecond)
	stop()
}

func TestNaturalMessageTypingStopCancelsRefreshes(t *testing.T) {
	sender := &fakeTypingSender{}
	channelID := snowflake.MustParse("100000000000000002")
	typing := newNaturalMessageTyping(context.Background(), sender, channelID, 5*time.Millisecond)

	typing.Start()
	if got := sender.count(); got != 1 {
		t.Fatalf("expected initial typing call, got %d", got)
	}

	typing.Stop()
	time.Sleep(25 * time.Millisecond)
	if got := sender.count(); got != 1 {
		t.Fatalf("expected typing refreshes to stop after progress handoff, got %d calls", got)
	}
	typing.Stop()
}

func TestNaturalMessageTypingStartIsIdempotent(t *testing.T) {
	sender := &fakeTypingSender{}
	typing := newNaturalMessageTyping(context.Background(), sender, snowflake.MustParse("100000000000000002"), time.Hour)

	typing.Start()
	typing.Start()
	defer typing.Stop()

	if got := sender.count(); got != 1 {
		t.Fatalf("expected duplicate typing start to be ignored, got %d calls", got)
	}
}

func TestNaturalMessageTypingStopBeforeStartPreventsTyping(t *testing.T) {
	sender := &fakeTypingSender{}
	typing := newNaturalMessageTyping(context.Background(), sender, snowflake.MustParse("100000000000000002"), time.Millisecond)

	typing.Stop()
	typing.Start()

	if got := sender.count(); got != 0 {
		t.Fatalf("expected stopped typing session not to start, got %d calls", got)
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

func TestImageReferencesFromMessageFiltersSupportedImages(t *testing.T) {
	pngType := "image/png"
	gifType := "image/gif"
	message := disgoDiscord.Message{
		Attachments: []disgoDiscord.Attachment{
			{ID: snowflake.MustParse("100000000000000010"), Filename: "reference.png", ContentType: &pngType, URL: "https://cdn.example.test/reference.png", Size: 1234},
			{ID: snowflake.MustParse("100000000000000011"), Filename: "sticker.webp", URL: "https://cdn.example.test/sticker.webp", Size: 2345},
			{ID: snowflake.MustParse("100000000000000012"), Filename: "animated.gif", ContentType: &gifType, URL: "https://cdn.example.test/animated.gif", Size: 3456},
			{ID: snowflake.MustParse("100000000000000013"), Filename: "missing-url.png", ContentType: &pngType, Size: 4567},
		},
	}

	references := imageReferencesFromMessage(message, "current")
	if len(references) != 3 {
		t.Fatalf("expected three supported image references, got %+v", references)
	}
	if references[0].ID != "current:100000000000000010" || references[0].MIMEType != "image/png" || references[0].URL != "https://cdn.example.test/reference.png" {
		t.Fatalf("unexpected png reference: %+v", references[0])
	}
	if references[1].ID != "current:100000000000000011" || references[1].MIMEType != "image/webp" {
		t.Fatalf("unexpected inferred webp reference: %+v", references[1])
	}
	if references[2].ID != "current:100000000000000012" || references[2].MIMEType != "image/gif" {
		t.Fatalf("unexpected gif reference: %+v", references[2])
	}
}

func TestImageReferencesFromMessageAcceptsDiscordImageDimensionsWithoutMimeOrExtension(t *testing.T) {
	width := 1024
	height := 768
	message := disgoDiscord.Message{
		Attachments: []disgoDiscord.Attachment{
			{
				ID:       snowflake.MustParse("100000000000000014"),
				Filename: "discord-upload",
				URL:      "https://cdn.discordapp.com/attachments/100/200/discord-upload",
				Width:    &width,
				Height:   &height,
				Size:     5678,
			},
		},
	}

	references := imageReferencesFromMessage(message, "reply")
	if len(references) != 1 {
		t.Fatalf("expected dimension-bearing Discord attachment to become an image reference, got %+v", references)
	}
	reference := references[0]
	if reference.ID != "reply:100000000000000014" || reference.MIMEType != "" || reference.URL != "https://cdn.discordapp.com/attachments/100/200/discord-upload" || reference.SizeBytes != 5678 {
		t.Fatalf("unexpected dimension-only image reference: %+v", reference)
	}
}

func TestImageReferencesFromMessageInfersImageFromProxyURLPath(t *testing.T) {
	message := disgoDiscord.Message{
		Attachments: []disgoDiscord.Attachment{
			{
				ID:       snowflake.MustParse("100000000000000015"),
				Filename: "discord-upload",
				ProxyURL: "https://media.discordapp.net/attachments/100/200/discord-upload.heic?ex=1",
				Size:     6789,
			},
		},
	}

	references := imageReferencesFromMessage(message, "reply")
	if len(references) != 1 {
		t.Fatalf("expected proxy URL image extension to become an image reference, got %+v", references)
	}
	reference := references[0]
	if reference.ID != "reply:100000000000000015" || reference.MIMEType != "image/heic" || reference.URL != "https://media.discordapp.net/attachments/100/200/discord-upload.heic?ex=1" {
		t.Fatalf("unexpected proxy URL image reference: %+v", reference)
	}
}

func TestImageReferencesFromMessageIncludesSnapshotProxyImages(t *testing.T) {
	jpegType := "image/jpeg"
	message := disgoDiscord.Message{
		MessageSnapshots: []disgoDiscord.MessageSnapshot{
			{
				Message: disgoDiscord.PartialMessage{
					Attachments: []disgoDiscord.Attachment{
						{
							ID:          snowflake.MustParse("100000000000000014"),
							Filename:    "quoted-image.jpg",
							ContentType: &jpegType,
							ProxyURL:    "https://media.discordapp.net/attachments/quoted-image.jpg",
							Size:        4567,
						},
					},
				},
			},
		},
	}

	references := imageReferencesFromMessage(message, "current")
	if len(references) != 1 {
		t.Fatalf("expected one snapshot image reference, got %+v", references)
	}
	reference := references[0]
	if reference.ID != "current_snapshot_1:100000000000000014" || reference.MIMEType != "image/jpeg" || reference.URL != "https://media.discordapp.net/attachments/quoted-image.jpg" || reference.SizeBytes != 4567 {
		t.Fatalf("unexpected snapshot reference: %+v", reference)
	}
}

func TestImageReferencesFromMessageIncludesEmbedMediaReferences(t *testing.T) {
	message := disgoDiscord.Message{
		Embeds: []disgoDiscord.Embed{
			{
				Type: disgoDiscord.EmbedTypeGifV,
				Video: &disgoDiscord.EmbedResource{
					URL:         "https://media.tenor.example/reaction.mp4",
					ContentType: "video/mp4",
					Width:       498,
					Height:      498,
				},
				Thumbnail: &disgoDiscord.EmbedResource{
					ProxyURL: "https://media.discordapp.net/external/reaction.jpg",
					Width:    498,
					Height:   498,
				},
			},
			{
				Type: disgoDiscord.EmbedTypeImage,
				Image: &disgoDiscord.EmbedResource{
					URL:   "https://cdn.example.test/photo.png?width=640",
					Width: 640,
				},
			},
			{
				Type: disgoDiscord.EmbedTypeLink,
				URL:  "https://example.test/post-without-direct-media",
			},
		},
	}

	references := imageReferencesFromMessage(message, "reply")
	if len(references) != 2 {
		t.Fatalf("expected two embed media references, got %+v", references)
	}
	if references[0].ID != "reply_embed_1" || references[0].MIMEType != "video/mp4" || references[0].URL != "https://media.tenor.example/reaction.mp4" {
		t.Fatalf("unexpected gifv video reference: %+v", references[0])
	}
	if strings.Contains(strings.ToLower(references[0].ID), "video") {
		t.Fatalf("embed reference ID should not expose media kind to the model: %+v", references[0])
	}
	if references[1].ID != "reply_embed_2" || references[1].MIMEType != "image/png" || references[1].URL != "https://cdn.example.test/photo.png?width=640" {
		t.Fatalf("unexpected image embed reference: %+v", references[1])
	}
}

func TestImageReferencesFromMessageIncludesSnapshotEmbedMediaReferences(t *testing.T) {
	message := disgoDiscord.Message{
		MessageSnapshots: []disgoDiscord.MessageSnapshot{
			{
				Message: disgoDiscord.PartialMessage{
					Embeds: []disgoDiscord.Embed{
						{
							Type: disgoDiscord.EmbedTypeRich,
							Thumbnail: &disgoDiscord.EmbedResource{
								ProxyURL: "https://media.discordapp.net/external/snapshot.webp",
								Width:    400,
								Height:   300,
							},
						},
					},
				},
			},
		},
	}

	references := imageReferencesFromMessage(message, "current")
	if len(references) != 1 {
		t.Fatalf("expected one snapshot embed reference, got %+v", references)
	}
	reference := references[0]
	if reference.ID != "current_snapshot_1_embed_1" || reference.MIMEType != "image/webp" || reference.URL != "https://media.discordapp.net/external/snapshot.webp" {
		t.Fatalf("unexpected snapshot embed reference: %+v", reference)
	}
}

func TestImageReferencesFromMessageIncludesStickerMediaReferences(t *testing.T) {
	stickerID := snowflake.MustParse("100000000000000099")
	message := disgoDiscord.Message{
		StickerItems: []disgoDiscord.MessageSticker{
			{ID: stickerID, Name: "dance", FormatType: disgoDiscord.StickerFormatTypeGIF},
			{ID: snowflake.MustParse("100000000000000100"), Name: "vector", FormatType: disgoDiscord.StickerFormatTypeLottie},
		},
	}

	references := imageReferencesFromMessage(message, "current")
	if len(references) != 1 {
		t.Fatalf("expected one sticker image reference, got %+v", references)
	}
	reference := references[0]
	if reference.ID != "current_sticker:"+stickerID.String() || reference.MIMEType != "image/gif" || reference.Filename != "dance.gif" {
		t.Fatalf("unexpected sticker reference metadata: %+v", reference)
	}
	if !strings.Contains(reference.URL, "/stickers/"+stickerID.String()+".gif") {
		t.Fatalf("expected sticker GIF CDN URL, got %q", reference.URL)
	}
}

func TestQueueInteractionRequestStoresPayload(t *testing.T) {
	queue := &fakeInteractionJobQueue{}
	bot := (&Bot{}).WithJobQueue(queue)
	applicationID := snowflake.MustParse("100000000000000010")

	response := bot.queueInteractionRequest(context.Background(), applicationID, "token-1", commands.Request{
		GuildID:   "guild-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Command:   "chat",
		Options:   map[string]string{"question": "make a clip"},
	})
	if !strings.Contains(response.Content, "job #1") {
		t.Fatalf("expected queued job response, got %+v", response)
	}
	if len(queue.jobs) != 1 || queue.jobs[0].Kind != InteractionJobKind || queue.jobs[0].GuildID != "guild-1" || queue.jobs[0].MaxAttempts != 3 {
		t.Fatalf("unexpected queued jobs: %+v", queue.jobs)
	}
	var payload interactionJobPayload
	if err := json.Unmarshal([]byte(queue.jobs[0].Payload), &payload); err != nil {
		t.Fatalf("decode job payload: %v", err)
	}
	if payload.ApplicationID != applicationID.String() || payload.Token != "token-1" || payload.Kind != "request" || payload.Request == nil || payload.Request.Command != "chat" || payload.Request.Options["question"] != "make a clip" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestQueueNaturalMessageStoresPayload(t *testing.T) {
	queue := &fakeInteractionJobQueue{}
	bot := (&Bot{}).WithJobQueue(queue)
	guildID := snowflake.MustParse("100000000000000001")
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000003")
	reference := &disgoDiscord.MessageReference{
		MessageID:       &messageID,
		ChannelID:       &channelID,
		GuildID:         &guildID,
		FailIfNotExists: false,
	}
	request := commands.Request{
		RequestID: "message-1",
		Options:   map[string]string{"message": "panda help"},
		GuildID:   guildID.String(),
		ChannelID: channelID.String(),
		UserID:    "user-1",
		ImageReferences: []generated.ImageReference{{
			ID:       "reply:100000000000000004",
			Filename: "cat.png",
			MIMEType: "image/png",
			URL:      "https://cdn.discordapp.com/attachments/cat.png",
		}},
	}

	if err := bot.queueNaturalMessage(context.Background(), channelID, reference, request); err != nil {
		t.Fatalf("queueNaturalMessage: %v", err)
	}
	if len(queue.jobs) != 1 || queue.jobs[0].Kind != NaturalMessageJobKind || queue.jobs[0].GuildID != guildID.String() || queue.jobs[0].MaxAttempts != 3 {
		t.Fatalf("unexpected queued jobs: %+v", queue.jobs)
	}
	var payload naturalMessageJobPayload
	if err := json.Unmarshal([]byte(queue.jobs[0].Payload), &payload); err != nil {
		t.Fatalf("decode natural message payload: %v", err)
	}
	if payload.ChannelID != channelID.String() || payload.Request.RequestID != "message-1" || payload.Request.Options["message"] != "panda help" {
		t.Fatalf("unexpected natural message payload: %+v", payload)
	}
	if payload.Reference == nil || payload.Reference.MessageID != messageID.String() || payload.Reference.ChannelID != channelID.String() || payload.Reference.GuildID != guildID.String() {
		t.Fatalf("unexpected natural message reference: %+v", payload.Reference)
	}
	if len(payload.Request.ImageReferences) != 1 || payload.Request.ImageReferences[0].ID != "reply:100000000000000004" {
		t.Fatalf("expected queued natural message to preserve image references, got %+v", payload.Request.ImageReferences)
	}
}

func TestAddReplyContextOptionsMarksCurrentUserSelfReply(t *testing.T) {
	userID := snowflake.MustParse("100000000000000003")
	messageID := snowflake.MustParse("100000000000000004")
	options := map[string]string{}
	bot := &Bot{}

	bot.addReplyContextOptions(context.Background(), options, disgoDiscord.Message{
		Author: disgoDiscord.User{ID: userID, Username: "sn0w"},
		ReferencedMessage: &disgoDiscord.Message{
			ID:      messageID,
			Content: "join bot-test vc and play fill my pockets by mgk, also tell me spacex stock price",
			Author:  disgoDiscord.User{ID: userID, Username: "sn0w"},
		},
	})

	if options["reply_message_id"] != messageID.String() || !strings.Contains(options["reply_text"], "fill my pockets") {
		t.Fatalf("expected reply context options, got %+v", options)
	}
	if options["reply_author_is_current_user"] != "true" {
		t.Fatalf("expected self-reply marker, got %+v", options)
	}
}

func TestAddReplyContextOptionsKeepsOtherUserReplyContext(t *testing.T) {
	currentUserID := snowflake.MustParse("100000000000000003")
	referencedUserID := snowflake.MustParse("100000000000000005")
	messageID := snowflake.MustParse("100000000000000004")
	options := map[string]string{}
	bot := &Bot{}

	bot.addReplyContextOptions(context.Background(), options, disgoDiscord.Message{
		Author: disgoDiscord.User{ID: currentUserID, Username: "xer0"},
		ReferencedMessage: &disgoDiscord.Message{
			ID:      messageID,
			Content: "join bot-test vc and play fill my pockets by mgk, also tell me spacex stock price",
			Author:  disgoDiscord.User{ID: referencedUserID, Username: "sn0w"},
		},
	})

	if options["reply_message_id"] != messageID.String() || !strings.Contains(options["reply_text"], "spacex stock price") {
		t.Fatalf("expected reply context options, got %+v", options)
	}
	if options["reply_author_is_current_user"] != "" {
		t.Fatalf("expected no self-reply marker for another user's message, got %+v", options)
	}
}

func TestAddReplyContextOptionsReturnsReplyImageReferences(t *testing.T) {
	currentUserID := snowflake.MustParse("100000000000000003")
	referencedUserID := snowflake.MustParse("100000000000000005")
	messageID := snowflake.MustParse("100000000000000004")
	attachmentID := snowflake.MustParse("100000000000000006")
	imageType := "image/png"
	options := map[string]string{}
	bot := &Bot{}

	references := bot.addReplyContextOptions(context.Background(), options, disgoDiscord.Message{
		Author: disgoDiscord.User{ID: currentUserID, Username: "sn0w"},
		ReferencedMessage: &disgoDiscord.Message{
			ID:      messageID,
			Content: "",
			Author:  disgoDiscord.User{ID: referencedUserID, Username: "xer0"},
			Attachments: []disgoDiscord.Attachment{
				{ID: attachmentID, Filename: "cat.png", ContentType: &imageType, URL: "https://cdn.discordapp.com/attachments/cat.png", Size: 1234},
			},
		},
	})

	if options["reply_message_id"] != messageID.String() {
		t.Fatalf("expected reply message id, got %+v", options)
	}
	if len(references) != 1 {
		t.Fatalf("expected one reply image reference, got %+v", references)
	}
	reference := references[0]
	if reference.ID != "reply:"+attachmentID.String() || reference.MIMEType != "image/png" || reference.URL != "https://cdn.discordapp.com/attachments/cat.png" {
		t.Fatalf("unexpected reply image reference: %+v", reference)
	}
}

func TestHydrateReplyContextOptionsFindsImagesMissingFromPartialReferencedMessage(t *testing.T) {
	currentUserID := snowflake.MustParse("100000000000000003")
	referencedUserID := snowflake.MustParse("100000000000000005")
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000004")
	attachmentID := snowflake.MustParse("100000000000000006")
	imageType := "image/png"
	options := map[string]string{}
	bot := &Bot{messages: fakeMessageFetcher{message: &disgoDiscord.Message{
		ID:        messageID,
		ChannelID: channelID,
		Content:   "",
		Author:    disgoDiscord.User{ID: referencedUserID, Username: "xer0"},
		Attachments: []disgoDiscord.Attachment{
			{ID: attachmentID, Filename: "cat.png", ContentType: &imageType, URL: "https://cdn.discordapp.com/attachments/cat.png", Size: 1234},
		},
	}}}
	reference := &disgoDiscord.MessageReference{
		MessageID: &messageID,
		ChannelID: &channelID,
	}
	current := disgoDiscord.Message{
		ChannelID:        channelID,
		Author:           disgoDiscord.User{ID: currentUserID, Username: "sn0w"},
		MessageReference: reference,
		ReferencedMessage: &disgoDiscord.Message{
			ID:      messageID,
			Content: "",
			Author:  disgoDiscord.User{ID: referencedUserID, Username: "xer0"},
		},
	}

	partialReferences := bot.addReplyContextOptions(context.Background(), options, current)
	if len(partialReferences) != 0 {
		t.Fatalf("partial gateway reference should not have image refs in this fixture, got %+v", partialReferences)
	}
	hydratedReferences := bot.hydrateReplyContextOptions(context.Background(), options, current)
	if options["reply_message_id"] != messageID.String() {
		t.Fatalf("expected reply message id, got %+v", options)
	}
	if len(hydratedReferences) != 1 {
		t.Fatalf("expected one hydrated reply image reference, got %+v", hydratedReferences)
	}
	hydrated := hydratedReferences[0]
	if hydrated.ID != "reply:"+attachmentID.String() || hydrated.MIMEType != "image/png" || hydrated.URL != "https://cdn.discordapp.com/attachments/cat.png" {
		t.Fatalf("unexpected hydrated reply image reference: %+v", hydrated)
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

func TestConfiguredBotOwnerCountsAsPandaGuildAdmin(t *testing.T) {
	ownerID := snowflake.MustParse("100000000000000001")
	regularID := snowflake.MustParse("100000000000000002")
	guildID := snowflake.MustParse("200000000000000001")
	bot := &Bot{cfg: config.Config{OwnerUserIDs: map[string]struct{}{ownerID.String(): {}}}}
	event := &events.MessageCreate{GenericMessage: &events.GenericMessage{
		Message: disgoDiscord.Message{GuildID: &guildID},
	}}

	if !bot.isBotOwner(ownerID) {
		t.Fatal("expected configured owner user id to count as bot owner")
	}
	if !bot.isMessageGuildAdmin(event, ownerID, event.Message.Member) {
		t.Fatal("expected configured bot owner to count as Panda guild admin")
	}
	if bot.isMessageGuildAdmin(event, regularID, event.Message.Member) {
		t.Fatal("expected unconfigured user to not count as Panda guild admin")
	}
}

func TestResolveMessageMemberFetchesMissingGuildMember(t *testing.T) {
	guildID := snowflake.MustParse("200000000000000001")
	userID := snowflake.MustParse("100000000000000001")
	roleID := snowflake.MustParse("300000000000000001")
	getter := &fakeMemberGetter{member: &disgoDiscord.Member{
		User:    disgoDiscord.User{ID: userID},
		RoleIDs: []snowflake.ID{roleID},
	}}
	event := &events.MessageCreate{GenericMessage: &events.GenericMessage{
		GuildID: &guildID,
		Message: disgoDiscord.Message{
			GuildID: &guildID,
			Author:  disgoDiscord.User{ID: userID},
			Member:  nil,
		},
	}}

	member := resolveMessageMember(context.Background(), event, getter)
	if member == nil {
		t.Fatal("expected missing message member to be fetched from REST")
	}
	if getter.calls != 1 || getter.guildID != guildID || getter.userID != userID {
		t.Fatalf("unexpected member lookup metadata: calls=%d guildID=%s userID=%s", getter.calls, getter.guildID, getter.userID)
	}
	if got := strings.Join(messageRoleIDs(member), ","); got != roleID.String() {
		t.Fatalf("expected fetched member roles to populate request role IDs, got %q", got)
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
	if !ok || cancel.CustomID != commands.ConfirmationCancelID("admin") || cancel.Style != disgoDiscord.ButtonStyleSecondary {
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
				CancelLabel:  "Cancel",
				Danger:       true,
			},
			{
				ID:           "p2t:ra:admin:100000000000000456:moderator",
				ConfirmLabel: "Set role profile",
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
	if !ok || cancel.CustomID != commands.ConfirmationCancelID("admin") || cancel.Style != disgoDiscord.ButtonStyleSecondary {
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

func TestGeneratedFileOnlyResponseIsDispatchableAndAttached(t *testing.T) {
	response := commands.Response{
		GeneratedFiles: []generated.File{{
			Filename: "panda-icon.png",
			MIMEType: "image/png",
			Data:     []byte("image-bytes"),
			AltText:  "Panda icon",
		}},
	}
	if !hasChannelResponsePayload(response) {
		t.Fatal("generated-file-only response should be dispatchable")
	}
	message := messageCreateFromResponse(response)
	if len(message.Files) != 1 {
		t.Fatalf("expected one attached file, got %+v", message.Files)
	}
	file := message.Files[0]
	if file.Name != "panda-icon.png" || file.Description != "Panda icon" {
		t.Fatalf("unexpected file metadata: %+v", file)
	}
	data, err := io.ReadAll(file.Reader)
	if err != nil {
		t.Fatalf("read attached file: %v", err)
	}
	if string(data) != "image-bytes" {
		t.Fatalf("unexpected file bytes: %q", string(data))
	}
}

func TestGeneratedFilesAttachOnlyToFirstResponseChunk(t *testing.T) {
	response := commands.Response{
		Content: strings.Repeat("x", discordContentLimit+10),
		GeneratedFiles: []generated.File{{
			Filename: "sprite.webp",
			MIMEType: "image/webp",
			Data:     []byte("image-bytes"),
		}},
	}
	chunks := splitDiscordContent(response.Content)
	if len(chunks) < 2 {
		t.Fatalf("expected chunked response, got %d chunks", len(chunks))
	}
	first := messageCreateFromResponsePartWithFiles(response, chunks[0], false, true)
	second := messageCreateFromResponsePartWithFiles(response, chunks[1], true, false)
	if len(first.Files) != 1 {
		t.Fatalf("expected generated file on first chunk, got %+v", first.Files)
	}
	if len(second.Files) != 0 {
		t.Fatalf("expected no generated files on later chunks, got %+v", second.Files)
	}
}

func TestGeneratedFileProtectionsSkipInvalidAttachments(t *testing.T) {
	response := commands.Response{
		GeneratedFiles: []generated.File{
			{Filename: "bad.gif", MIMEType: "image/gif", Data: []byte("gif")},
			{Filename: "huge.png", MIMEType: "image/png", Data: make([]byte, discordGeneratedFileLimit+1)},
			{Filename: "empty.png", MIMEType: "image/png"},
		},
	}
	if files := discordFilesFromResponse(response); len(files) != 0 {
		t.Fatalf("expected invalid generated files to be skipped, got %+v", files)
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

func TestResponseMessageCreatesMediaItemEmbeds(t *testing.T) {
	response := commands.Response{
		Content: "Clips ready.",
		Presentation: commands.Presentation{
			Title:  "YouTube clips ready",
			Accent: commands.AccentInfo,
		},
		MediaItems: []commands.MediaItem{
			{
				Title:        "1. Best Moment",
				Description:  "Strong standalone hook. - 20s",
				URL:          "https://cdn.example.test/clips/01-best-moment.mp4",
				ThumbnailURL: "https://cdn.example.test/clips/01-best-moment.jpg",
			},
			{
				Title:        "2. Second Moment",
				URL:          "https://cdn.example.test/clips/02-second-moment.mp4",
				ThumbnailURL: "https://cdn.example.test/clips/02-second-moment.jpg",
			},
		},
		Actions: []commands.Action{{Label: "1. Best Moment", URL: "https://cdn.example.test/clips/01-best-moment.mp4"}},
	}

	message := messageCreateFromResponsePart(response, response.Content, true)
	if message.Content != "" || len(message.Embeds) != 3 || message.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("expected main embed plus media embeds, got %+v", message)
	}
	if message.Embeds[1].Title != "1. Best Moment" || message.Embeds[1].URL != "https://cdn.example.test/clips/01-best-moment.mp4" || message.Embeds[1].Thumbnail == nil || message.Embeds[1].Thumbnail.URL != "https://cdn.example.test/clips/01-best-moment.jpg" {
		t.Fatalf("unexpected first media embed: %+v", message.Embeds[1])
	}
	if message.Embeds[2].Title != "2. Second Moment" || message.Embeds[2].Thumbnail == nil || message.Embeds[2].Thumbnail.URL != "https://cdn.example.test/clips/02-second-moment.jpg" {
		t.Fatalf("unexpected second media embed: %+v", message.Embeds[2])
	}
}

func TestPandaAboutResponseRendersGithubAndXButtons(t *testing.T) {
	response := commands.Response{
		Content: "I help Discord servers stay organized.\n\nCreated by @andrew_da_miz",
		Presentation: commands.Presentation{
			Title:  "I'm Panda, a Discord-native assistant.",
			Accent: commands.AccentInfo,
		},
		Actions: []commands.Action{
			{Label: "Github", URL: pandainfo.RepositoryURL},
			{Label: "X", URL: pandainfo.CreatorURL},
		},
	}

	message := messageCreateFromResponsePart(response, response.Content, true)
	if len(message.Components) != 1 {
		t.Fatalf("expected one action row, got %+v", message.Components)
	}
	row, ok := message.Components[0].(disgoDiscord.ActionRowComponent)
	if !ok || len(row.Components) != 2 {
		t.Fatalf("expected Github and X buttons, got %+v", message.Components)
	}
	github, ok := row.Components[0].(disgoDiscord.ButtonComponent)
	if !ok || github.Style != disgoDiscord.ButtonStyleLink || github.Label != "Github" || github.URL != pandainfo.RepositoryURL {
		t.Fatalf("unexpected Github button: %+v", row.Components[0])
	}
	x, ok := row.Components[1].(disgoDiscord.ButtonComponent)
	if !ok || x.Style != disgoDiscord.ButtonStyleLink || x.Label != "X" || x.URL != pandainfo.CreatorURL {
		t.Fatalf("unexpected X button: %+v", row.Components[1])
	}
}

func TestResponseMessageSplitsManyLinkActionsAcrossRows(t *testing.T) {
	actions := make([]commands.Action, 0, 13)
	for i := 1; i <= 13; i++ {
		actions = append(actions, commands.Action{
			Label: fmt.Sprintf("Clip %d", i),
			URL:   fmt.Sprintf("https://example.com/clips/%d", i),
		})
	}
	response := commands.Response{
		Content: "Clips ready.",
		Actions: actions,
	}

	message := messageCreateFromResponsePart(response, response.Content, true)
	if len(message.Components) != 3 {
		t.Fatalf("expected three action rows, got %+v", message.Components)
	}
	expectedCounts := []int{5, 5, 3}
	for index, component := range message.Components {
		row, ok := component.(disgoDiscord.ActionRowComponent)
		if !ok || len(row.Components) != expectedCounts[index] {
			t.Fatalf("expected row %d to contain %d buttons, got %+v", index+1, expectedCounts[index], component)
		}
	}
	lastRow := message.Components[2].(disgoDiscord.ActionRowComponent)
	lastButton, ok := lastRow.Components[2].(disgoDiscord.ButtonComponent)
	if !ok || lastButton.Style != disgoDiscord.ButtonStyleLink || lastButton.Label != "Clip 13" || lastButton.URL != "https://example.com/clips/13" {
		t.Fatalf("unexpected last link button: %+v", lastRow.Components[2])
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

func TestNaturalToolProgressResponseRendersClipProgressCard(t *testing.T) {
	response, ok := naturalToolProgressResponse("panda_clip_youtube")
	if !ok {
		t.Fatal("expected clip tool to produce a natural progress response")
	}
	message := messageCreateFromResponse(response)
	if message.Content != "" || len(message.Embeds) != 1 || message.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("expected clip progress embed, got %+v", message)
	}
	embed := message.Embeds[0]
	if embed.Title != "Clipping YouTube video" || embed.Color != infoEmbedColor || embed.Description != "" {
		t.Fatalf("unexpected clip progress embed: %+v", embed)
	}
	if len(embed.Fields) != 1 || embed.Fields[0].Name != "Status" || embed.Fields[0].Value != "Searching" {
		t.Fatalf("expected clip progress status field, got %+v", embed.Fields)
	}
	response, ok = naturalToolProgressResponse("panda_clip_youtube", "Transcribing")
	if !ok {
		t.Fatal("expected clip tool progress update response")
	}
	message = messageCreateFromResponse(response)
	if got := message.Embeds[0].Fields[0].Value; got != "Transcribing" {
		t.Fatalf("expected updated clip progress status, got %q", got)
	}
	if _, ok := naturalToolProgressResponse("web_search"); ok {
		t.Fatal("short/non-YouTube tools should not produce natural progress cards")
	}
}

func TestNaturalMessageProgressStartReportsCreated(t *testing.T) {
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000003")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/channels/"+channelID.String()+"/messages" {
			t.Fatalf("unexpected progress request %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read progress request body: %v", err)
		}
		if !strings.Contains(string(body), "Clipping YouTube video") {
			t.Fatalf("expected progress card title in request body, got %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"%s","channel_id":"%s","author":{"id":"100000000000000004","username":"Panda","discriminator":"0000"},"timestamp":"2026-06-28T15:00:00Z","type":0,"content":""}`, messageID, channelID)
	}))
	defer server.Close()

	restClient := rest.NewClient("test-token",
		rest.WithHTTPClient(server.Client()),
		rest.WithRateLimiter(rest.NewNoopRateLimiter()),
		rest.WithURL(server.URL),
	)
	progress := &naturalMessageProgress{
		bot:       &Bot{client: &disgoBot.Client{Rest: rest.New(restClient)}},
		channelID: channelID,
	}

	if !progress.Start(context.Background(), "panda_clip_youtube") {
		t.Fatal("expected progress start to report a created card")
	}
	if !progress.Created() {
		t.Fatal("expected progress to be marked created")
	}
	if progress.Start(context.Background(), "panda_clip_youtube") {
		t.Fatal("expected duplicate progress start to report false")
	}
	if requests != 1 {
		t.Fatalf("expected one progress request, got %d", requests)
	}
}

func TestNaturalMessageProgressUpdatesClipStatus(t *testing.T) {
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000003")
	var creates int
	var updates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read progress request body: %v", err)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/channels/"+channelID.String()+"/messages":
			creates++
			if !strings.Contains(string(body), "Searching") {
				t.Fatalf("expected initial progress status in request body, got %s", string(body))
			}
		case r.Method == http.MethodPatch && r.URL.Path == "/channels/"+channelID.String()+"/messages/"+messageID.String():
			updates++
			if !strings.Contains(string(body), "Rendering") {
				t.Fatalf("expected updated progress status in request body, got %s", string(body))
			}
		default:
			t.Fatalf("unexpected progress request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"%s","channel_id":"%s","author":{"id":"100000000000000004","username":"Panda","discriminator":"0000"},"timestamp":"2026-06-28T15:00:00Z","type":0,"content":""}`, messageID, channelID)
	}))
	defer server.Close()

	restClient := rest.NewClient("test-token",
		rest.WithHTTPClient(server.Client()),
		rest.WithRateLimiter(rest.NewNoopRateLimiter()),
		rest.WithURL(server.URL),
	)
	progress := &naturalMessageProgress{
		bot:       &Bot{client: &disgoBot.Client{Rest: rest.New(restClient)}},
		channelID: channelID,
	}

	if !progress.Start(context.Background(), "panda_clip_youtube") {
		t.Fatal("expected progress start to report a created card")
	}
	if !progress.UpdateToolStatus(context.Background(), "panda_clip_youtube", "Searching") {
		t.Fatal("expected duplicate status update to be accepted")
	}
	if !progress.UpdateToolStatus(context.Background(), "panda_clip_youtube", "Rendering") {
		t.Fatal("expected progress status update to report success")
	}
	if creates != 1 || updates != 1 {
		t.Fatalf("expected one create and one update request, got creates=%d updates=%d", creates, updates)
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

func TestSelectionResponseRendersSelectMenuAndCandidateEmbeds(t *testing.T) {
	response := commands.Response{
		Content: "Pick a result.",
		Presentation: commands.Presentation{
			Title:  "Choose a YouTube video",
			Accent: commands.AccentInfo,
		},
		Selection: &commands.Selection{
			ID:          "p2s:pick:user-1:abcdef",
			Placeholder: "Choose a video",
			Options: []commands.SelectionOption{
				{
					Label:        "First Result",
					Description:  "Creator - 2:04",
					Value:        "video_1",
					URL:          "https://www.youtube.com/watch?v=one",
					ThumbnailURL: "https://i.ytimg.com/vi/one/hqdefault.jpg",
				},
				{
					Label:       "Second Result",
					Description: "Other Creator",
					Value:       "video_2",
					URL:         "https://www.youtube.com/watch?v=two",
				},
			},
		},
	}

	message := messageCreateFromResponse(response)
	if message.Content != "" || len(message.Embeds) != 3 || message.Flags.Has(disgoDiscord.MessageFlagSuppressEmbeds) {
		t.Fatalf("expected main embed plus two candidate embeds, got %+v", message)
	}
	if message.Embeds[1].Title != "1. First Result" || message.Embeds[1].URL != "https://www.youtube.com/watch?v=one" || message.Embeds[1].Thumbnail == nil || message.Embeds[1].Thumbnail.URL == "" {
		t.Fatalf("unexpected first candidate embed: %+v", message.Embeds[1])
	}
	if len(message.Components) != 1 {
		t.Fatalf("expected one selection row, got %+v", message.Components)
	}
	row, ok := message.Components[0].(disgoDiscord.ActionRowComponent)
	if !ok || len(row.Components) != 1 {
		t.Fatalf("expected action row with select menu, got %+v", message.Components)
	}
	menu, ok := row.Components[0].(disgoDiscord.StringSelectMenuComponent)
	if !ok || menu.CustomID != response.Selection.ID || menu.Placeholder != "Choose a video" || len(menu.Options) != 2 {
		t.Fatalf("unexpected select menu: %+v", row.Components[0])
	}
	if menu.Options[0].Label != "First Result" || menu.Options[0].Value != "video_1" || menu.Options[0].Description != "Creator - 2:04" {
		t.Fatalf("unexpected first menu option: %+v", menu.Options[0])
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
