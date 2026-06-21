package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/assistant"
	"github.com/sn0w/panda2/internal/composed"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/llm"
	"github.com/sn0w/panda2/internal/music"
	"github.com/sn0w/panda2/internal/ops"
	"github.com/sn0w/panda2/internal/polls"
	"github.com/sn0w/panda2/internal/ratelimit"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
	"github.com/sn0w/panda2/internal/textutil"
	toolsvc "github.com/sn0w/panda2/internal/tools"
)

type Router struct {
	admin       *admin.Service
	assistant   *assistant.Service
	composed    *composed.Service
	context     *contextsvc.Service
	threads     ThreadManager
	memberRoles MemberRoleManager
	attachments AttachmentReader
	ops         *ops.Service
	music       MusicService
	rateLimit   *ratelimit.Limiter
}

type ThreadManager interface {
	EnsureChatThread(ctx context.Context, request ThreadRequest) (Thread, error)
}

type MemberRoleManager interface {
	AddMemberRole(ctx context.Context, request MemberRoleRequest) error
	RemoveMemberRole(ctx context.Context, request MemberRoleRequest) error
}

type AttachmentReader interface {
	Get(ctx context.Context, guildID string, id uint) (store.Attachment, error)
}

type MusicService interface {
	Handle(ctx context.Context, request music.Request) (music.Response, error)
}

type helpAccess struct {
	config      bool
	soul        bool
	moderation  bool
	toolDraft   bool
	toolApprove bool
	toolInvoke  bool
	toolAudit   bool
}

func (access helpAccess) elevated() bool {
	return access.config || access.soul || access.moderation || access.composedTools()
}

func (access helpAccess) composedTools() bool {
	return access.toolDraft || access.toolApprove || access.toolInvoke || access.toolAudit
}

const baseHelpMessage = "### Panda Help\n\n" +
	"**Chat naturally**\n" +
	"- Mention `Panda` in a normal message: `Panda is this true?`\n" +
	"- Panda checks intent before replying, so casual mentions do not always trigger it.\n\n" +
	"**Music**\n" +
	"- Join a voice channel, then say `Panda play <song>`.\n" +
	"- Natural controls: `pause`, `resume`, `skip`, `stop`, `queue`, `clear queue`, `now playing`.\n\n" +
	"**Message actions**\n" +
	"- Use **Explain with Panda** or **Summarize with Panda** from a message's **Apps** menu.\n" +
	"- `/poll question:<text> answers:<answer | answer | answer>` creates a native Discord poll."

const regularHelpMessage = baseHelpMessage + "\n\n" +
	"**Good things to ask**\n" +
	"- Questions about the conversation\n" +
	"- Summaries, rewrites, and explanations\n" +
	"- Help thinking through an idea or decision"

const (
	pandaRepositoryURL = "https://github.com/snowdamiz/panda2"
	pandaSetupURL      = "https://github.com/snowdamiz/panda2#local-development"
	pandaCommandsURL   = "https://github.com/snowdamiz/panda2#commands"
)

const discordMessageContentLimit = 2000
const invocationContextWindow = 2 * time.Minute
const invocationContextLimit = 50

func NewRouter(adminService *admin.Service, assistantService *assistant.Service, opsService *ops.Service, limiter *ratelimit.Limiter) *Router {
	return &Router{admin: adminService, assistant: assistantService, ops: opsService, rateLimit: limiter}
}

func (r *Router) WithContextService(contextService *contextsvc.Service) *Router {
	r.context = contextService
	return r
}

func (r *Router) WithThreadManager(threadManager ThreadManager) *Router {
	r.threads = threadManager
	return r
}

func (r *Router) WithMemberRoleManager(memberRoles MemberRoleManager) *Router {
	r.memberRoles = memberRoles
	return r
}

func (r *Router) WithAttachmentReader(attachmentReader AttachmentReader) *Router {
	r.attachments = attachmentReader
	return r
}

func (r *Router) WithComposedService(composedService *composed.Service) *Router {
	r.composed = composedService
	return r
}

func (r *Router) WithMusicService(musicService MusicService) *Router {
	r.music = musicService
	return r
}

func (r *Router) Handle(ctx context.Context, request Request) Response {
	switch strings.ToLower(request.Command) {
	case "ping":
		return Response{Content: "pong", Ephemeral: true, Presentation: Presentation{Title: "Panda is online", Accent: AccentSuccess}}
	case "help":
		return r.handleHelp(ctx, request)
	case "poll":
		return r.handlePoll(request)
	case "admin":
		return r.handleAdmin(ctx, request)
	case "ops":
		return r.handleOps(ctx, request)
	case "ask":
		return r.handleAsk(ctx, request, "ask")
	case "chat":
		return r.handleChat(ctx, request)
	case "summarize", "explain", "rewrite", "translate":
		return r.handleTask(ctx, request)
	default:
		return Response{Content: "Unknown command.", Ephemeral: true, Presentation: Presentation{Title: "Unknown command", Accent: AccentWarning}}
	}
}

func (r *Router) handleHelp(ctx context.Context, request Request) Response {
	return Response{
		Content:   r.helpMessage(ctx, request),
		Ephemeral: true,
		Presentation: Presentation{
			Title:  "Panda Help",
			Accent: AccentInfo,
			Footer: "Open-source Discord assistant",
		},
		Actions: []Action{
			{Label: "Commands", URL: pandaCommandsURL},
			{Label: "Setup", URL: pandaSetupURL},
			{Label: "Repository", URL: pandaRepositoryURL},
		},
	}
}

func (r *Router) helpMessage(ctx context.Context, request Request) string {
	access := r.helpAccess(ctx, request)
	if !access.elevated() {
		return regularHelpMessage
	}
	return elevatedHelpMessage(access)
}

func (r *Router) handlePoll(request Request) Response {
	question := strings.TrimSpace(request.Options["question"])
	answers := polls.ParseAnswers(request.Options["answers"])
	poll, err := polls.New(question, answers, intOption(request.Options["duration_hours"], 0), truthyOption(request.Options["allow_multiselect"]))
	if err != nil {
		return Response{
			Content:   "Poll could not be created: " + err.Error(),
			Ephemeral: true,
			Presentation: Presentation{
				Title:  "Poll not created",
				Accent: AccentWarning,
			},
		}
	}
	return Response{Poll: &poll}
}

func (r *Router) helpAccess(ctx context.Context, request Request) helpAccess {
	if r.admin == nil {
		return helpAccess{}
	}
	accessRequest := assistantAccessRequest(request)
	allowed := func(check func(context.Context, admin.AssistantAccessRequest) (bool, error)) bool {
		ok, err := check(ctx, accessRequest)
		return err == nil && ok
	}
	return helpAccess{
		config:      allowed(r.admin.CanWriteConfig),
		soul:        allowed(r.admin.CanWriteSoul),
		moderation:  allowed(r.admin.CanUseModeration),
		toolDraft:   allowed(r.admin.CanDraftComposedTool),
		toolApprove: allowed(r.admin.CanApproveComposedTool),
		toolInvoke:  allowed(r.admin.CanInvokeComposedTool),
		toolAudit:   allowed(r.admin.CanAuditComposedTool),
	}
}

func elevatedHelpMessage(access helpAccess) string {
	var builder strings.Builder
	builder.WriteString(baseHelpMessage)

	if access.moderation {
		builder.WriteString("\n\n**Moderator tools**\n")
		builder.WriteString("- Ask Panda for moderation guidance, review help, and action drafts in chat.\n")
	}

	if access.config || access.soul {
		builder.WriteString("\n\n**Admin commands**\n")
		if access.config {
			builder.WriteString("- `/admin role action:list|set|remove profile:admin|moderator role:@Role` - Panda role profiles\n")
			builder.WriteString("- `/admin member-role action:add|remove user:@User role:@Role` - assign Discord roles to users\n")
			builder.WriteString("- `/admin tool action:list|add|remove tool_name:<tool> role:@Role` - role tool grants\n")
			builder.WriteString("- `/admin channel action:list|allow|deny|remove channel:#Channel`\n")
			builder.WriteString("- `/admin model model:<slug> fallback_models:<csv> temperature:<0-2> max_response_tokens:<64-4000> tool_policy:<policy> dry_run:<bool>`\n")
			builder.WriteString("- Policies: `off`, `read_only`, `assistive`, `admin_only`, `moderator`, `write_confirmed`, `owner_ops`\n")
			builder.WriteString("- `/admin prompt prompt:<text> dry_run:<bool>` - server instructions; omit `prompt` for modal\n")
			builder.WriteString("- `/admin audit` - recent privileged changes\n")
			builder.WriteString("- `/admin enable dry_run:<bool>` / `/admin disable confirm:<token> dry_run:<bool>`\n")
		}
		if access.soul {
			builder.WriteString("- `/admin soul soul:<text> dry_run:<bool>` - personality/tone; omit `soul` for modal\n")
		}
	}

	if access.composedTools() {
		builder.WriteString("\n\n**Composed tools**\n")
		if access.toolDraft {
			builder.WriteString("- Ask Panda to draft or preview new server tools.\n")
		}
		if access.toolAudit {
			builder.WriteString("- Ask Panda to list/show tools or export approved spec JSON.\n")
		}
		if access.toolApprove {
			builder.WriteString("- Ask Panda to approve, pause, resume, disable, archive, or roll back tools. Approval/rollback use buttons.\n")
		}
		if access.toolInvoke {
			builder.WriteString("- Ask Panda to run or simulate approved composed tools.\n")
		}
	}

	if access.config || access.moderation {
		builder.WriteString("\n\n**Also available through Panda chat/tools**\n")
		if access.config {
			builder.WriteString("- Usage and limits\n")
			builder.WriteString("- Server knowledge\n")
			builder.WriteString("- Role/channel access\n")
			builder.WriteString("- Memory consent\n")
		}
		if access.moderation {
			builder.WriteString("- Moderation guidance\n")
		}
	}

	return strings.TrimRight(builder.String(), "\n")
}

func (r *Router) HandleNaturalMessage(ctx context.Context, request Request) Response {
	message := strings.TrimSpace(firstNonEmpty(request.Options["message"], request.Options["question"]))
	if message == "" {
		return Response{}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return Response{}
	}
	if response, ok := r.handleNaturalMusic(ctx, request, message); ok {
		return response
	}
	if response, ok := handleNaturalPoll(message); ok {
		return response
	}
	decision, err := r.assistant.ClassifyNaturalMessage(ctx, assistant.NaturalMessageRequest{
		GuildID:          request.GuildID,
		UserID:           request.UserID,
		ChannelID:        request.ChannelID,
		Content:          message,
		BotMentioned:     truthyOption(request.Options["bot_mentioned"]),
		ReplyContent:     request.Options["reply_text"],
		ReplyMessageID:   request.Options["reply_message_id"],
		ReplyAuthorIsBot: truthyOption(request.Options["reply_author_is_bot"]),
	})
	if err != nil {
		slog.Warn("natural message classification failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("channel_id", request.ChannelID), slog.String("request_id", request.RequestID))
		if prompt, ok := naturalMessageFallbackPrompt(message, request.Options); ok {
			request.Command = "chat"
			if request.Options == nil {
				request.Options = map[string]string{}
			}
			request.Options["question"] = prompt
			return r.handleChatMode(ctx, request, false)
		}
		return Response{}
	}
	if !decision.Respond {
		return Response{}
	}
	request.Command = "chat"
	if request.Options == nil {
		request.Options = map[string]string{}
	}
	request.Options["question"] = decision.Prompt
	return r.handleChatMode(ctx, request, false)
}

func (r *Router) handleNaturalMusic(ctx context.Context, request Request, message string) (Response, bool) {
	if r.music == nil {
		return Response{}, false
	}
	intent, ok := music.ParseIntent(message)
	if !ok {
		return Response{}, false
	}
	response, err := r.music.Handle(ctx, music.Request{
		GuildID:        request.GuildID,
		TextChannelID:  request.ChannelID,
		UserID:         request.UserID,
		VoiceChannelID: request.VoiceChannelID,
		Intent:         intent,
	})
	if err != nil {
		return musicError(err), true
	}
	return responseFromMusic(response), true
}

func musicError(err error) Response {
	switch {
	case errors.Is(err, music.ErrMissingGuild):
		return musicStatus("Music unavailable", "Music only works inside a Discord server.", AccentWarning)
	case errors.Is(err, music.ErrMissingVoice):
		return musicStatus("Join voice first", "Join a voice channel first, then ask me to play the song.", AccentWarning)
	case errors.Is(err, music.ErrMissingSong):
		return musicStatus("Song needed", "Tell me what song to play.", AccentWarning)
	case errors.Is(err, music.ErrNothingPlaying):
		return musicStatus("Nothing playing", "Nothing is playing right now.", AccentWarning)
	case errors.Is(err, music.ErrAlreadyPaused):
		return musicStatus("Already paused", "Music is already paused.", AccentWarning)
	case errors.Is(err, music.ErrAlreadyPlaying):
		return musicStatus("Already playing", "Music is already playing.", AccentWarning)
	case errors.Is(err, music.ErrDifferentVoice):
		return musicStatus("Different voice channel", "I am already playing music in another voice channel.", AccentWarning)
	case errors.Is(err, music.ErrVoiceConnection):
		return musicStatus("Voice connection failed", "I could not join or speak in that voice channel yet. Try again in a moment.", AccentDanger)
	case errors.Is(err, music.ErrDependencyMissing):
		return musicStatus("Audio tools unavailable", "I could not prepare the server-side audio tools yet. Try again in a moment.", AccentDanger)
	case errors.Is(err, music.ErrTrackLookupFailed):
		return musicStatus("Song not found", "I could not find that song.", AccentWarning)
	case errors.Is(err, music.ErrTrackStreamFailed):
		return musicStatus("Stream failed", "I found the song, but could not start the audio stream.", AccentDanger)
	default:
		return musicStatus("Music error", "Music had trouble with that request.", AccentDanger)
	}
}

func responseFromMusic(response music.Response) Response {
	fields := make([]Field, 0, len(response.Fields))
	for _, field := range response.Fields {
		fields = append(fields, Field{Name: field.Name, Value: field.Value, Inline: field.Inline})
	}
	actions := make([]Action, 0, len(response.Actions))
	for _, action := range response.Actions {
		actions = append(actions, Action{Label: action.Label, URL: action.URL})
	}
	return Response{
		Content: response.Content,
		Presentation: Presentation{
			Title:  firstNonEmpty(response.Title, "Music"),
			Accent: AccentMusic,
			URL:    response.URL,
			Fields: fields,
		},
		Actions: actions,
	}
}

func musicStatus(title, content string, accent Accent) Response {
	return Response{
		Content: content,
		Presentation: Presentation{
			Title:  title,
			Accent: accent,
		},
	}
}

func handleNaturalPoll(message string) (Response, bool) {
	poll, ok := naturalPollFromMessage(message)
	if !ok {
		return Response{}, false
	}
	return Response{Poll: &poll}, true
}

func naturalPollFromMessage(message string) (polls.Poll, bool) {
	prompt, ok := naturalPollPrompt(message)
	if !ok {
		return polls.Poll{}, false
	}
	question, answers, ok := naturalPollQuestionAndAnswers(prompt)
	if !ok {
		return polls.Poll{}, false
	}
	poll, err := polls.New(question, answers, 0, false)
	if err != nil {
		return polls.Poll{}, false
	}
	return poll, true
}

func naturalPollPrompt(message string) (string, bool) {
	message = stripLeadingDiscordMention(strings.TrimSpace(message))
	message = stripLeadingPandaWakePhrase(message)
	if !isPollCreateRequest(message) {
		return "", false
	}
	afterPoll, ok := textAfterWord(message, "poll")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(trimNaturalPollLeadIn(afterPoll)), true
}

func stripLeadingDiscordMention(message string) string {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, "<@") {
		return message
	}
	end := strings.Index(message, ">")
	if end < 0 {
		return message
	}
	return strings.TrimLeftFunc(strings.TrimSpace(message[end+1:]), func(value rune) bool {
		return value == ',' || value == ':' || value == '-' || unicode.IsSpace(value)
	})
}

func isPollCreateRequest(message string) bool {
	words := messageWords(message)
	hasPoll := false
	hasCreateVerb := false
	for index, word := range words {
		if word == "poll" {
			hasPoll = true
			if index == 0 {
				hasCreateVerb = true
			}
			continue
		}
		switch word {
		case "make", "create", "start", "open", "post", "run":
			hasCreateVerb = true
		}
	}
	return hasPoll && hasCreateVerb
}

func messageWords(message string) []string {
	var words []string
	wordStart := -1
	for index, value := range message {
		if isMessageWordRune(value) {
			if wordStart < 0 {
				wordStart = index
			}
			continue
		}
		if wordStart >= 0 {
			words = append(words, strings.ToLower(message[wordStart:index]))
			wordStart = -1
		}
	}
	if wordStart >= 0 {
		words = append(words, strings.ToLower(message[wordStart:]))
	}
	return words
}

func textAfterWord(message, target string) (string, bool) {
	wordStart := -1
	for index, value := range message {
		if isMessageWordRune(value) {
			if wordStart < 0 {
				wordStart = index
			}
			continue
		}
		if wordStart >= 0 {
			if strings.EqualFold(message[wordStart:index], target) {
				return message[index:], true
			}
			wordStart = -1
		}
	}
	if wordStart >= 0 && strings.EqualFold(message[wordStart:], target) {
		return "", true
	}
	return "", false
}

func trimNaturalPollLeadIn(value string) string {
	value = strings.TrimSpace(strings.TrimLeft(value, " \t\r\n,;:.-"))
	for _, prefix := range []string{"about ", "for ", "on ", "asking ", "that asks ", "with "} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			return strings.TrimSpace(value[len(prefix):])
		}
	}
	return value
}

func naturalPollQuestionAndAnswers(prompt string) (string, []polls.Answer, bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", nil, false
	}
	if question, answers, ok := explicitNaturalPollOptions(prompt); ok {
		return question, answers, true
	}
	if question, answers, ok := questionMarkNaturalPollOptions(prompt); ok {
		return question, answers, true
	}
	if question, answers, ok := binaryNaturalPollOptions(prompt); ok {
		return question, answers, true
	}
	return "", nil, false
}

func explicitNaturalPollOptions(prompt string) (string, []polls.Answer, bool) {
	for _, label := range []string{" options:", " option:", " choices:", " choice:", " answers:", " answer:"} {
		index := strings.Index(strings.ToLower(prompt), label)
		if index < 0 {
			continue
		}
		question := cleanNaturalPollQuestion(prompt[:index])
		answers := polls.ParseAnswers(prompt[index+len(label):])
		return question, answers, question != "" && len(answers) >= polls.MinAnswers
	}
	return "", nil, false
}

func questionMarkNaturalPollOptions(prompt string) (string, []polls.Answer, bool) {
	index := strings.Index(prompt, "?")
	if index < 0 || index == len(prompt)-1 {
		return "", nil, false
	}
	question := cleanNaturalPollQuestion(prompt[:index+1])
	answers := polls.ParseAnswers(prompt[index+1:])
	return question, answers, question != "" && len(answers) >= polls.MinAnswers
}

func binaryNaturalPollOptions(prompt string) (string, []polls.Answer, bool) {
	left, right, separator, ok := splitBinaryNaturalPoll(prompt)
	if !ok {
		return "", nil, false
	}
	firstAnswer, questionPrefix := splitNaturalPollQuestionPrefix(left)
	firstAnswer = cleanNaturalPollAnswer(firstAnswer)
	secondAnswer := cleanNaturalPollAnswer(right)
	if firstAnswer == "" || secondAnswer == "" {
		return "", nil, false
	}
	question := cleanNaturalPollQuestion(prompt)
	if questionPrefix != "" {
		question = naturalBinaryPollQuestion(questionPrefix, firstAnswer, separator, secondAnswer)
	}
	return question, []polls.Answer{{Text: firstAnswer}, {Text: secondAnswer}}, question != ""
}

func splitBinaryNaturalPoll(prompt string) (string, string, string, bool) {
	lower := strings.ToLower(prompt)
	for _, separator := range []string{" versus ", " vs ", " or "} {
		index := strings.LastIndex(lower, separator)
		if index <= 0 || index+len(separator) >= len(prompt) {
			continue
		}
		return prompt[:index], prompt[index+len(separator):], strings.TrimSpace(separator), true
	}
	return "", "", "", false
}

func splitNaturalPollQuestionPrefix(left string) (string, string) {
	trimmed := strings.TrimSpace(left)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{
		"what will be better",
		"what would be better",
		"what is better",
		"which will be better",
		"which would be better",
		"which is better",
		"what should we pick",
		"which should we pick",
		"who will win",
		"who wins",
	} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimNaturalPollLeadIn(trimmed[len(prefix):])), prefix
		}
	}
	return trimmed, ""
}

func naturalBinaryPollQuestion(prefix, firstAnswer, separator, secondAnswer string) string {
	label := sentenceCase(prefix)
	if separator == "vs" || separator == "versus" {
		separator = "or"
	}
	return fmt.Sprintf("%s: %s %s %s?", label, firstAnswer, separator, secondAnswer)
}

func cleanNaturalPollQuestion(value string) string {
	value = strings.TrimSpace(strings.Trim(value, " \t\r\n,;:."))
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, "?") {
		return value
	}
	return value + "?"
}

func cleanNaturalPollAnswer(value string) string {
	return strings.TrimSpace(strings.Trim(value, " \t\r\n,;:.?!"))
}

func sentenceCase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func naturalMessageFallbackPrompt(message string, options map[string]string) (string, bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", false
	}
	if truthyOption(options["bot_mentioned"]) || truthyOption(options["reply_author_is_bot"]) {
		return message, true
	}
	if directlyAddressesPanda(message) {
		prompt := stripLeadingPandaWakePhrase(message)
		if prompt == "" {
			prompt = message
		}
		return prompt, true
	}
	return "", false
}

func directlyAddressesPanda(message string) bool {
	words := leadingWords(message, 3)
	if len(words) == 0 {
		return false
	}
	if words[0] == "panda" {
		return true
	}
	return len(words) >= 2 && isGreetingWord(words[0]) && words[1] == "panda"
}

func stripLeadingPandaWakePhrase(message string) string {
	tokens := leadingWordTokens(message, 3)
	if len(tokens) == 0 {
		return strings.TrimSpace(message)
	}
	removeThrough := -1
	if strings.EqualFold(tokens[0].word, "panda") {
		removeThrough = 0
	} else if len(tokens) >= 2 && isGreetingWord(strings.ToLower(tokens[0].word)) && strings.EqualFold(tokens[1].word, "panda") {
		removeThrough = 1
	}
	if removeThrough < 0 {
		return strings.TrimSpace(message)
	}
	return strings.TrimLeftFunc(strings.TrimSpace(message[tokens[removeThrough].end:]), func(value rune) bool {
		return value == ',' || value == ':' || value == '-' || unicode.IsSpace(value)
	})
}

type wordToken struct {
	word string
	end  int
}

func leadingWords(message string, limit int) []string {
	tokens := leadingWordTokens(message, limit)
	words := make([]string, 0, len(tokens))
	for _, token := range tokens {
		words = append(words, strings.ToLower(token.word))
	}
	return words
}

func leadingWordTokens(message string, limit int) []wordToken {
	var tokens []wordToken
	wordStart := -1
	for index, value := range message {
		if isMessageWordRune(value) {
			if wordStart < 0 {
				wordStart = index
			}
			continue
		}
		if wordStart >= 0 {
			tokens = append(tokens, wordToken{word: message[wordStart:index], end: index})
			if len(tokens) >= limit {
				return tokens
			}
			wordStart = -1
		}
		if len(tokens) == 0 && !unicode.IsSpace(value) && value != ',' && value != ':' && value != '-' {
			return tokens
		}
	}
	if wordStart >= 0 {
		tokens = append(tokens, wordToken{word: message[wordStart:], end: len(message)})
	}
	if len(tokens) > limit {
		return tokens[:limit]
	}
	return tokens
}

func isGreetingWord(word string) bool {
	switch word {
	case "hey", "hi", "hello", "yo", "ok", "okay", "please":
		return true
	default:
		return false
	}
}

func isMessageWordRune(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func (r *Router) handleOps(ctx context.Context, request Request) Response {
	if !request.IsOwner {
		return Response{Content: "Only a bot owner can use ops commands.", Ephemeral: true}
	}
	switch strings.ToLower(request.Subcommand) {
	case "health":
		health, err := r.ops.Health(ctx)
		if err != nil {
			return Response{Content: "Ops health check failed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Health: sqlite=%s discord=%s shards=%s openrouter=%s queued_jobs=%d guild_configs=%d draining=%t incident=%t data_dir=`%s`.", health.SQLite, health.Discord, health.Shards, health.OpenRouter, health.QueuedJobs, health.ConfiguredGuildCount, health.Draining, health.Incident, health.DataDir), Ephemeral: true}
	case "guilds":
		health, err := r.ops.Health(ctx)
		if err != nil {
			return Response{Content: "Guild lookup failed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Configured guilds: %d.", health.ConfiguredGuildCount), Ephemeral: true}
	case "drain":
		r.ops.Drain()
		return Response{Content: "Queue worker is draining and will not claim new jobs.", Ephemeral: true}
	case "resume":
		r.ops.Resume()
		return Response{Content: "Queue worker resumed job processing.", Ephemeral: true}
	case "incident":
		action := strings.ToLower(strings.TrimSpace(request.Options["action"]))
		switch action {
		case "enable":
			r.ops.EnableIncident()
			return Response{Content: "Incident mode enabled.", Ephemeral: true}
		case "disable":
			r.ops.DisableIncident()
			return Response{Content: "Incident mode disabled.", Ephemeral: true}
		default:
			health, err := r.ops.Health(ctx)
			if err != nil {
				return Response{Content: "Incident status lookup failed.", Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Incident mode: %t.", health.Incident), Ephemeral: true}
		}
	case "reload":
		if err := r.ops.Reload(ctx); err != nil {
			return Response{Content: "Runtime config reload check failed.", Ephemeral: true}
		}
		return Response{Content: "Runtime config reload check passed.", Ephemeral: true}
	default:
		return Response{Content: "Unknown ops command.", Ephemeral: true}
	}
}

func (r *Router) handleAdmin(ctx context.Context, request Request) Response {
	if request.GuildID == "" {
		return Response{Content: "Admin commands must be run inside a Discord server.", Ephemeral: true}
	}

	subcommand := strings.ToLower(request.Subcommand)
	if !request.IsGuildAdmin && !request.IsOwner {
		if subcommand != "soul" {
			allowed, err := r.admin.CanWriteConfig(ctx, assistantAccessRequest(request))
			if err != nil {
				return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
			}
			if !allowed {
				return Response{Content: "Only the Panda owner, server owner or administrator, or a delegated config role can use admin commands.", Ephemeral: true}
			}
		}
		if subcommand == "soul" {
			allowed, err := r.admin.CanWriteSoul(ctx, assistantAccessRequest(request))
			if err != nil {
				return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
			}
			if !allowed {
				return Response{Content: "Only the Panda owner, server owner or administrator, moderator, creator, or delegated soul writer can update Panda's soul.", Ephemeral: true}
			}
		}
	}

	switch subcommand {
	case "role":
		return r.handleAdminRoleProfile(ctx, request)
	case "member-role", "member_role":
		return r.handleAdminMemberRole(ctx, request)
	case "tool":
		return r.handleAdminToolAccess(ctx, request)
	case "channel", "channels":
		return r.handleAdminChannelAccess(ctx, request)
	case "model":
		return r.handleAdminModel(ctx, request)
	case "prompt":
		return r.handleAdminPrompt(ctx, request)
	case "soul":
		return r.handleAdminSoul(ctx, request)
	case "audit":
		return r.handleAdminAudit(ctx, request)
	case "enable":
		return r.handleAdminToggle(ctx, request, true)
	case "disable":
		return r.handleAdminToggle(ctx, request, false)
	default:
		return Response{Content: "Unknown admin command.", Ephemeral: true}
	}
}

func roleDisplay(roleID, roleName string) string {
	roleID = strings.TrimSpace(roleID)
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return fmt.Sprintf("`%s`", roleID)
	}
	return fmt.Sprintf("`%s` (`%s`)", roleName, roleID)
}

func (r *Router) handleAdminRoleProfile(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	switch action {
	case "list", "":
		roles, err := r.admin.ListRolePermissions(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Role profile lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderRoleProfiles(roles), Ephemeral: true}
	case "set", "add":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can set Panda role profiles."); denied.Content != "" {
			return denied
		}
		profile, roleID, roleName, response := roleProfileOptions(request)
		if response.Content != "" {
			return response
		}
		if _, err := r.admin.ApplyRoleProfile(ctx, request.GuildID, request.UserID, roleID, profile); err != nil {
			return Response{Content: "Role profile could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("%s is now a Panda %s role.", roleDisplay(roleID, roleName), admin.RoleProfileLabel(profile)), Ephemeral: true}
	case "remove", "unset":
		if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can remove Panda role profiles."); denied.Content != "" {
			return denied
		}
		profile, roleID, roleName, response := roleProfileOptions(request)
		if response.Content != "" {
			return response
		}
		if err := r.admin.RemoveRoleProfile(ctx, request.GuildID, request.UserID, roleID, profile); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That role profile was not configured for this role.", Ephemeral: true}
			}
			return Response{Content: "Role profile could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from %s.", admin.RoleProfileLabel(profile), roleDisplay(roleID, roleName)), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `set`, or `remove`.", Ephemeral: true}
	}
}

func roleProfileOptions(request Request) (string, string, string, Response) {
	profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
	if !ok {
		return "", "", "", Response{Content: "`profile` must be `admin` or `moderator`.", Ephemeral: true}
	}
	roleID := strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	if roleID == "" {
		return "", "", "", Response{Content: "Choose a Discord role.", Ephemeral: true}
	}
	return profile, roleID, request.Options["role_name"], Response{}
}

func renderRoleProfiles(roles []store.GuildRole) string {
	adminRoles := roleIDsForPermission(roles, admin.PermissionAdminBadge)
	moderatorRoles := roleIDsForPermission(roles, admin.PermissionModerationUse)
	var builder strings.Builder
	builder.WriteString("Panda role profiles:\n")
	builder.WriteString(roleProfileLine("admin", adminRoles))
	builder.WriteString("\n")
	builder.WriteString(roleProfileLine("moderator", moderatorRoles))
	builder.WriteString("\n\nModerator roles include `assistant.use` and `moderation.use`.")
	return builder.String()
}

func roleIDsForPermission(roles []store.GuildRole, permission string) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, role := range roles {
		if role.Permission != permission {
			continue
		}
		if _, ok := seen[role.RoleID]; ok {
			continue
		}
		seen[role.RoleID] = struct{}{}
		ids = append(ids, role.RoleID)
	}
	sort.Strings(ids)
	return ids
}

func roleProfileLine(profile string, roleIDs []string) string {
	if len(roleIDs) == 0 {
		return fmt.Sprintf("- %s: not configured", profile)
	}
	values := make([]string, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		values = append(values, fmt.Sprintf("`%s`", roleID))
	}
	return fmt.Sprintf("- %s: %s", profile, strings.Join(values, ", "))
}

func (r *Router) handleAdminMemberRole(ctx context.Context, request Request) Response {
	if denied := r.ensureGuildControl(ctx, request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can assign Discord roles."); denied.Content != "" {
		return denied
	}
	if r.memberRoles == nil {
		return Response{Content: "Discord role assignment is not configured for this runtime.", Ephemeral: true}
	}
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "add")))
	memberRequest, response := memberRoleOptions(request)
	if response.Content != "" {
		return response
	}
	switch action {
	case "add", "assign", "set":
		memberRequest.Reason = "Panda admin member-role add"
		if err := r.memberRoles.AddMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be assigned. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Assigned %s to %s.", roleDisplay(memberRequest.RoleID, request.Options["role_name"]), userMention(memberRequest.UserID, request.Options["member_user_name"])), Ephemeral: true}
	case "remove", "unassign", "unset":
		memberRequest.Reason = "Panda admin member-role remove"
		if err := r.memberRoles.RemoveMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be removed. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from %s.", roleDisplay(memberRequest.RoleID, request.Options["role_name"]), userMention(memberRequest.UserID, request.Options["member_user_name"])), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `add` or `remove`.", Ephemeral: true}
	}
}

func memberRoleOptions(request Request) (MemberRoleRequest, Response) {
	userID := strings.TrimSpace(firstNonEmpty(request.Options["member_user_id"], firstNonEmpty(request.Options["user_id"], request.Options["user"])))
	roleID := strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	if userID == "" {
		return MemberRoleRequest{}, Response{Content: "Choose a Discord user.", Ephemeral: true}
	}
	if roleID == "" {
		return MemberRoleRequest{}, Response{Content: "Choose a Discord role.", Ephemeral: true}
	}
	return MemberRoleRequest{GuildID: request.GuildID, UserID: userID, RoleID: roleID, ActorID: request.UserID}, Response{}
}

func userMention(userID, username string) string {
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Sprintf("`%s`", userID)
	}
	return fmt.Sprintf("`%s` (`%s`)", username, userID)
}

func (r *Router) handleAdminToolAccess(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	toolName := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["tool_name"], request.Options["tool"])))
	roleID := strings.TrimSpace(firstNonEmpty(request.Options["role_id"], request.Options["role"]))
	switch action {
	case "list", "":
		roles, err := r.admin.ListToolRoles(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Tool access lookup failed.", Ephemeral: true}
		}
		if len(roles) == 0 {
			return Response{Content: "No role-specific tool access rules are configured. Native tools use their normal permission policy; composed tools are admin-only until a role is allowed.", Ephemeral: true}
		}
		lines := []string{"Tool access rules:"}
		for _, role := range roles {
			lines = append(lines, fmt.Sprintf("- `%s` -> `%s`", role.ToolName, role.RoleID))
		}
		return Response{Content: strings.Join(lines, "\n"), Ephemeral: true}
	case "add", "allow":
		if toolName == "" || roleID == "" {
			return Response{Content: "Provide `tool_name` and `role` to allow a role to use a tool.", Ephemeral: true}
		}
		toolRole, err := r.admin.AddToolRole(ctx, request.GuildID, request.UserID, toolName, roleID)
		if err != nil {
			return Response{Content: "Tool access could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Allowed %s to use `%s`.", roleDisplay(toolRole.RoleID, request.Options["role_name"]), toolRole.ToolName), Ephemeral: true}
	case "remove", "deny":
		if toolName == "" || roleID == "" {
			return Response{Content: "Provide `tool_name` and `role` to remove tool access.", Ephemeral: true}
		}
		if err := r.admin.RemoveToolRole(ctx, request.GuildID, request.UserID, toolName, roleID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That tool access rule was not found.", Ephemeral: true}
			}
			return Response{Content: "Tool access could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed %s from `%s`.", roleDisplay(roleID, request.Options["role_name"]), toolName), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `add`, or `remove`.", Ephemeral: true}
	}
}

func (r *Router) handleAdminChannelAccess(ctx context.Context, request Request) Response {
	action := strings.ToLower(strings.TrimSpace(firstNonEmpty(request.Options["action"], "list")))
	channelID, channelName := channelOptions(request)
	switch action {
	case "list", "":
		rules, err := r.admin.ListChannelRules(ctx, request.GuildID)
		if err != nil {
			return Response{Content: "Channel access lookup failed.", Ephemeral: true}
		}
		return Response{Content: renderChannelRules(rules), Ephemeral: true}
	case "allow", "add":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to allow Panda assistant use there.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("Panda assistant use would be allowed in %s.", channelDisplay(channelID, channelName))
		}
		rule, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, "allow")
		if err != nil {
			return Response{Content: "Channel access rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Allowed Panda assistant use in %s. Because an allow rule exists, other channels need their own allow rule unless the user is an admin.", channelDisplay(rule.ChannelID, channelName)), Ephemeral: true}
	case "deny", "block":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to deny Panda assistant use there.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("Panda assistant use would be denied in %s.", channelDisplay(channelID, channelName))
		}
		rule, err := r.admin.SetChannelRule(ctx, request.GuildID, request.UserID, channelID, "deny")
		if err != nil {
			return Response{Content: "Channel access rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Denied Panda assistant use in %s.", channelDisplay(rule.ChannelID, channelName)), Ephemeral: true}
	case "remove", "clear":
		if channelID == "" {
			return Response{Content: "Choose a `channel` to remove from Panda channel access rules.", Ephemeral: true}
		}
		if dryRunRequested(request) {
			return dryRunResponse("The Panda channel access rule for %s would be removed.", channelDisplay(channelID, channelName))
		}
		if err := r.admin.RemoveChannelRule(ctx, request.GuildID, request.UserID, channelID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return Response{Content: "That channel access rule was not found.", Ephemeral: true}
			}
			return Response{Content: "Channel access rule could not be removed.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed Panda channel access rule for %s.", channelDisplay(channelID, channelName)), Ephemeral: true}
	default:
		return Response{Content: "`action` must be `list`, `allow`, `deny`, or `remove`.", Ephemeral: true}
	}
}

func channelOptions(request Request) (string, string) {
	channelID := normalizeChannelID(firstNonEmpty(request.Options["channel_id"], request.Options["channel"]))
	channelName := strings.TrimPrefix(strings.TrimSpace(request.Options["channel_name"]), "#")
	return channelID, channelName
}

func normalizeChannelID(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<#") && strings.HasSuffix(value, ">") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "<#"), ">"))
	}
	return value
}

func channelDisplay(channelID, channelName string) string {
	channelID = normalizeChannelID(channelID)
	channelName = strings.TrimPrefix(strings.TrimSpace(channelName), "#")
	if channelName != "" {
		return fmt.Sprintf("`#%s` (`%s`)", channelName, channelID)
	}
	return fmt.Sprintf("`%s`", channelID)
}

func renderChannelRules(rules []store.GuildChannelRule) string {
	if len(rules) == 0 {
		return "No channel access rules are configured. Panda assistant use is available in any channel where Discord permissions allow it."
	}
	hasAllow := false
	for _, rule := range rules {
		if rule.Rule == "allow" {
			hasAllow = true
			break
		}
	}
	header := "Channel access rules:"
	if hasAllow {
		header = "Channel access rules (allow-list active):"
	}
	lines := []string{header}
	for _, rule := range rules {
		lines = append(lines, fmt.Sprintf("- `%s` %s", rule.Rule, channelDisplay(rule.ChannelID, "")))
	}
	if hasAllow {
		lines = append(lines, "Only allowed channels can use Panda assistant features unless the user is an admin.")
	}
	return strings.Join(lines, "\n")
}

func (r *Router) handleAdminModel(ctx context.Context, request Request) Response {
	settings, parseErr := modelSettingsFromOptions(request.Options)
	if parseErr != nil {
		return Response{Content: parseErr.Error(), Ephemeral: true}
	}
	if dryRunRequested(request) {
		return dryRunResponse("model settings would be updated. %s", renderModelSettingsDryRun(settings))
	}
	config, err := r.admin.ConfigureModel(ctx, request.GuildID, request.UserID, settings)
	if err != nil {
		return Response{Content: "Model update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Model settings updated. Default `%s`, %d fallback model(s), temperature %.2f, max response %d tokens, tool policy `%s`.", config.DefaultModel, fallbackModelCount(config.FallbackModels), config.Temperature, config.MaxResponseTokens, config.ToolPolicy), Ephemeral: true}
}

func (r *Router) handleAdminPrompt(ctx context.Context, request Request) Response {
	prompt := strings.TrimSpace(request.Options["prompt"])
	if dryRunRequested(request) {
		return dryRunResponse("server prompt would be updated (%d characters).", len(prompt))
	}
	if prompt == "" {
		return promptModalResponse(request.UserID)
	}
	config, err := r.admin.SetPrompt(ctx, request.GuildID, request.UserID, prompt)
	if err != nil {
		return Response{Content: "Prompt update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Server prompt updated (%d characters).", len(config.SystemPromptOverlay)), Ephemeral: true}
}

func (r *Router) handleAdminSoul(ctx context.Context, request Request) Response {
	soul := strings.TrimSpace(request.Options["soul"])
	if dryRunRequested(request) {
		return dryRunResponse("agent soul would be updated (%d characters).", len(soul))
	}
	if soul == "" {
		return soulModalResponse(request.UserID)
	}
	config, err := r.admin.SetSoul(ctx, request.GuildID, request.UserID, soul)
	if err != nil {
		return Response{Content: "Soul update failed.", Ephemeral: true}
	}
	return Response{Content: fmt.Sprintf("Agent soul updated (%d characters).", len(config.AgentSoul)), Ephemeral: true}
}

func (r *Router) handleAdminToggle(ctx context.Context, request Request, enabled bool) Response {
	if dryRunRequested(request) {
		if enabled {
			return dryRunResponse("assistant responses would be enabled for this server.")
		}
		return dryRunResponse("assistant responses would be disabled for this server.")
	}
	if !enabled {
		confirmationID := adminDisableConfirmationID(request.UserID)
		if !confirmed(request, confirmationID) {
			return destructiveConfirmation(confirmationID, "Disable assistant", "This will pause assistant responses for this server.")
		}
	}
	_, err := r.admin.SetAssistantEnabled(ctx, request.GuildID, request.UserID, enabled)
	if err != nil {
		return Response{Content: "Assistant status update failed.", Ephemeral: true}
	}
	if enabled {
		return Response{Content: "Assistant responses are enabled.", Ephemeral: true}
	}
	return Response{Content: "Assistant responses are disabled.", Ephemeral: true}
}

func (r *Router) handleAdminAudit(ctx context.Context, request Request) Response {
	events, err := r.admin.RecentAudit(ctx, request.GuildID, 10)
	if err != nil {
		return Response{Content: "Audit lookup failed.", Ephemeral: true}
	}
	if len(events) == 0 {
		return Response{Content: "No audit events recorded yet.", Ephemeral: true}
	}
	return Response{Content: renderAudit(events), Ephemeral: true}
}

func (r *Router) invocationContext(ctx context.Context, request Request) string {
	if r.context == nil || strings.TrimSpace(request.GuildID) == "" || strings.TrimSpace(request.ChannelID) == "" {
		return ""
	}
	packed, err := r.context.RecentMessagesSinceContext(ctx, contextsvc.ChannelRef{
		GuildID:   request.GuildID,
		ChannelID: request.ChannelID,
	}, invocationContextLimit, time.Now().UTC().Add(-invocationContextWindow))
	if err != nil {
		slog.Warn("invocation context fetch failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("channel_id", request.ChannelID), slog.String("request_id", request.RequestID))
		return ""
	}
	if strings.TrimSpace(packed.Text) == "" || len(packed.Citations) == 0 {
		return ""
	}
	return packed.Text
}

func (r *Router) handleAsk(ctx context.Context, request Request, command string) Response {
	question := strings.TrimSpace(request.Options["question"])
	if question == "" {
		return Response{Content: "Please include a question.", Ephemeral: true}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied
	}

	toolFilter := r.toolFilter(ctx, request)
	invocationContext := r.invocationContext(ctx, request)
	answer, err := r.assistant.Ask(ctx, assistant.AskRequest{
		RequestID:                    request.RequestID,
		GuildID:                      request.GuildID,
		UserID:                       request.UserID,
		ChannelID:                    request.ChannelID,
		Question:                     question,
		InvocationContext:            invocationContext,
		AllowedPermissions:           r.allowedToolPermissions(ctx, request),
		AllowedTools:                 toolFilter.allowed,
		RestrictedTools:              toolFilter.restricted,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	})
	if err != nil {
		return assistantError(err)
	}
	if strings.TrimSpace(answer.Content) == "" {
		return Response{Content: "The model returned an empty response.", Ephemeral: true}
	}
	return responseFromAssistantAnswer(request.UserID, answer, "", "")
}

func (r *Router) handleChat(ctx context.Context, request Request) Response {
	return r.handleChatMode(ctx, request, true)
}

func (r *Router) handleChatMode(ctx context.Context, request Request, threaded bool) Response {
	question := strings.TrimSpace(request.Options["question"])
	if question == "" {
		return Response{Content: "Please include a message.", Ephemeral: true}
	}
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	if threaded && r.threads != nil && request.GuildID != "" {
		if denied := r.ensureThreadsAllowed(ctx, request); denied.Content != "" {
			return denied
		}
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied
	}

	chatChannelID := request.ChannelID
	threadID := ""
	threadName := ""
	if threaded && r.threads != nil && request.GuildID != "" {
		thread, err := r.threads.EnsureChatThread(ctx, ThreadRequest{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
			UserID:    request.UserID,
			Title:     chatThreadTitle(question),
		})
		if err != nil {
			return Response{Content: "I could not create a chat thread here. Please check my thread permissions.", Ephemeral: true}
		}
		chatChannelID = thread.ID
		threadID = thread.ID
		threadName = thread.Name
	}

	toolFilter := r.toolFilter(ctx, request)
	invocationContext := r.invocationContext(ctx, request)
	answer, err := r.assistant.Chat(ctx, assistant.AskRequest{
		RequestID:                    request.RequestID,
		GuildID:                      request.GuildID,
		UserID:                       request.UserID,
		ChannelID:                    chatChannelID,
		ThreadID:                     threadID,
		Question:                     question,
		InvocationContext:            invocationContext,
		ReplyContent:                 request.Options["reply_text"],
		ReplyMessageID:               request.Options["reply_message_id"],
		ReplyAuthorIsBot:             truthyOption(request.Options["reply_author_is_bot"]),
		AllowedPermissions:           r.allowedToolPermissions(ctx, request),
		AllowedTools:                 toolFilter.allowed,
		RestrictedTools:              toolFilter.restricted,
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	})
	if err != nil {
		return assistantError(err)
	}
	return responseFromAssistantAnswer(request.UserID, answer, threadID, threadName)
}

func (r *Router) handleTask(ctx context.Context, request Request) Response {
	if denied := r.ensureAssistantAllowed(ctx, request); denied.Content != "" {
		return denied
	}
	input, contextError := r.taskInput(ctx, request)
	if contextError.Content != "" {
		return contextError
	}
	if limited := r.allowUser(request.UserID); limited.Content != "" {
		return limited
	}
	if denied := r.ensureBudgetAvailable(ctx, request); denied.Content != "" {
		return denied
	}

	toolFilter := r.toolFilter(ctx, request)
	invocationContext := r.invocationContext(ctx, request)
	task := BackgroundTask{
		RequestID:                    request.RequestID,
		GuildID:                      request.GuildID,
		UserID:                       request.UserID,
		ChannelID:                    request.ChannelID,
		Command:                      request.Command,
		Input:                        input,
		InvocationContext:            invocationContext,
		Tone:                         request.Options["tone"],
		Language:                     request.Options["language"],
		Detail:                       request.Options["detail"],
		AllowedPermissions:           permissionNames(r.allowedToolPermissions(ctx, request)),
		AllowedTools:                 permissionNames(toolFilter.allowed),
		RestrictedTools:              permissionNames(toolFilter.restricted),
		RequireExplicitComposedTools: toolFilter.requireExplicitComposed,
	}
	if shouldBackgroundTask(request, input) {
		return Response{
			Content:      "Queued long summary. The result will replace this response when it is ready.",
			Presentation: Presentation{Title: "Summary queued", Accent: AccentInfo},
			Background:   &task,
		}
	}
	return r.HandleBackgroundTask(ctx, task)
}

func (r *Router) HandleBackgroundTask(ctx context.Context, task BackgroundTask) Response {
	answer, err := r.assistant.CompleteTask(ctx, assistant.TaskRequest{
		RequestID:                    task.RequestID,
		GuildID:                      task.GuildID,
		UserID:                       task.UserID,
		ChannelID:                    task.ChannelID,
		Command:                      task.Command,
		Input:                        task.Input,
		InvocationContext:            task.InvocationContext,
		Tone:                         task.Tone,
		Language:                     task.Language,
		Detail:                       task.Detail,
		AllowedPermissions:           permissionsFromNames(task.AllowedPermissions),
		AllowedTools:                 permissionsFromNames(task.AllowedTools),
		RestrictedTools:              permissionsFromNames(task.RestrictedTools),
		RequireExplicitComposedTools: task.RequireExplicitComposedTools,
	})
	if err != nil {
		return assistantError(err)
	}
	return responseFromAssistantAnswer(task.UserID, answer, "", "")
}

func (r *Router) HandleToolConfirmation(ctx context.Context, request ToolConfirmationRequest) Response {
	if request.Request.GuildID == "" {
		return Response{Content: "This confirmation must be used inside a Discord server.", Ephemeral: true}
	}
	switch request.Action {
	case toolActionKnowledgeDelete:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanManageMemory, "You do not have permission to manage server knowledge."); denied.Content != "" {
			return denied
		}
		documentID, err := strconv.ParseUint(strings.TrimSpace(request.Options["document_id"]), 10, 64)
		if err != nil || documentID == 0 {
			return Response{Content: "That knowledge deletion confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.DeleteMemoryDocument(ctx, request.Request.GuildID, request.Request.UserID, uint(documentID)); err != nil {
			return toolConfirmationError(err, "Knowledge document could not be deleted.", "That knowledge document was not found.")
		}
		return Response{Content: fmt.Sprintf("Deleted knowledge document `%d`.", documentID), Ephemeral: true}
	case toolActionBudgetLimitSet:
		scope := strings.ToLower(strings.TrimSpace(request.Options["scope"]))
		if !validBudgetScope(scope) {
			return Response{Content: "That budget-limit confirmation is invalid.", Ephemeral: true}
		}
		if scope == repository.BudgetScopeGlobal {
			if !request.Request.IsOwner {
				return Response{Content: "Only a bot owner can set global limits.", Ephemeral: true}
			}
		} else if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage limits."); denied.Content != "" {
			return denied
		}
		subjectID := strings.TrimSpace(request.Options["subject_id"])
		if scope == repository.BudgetScopeGuild && subjectID == "" {
			subjectID = request.Request.GuildID
		}
		limit := intOption(request.Options["limit"], 0)
		windowSeconds := intOption(request.Options["window_seconds"], 0)
		if limit <= 0 || windowSeconds <= 0 {
			return Response{Content: "That budget-limit confirmation is invalid.", Ephemeral: true}
		}
		saved, err := r.admin.SetBudgetLimit(ctx, request.Request.GuildID, request.Request.UserID, store.BudgetLimit{
			Scope:         scope,
			SubjectID:     subjectID,
			Limit:         limit,
			WindowSeconds: windowSeconds,
		})
		if err != nil {
			return Response{Content: "Budget limit could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Set `%s` budget limit for `%s` to %d request(s) per %d seconds.", saved.Scope, firstNonEmpty(saved.SubjectID, "global"), saved.Limit, saved.WindowSeconds), Ephemeral: true}
	case toolActionBudgetLimitRemove:
		scope := strings.ToLower(strings.TrimSpace(request.Options["scope"]))
		if !validBudgetScope(scope) {
			return Response{Content: "That budget-limit confirmation is invalid.", Ephemeral: true}
		}
		if scope == repository.BudgetScopeGlobal {
			if !request.Request.IsOwner {
				return Response{Content: "Only a bot owner can remove global limits.", Ephemeral: true}
			}
		} else if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage limits."); denied.Content != "" {
			return denied
		}
		subjectID := strings.TrimSpace(request.Options["subject_id"])
		if scope == repository.BudgetScopeGuild && subjectID == "" {
			subjectID = request.Request.GuildID
		}
		if err := r.admin.RemoveBudgetLimit(ctx, request.Request.GuildID, request.Request.UserID, scope, subjectID); err != nil {
			return toolConfirmationError(err, "Budget limit could not be removed.", "That budget limit was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed `%s` budget limit for `%s`.", scope, firstNonEmpty(subjectID, "global")), Ephemeral: true}
	case toolActionRolePermissionAdd:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage role permissions."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		permission := strings.TrimSpace(request.Options["permission"])
		if roleID == "" || !admin.IsPermissionNameAllowed(permission) {
			return Response{Content: "That role-permission confirmation is invalid.", Ephemeral: true}
		}
		if _, err := r.admin.AddRolePermission(ctx, request.Request.GuildID, request.Request.UserID, roleID, permission); err != nil {
			return Response{Content: "Role permission could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Granted `%s` to role `%s`.", permission, roleID), Ephemeral: true}
	case toolActionRolePermissionRemove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage role permissions."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		permission := strings.TrimSpace(request.Options["permission"])
		if roleID == "" || !admin.IsPermissionNameAllowed(permission) {
			return Response{Content: "That role-permission confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveRolePermission(ctx, request.Request.GuildID, request.Request.UserID, roleID, permission); err != nil {
			return toolConfirmationError(err, "Role permission could not be removed.", "That role permission was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed `%s` from role `%s`.", permission, roleID), Ephemeral: true}
	case toolActionRoleProfileAdd:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can set Panda role profiles."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
		if roleID == "" || !ok {
			return Response{Content: "That role-profile confirmation is invalid.", Ephemeral: true}
		}
		if _, err := r.admin.ApplyRoleProfile(ctx, request.Request.GuildID, request.Request.UserID, roleID, profile); err != nil {
			return Response{Content: "Role profile could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Role `%s` is now a Panda %s role.", roleID, admin.RoleProfileLabel(profile)), Ephemeral: true}
	case toolActionRoleProfileRemove:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can remove Panda role profiles."); denied.Content != "" {
			return denied
		}
		roleID := strings.TrimSpace(request.Options["role_id"])
		profile, ok := admin.NormalizeRoleProfile(request.Options["profile"])
		if roleID == "" || !ok {
			return Response{Content: "That role-profile confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveRoleProfile(ctx, request.Request.GuildID, request.Request.UserID, roleID, profile); err != nil {
			return toolConfirmationError(err, "Role profile could not be removed.", "That role profile was not configured for this role.")
		}
		return Response{Content: fmt.Sprintf("Removed the Panda %s profile from role `%s`.", admin.RoleProfileLabel(profile), roleID), Ephemeral: true}
	case toolActionMemberRoleAdd, toolActionMemberRoleRemove:
		if denied := r.ensureGuildControl(ctx, request.Request, "Only the Panda owner, server owner or administrator, or the current Panda admin role can assign Discord roles."); denied.Content != "" {
			return denied
		}
		if r.memberRoles == nil {
			return Response{Content: "Discord role assignment is not configured for this runtime.", Ephemeral: true}
		}
		memberRequest := MemberRoleRequest{
			GuildID: request.Request.GuildID,
			UserID:  strings.TrimSpace(request.Options["user_id"]),
			RoleID:  strings.TrimSpace(request.Options["role_id"]),
			ActorID: request.Request.UserID,
		}
		if memberRequest.UserID == "" || memberRequest.RoleID == "" {
			return Response{Content: "That member-role confirmation is invalid.", Ephemeral: true}
		}
		if request.Action == toolActionMemberRoleAdd {
			memberRequest.Reason = "Panda natural-language member-role add"
			if err := r.memberRoles.AddMemberRole(ctx, memberRequest); err != nil {
				return Response{Content: "Discord role could not be assigned. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Assigned role `%s` to user `%s`.", memberRequest.RoleID, memberRequest.UserID), Ephemeral: true}
		}
		memberRequest.Reason = "Panda natural-language member-role remove"
		if err := r.memberRoles.RemoveMemberRole(ctx, memberRequest); err != nil {
			return Response{Content: "Discord role could not be removed. Check Panda's Manage Roles permission and role hierarchy.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Removed role `%s` from user `%s`.", memberRequest.RoleID, memberRequest.UserID), Ephemeral: true}
	case toolActionToolAccessAdd, toolActionToolAccessRemove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage tool access."); denied.Content != "" {
			return denied
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		roleID := strings.TrimSpace(request.Options["role_id"])
		if toolName == "" || roleID == "" {
			return Response{Content: "That tool-access confirmation is invalid.", Ephemeral: true}
		}
		if request.Action == toolActionToolAccessAdd {
			if _, err := r.admin.AddToolRole(ctx, request.Request.GuildID, request.Request.UserID, toolName, roleID); err != nil {
				return Response{Content: "Tool access could not be saved.", Ephemeral: true}
			}
			return Response{Content: fmt.Sprintf("Allowed `%s` for role `%s`.", toolName, roleID), Ephemeral: true}
		}
		if err := r.admin.RemoveToolRole(ctx, request.Request.GuildID, request.Request.UserID, toolName, roleID); err != nil {
			return toolConfirmationError(err, "Tool access could not be removed.", "That tool access rule was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed `%s` access for role `%s`.", toolName, roleID), Ephemeral: true}
	case toolActionChannelRuleSet:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage channel rules."); denied.Content != "" {
			return denied
		}
		channelID := strings.TrimSpace(request.Options["channel_id"])
		rule := strings.ToLower(strings.TrimSpace(request.Options["rule"]))
		if channelID == "" || (rule != "allow" && rule != "deny") {
			return Response{Content: "That channel-rule confirmation is invalid.", Ephemeral: true}
		}
		saved, err := r.admin.SetChannelRule(ctx, request.Request.GuildID, request.Request.UserID, channelID, rule)
		if err != nil {
			return Response{Content: "Channel rule could not be saved.", Ephemeral: true}
		}
		return Response{Content: fmt.Sprintf("Set `%s` channel access rule for `%s`.", saved.Rule, saved.ChannelID), Ephemeral: true}
	case toolActionChannelRuleRemove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanWriteConfig, "You do not have permission to manage channel rules."); denied.Content != "" {
			return denied
		}
		channelID := strings.TrimSpace(request.Options["channel_id"])
		if channelID == "" {
			return Response{Content: "That channel-rule confirmation is invalid.", Ephemeral: true}
		}
		if err := r.admin.RemoveChannelRule(ctx, request.Request.GuildID, request.Request.UserID, channelID); err != nil {
			return toolConfirmationError(err, "Channel rule could not be removed.", "That channel rule was not found.")
		}
		return Response{Content: fmt.Sprintf("Removed channel access rule for `%s`.", channelID), Ephemeral: true}
	case toolActionComposedToolApprove:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanApproveComposedTool, "You do not have permission to approve composed tools."); denied.Content != "" {
			return denied
		}
		if r.composed == nil {
			return Response{Content: "Composed tools are not configured for this runtime.", Ephemeral: true}
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		version := intOption(request.Options["version"], 1)
		if toolName == "" || version <= 0 {
			return Response{Content: "That composed-tool approval confirmation is invalid.", Ephemeral: true}
		}
		result, err := r.composed.Approve(ctx, request.Request.GuildID, toolName, version, request.Request.UserID)
		if err != nil {
			return toolConfirmationError(err, "Composed tool could not be approved.", "That composed tool version was not found.")
		}
		return Response{Content: fmt.Sprintf("Approved `%s` version %d. Risk: `%s`.", result.Tool, result.Version, result.Validation.RiskLevel), Ephemeral: true}
	case toolActionComposedToolRollback:
		if denied := r.ensureToolConfirmationPermission(ctx, request.Request, r.admin.CanApproveComposedTool, "You do not have permission to roll back composed tools."); denied.Content != "" {
			return denied
		}
		if r.composed == nil {
			return Response{Content: "Composed tools are not configured for this runtime.", Ephemeral: true}
		}
		toolName := strings.TrimSpace(request.Options["tool_name"])
		version := intOption(request.Options["version"], 0)
		if toolName == "" || version <= 0 {
			return Response{Content: "That composed-tool rollback confirmation is invalid.", Ephemeral: true}
		}
		result, err := r.composed.Rollback(ctx, request.Request.GuildID, toolName, version, request.Request.UserID)
		if err != nil {
			return toolConfirmationError(err, "Composed tool could not be rolled back.", "That approved composed tool version was not found.")
		}
		return Response{Content: fmt.Sprintf("Rolled `%s` back to version %d.", result.Tool, result.Version), Ephemeral: true}
	default:
		return Response{Content: "That confirmation is no longer supported.", Ephemeral: true}
	}
}

func (r *Router) taskInput(ctx context.Context, request Request) (string, Response) {
	input := strings.TrimSpace(firstNonEmpty(request.Options["text"], request.Options["question"]))
	if input != "" {
		return input, Response{}
	}
	if attachmentID := strings.TrimSpace(request.Options["attachment_id"]); attachmentID != "" {
		return r.attachmentInput(ctx, request, attachmentID)
	}
	if !hasContextOptions(request.Options) {
		return "", Response{Content: "Please include text, an `attachment_id`, a `message_id`, or a `recent_limit` to work with.", Ephemeral: true}
	}
	if r.context == nil {
		return "", Response{Content: "Discord context fetching is not configured for this runtime.", Ephemeral: true}
	}

	var packed contextsvc.PackedContext
	var err error
	targetType := "discord_recent_messages"
	targetID := request.ChannelID
	if messageID := strings.TrimSpace(request.Options["message_id"]); messageID != "" {
		targetType = "discord_message"
		targetID = messageID
		packed, err = r.context.MessageContext(ctx, contextsvc.MessageRef{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
			MessageID: messageID,
		})
	} else {
		limit := intOption(request.Options["recent_limit"], 10)
		packed, err = r.context.RecentMessagesContext(ctx, contextsvc.ChannelRef{
			GuildID:   request.GuildID,
			ChannelID: request.ChannelID,
		}, limit)
	}
	if err != nil {
		return "", Response{Content: "Discord context could not be fetched.", Ephemeral: true}
	}
	if strings.TrimSpace(packed.Text) == "" {
		return "", Response{Content: "No Discord context was available.", Ephemeral: true}
	}
	r.admin.RecordSensitiveReadAudit(ctx, request.GuildID, request.UserID, targetType, targetID, map[string]string{
		"command":      request.Command,
		"source_count": strconv.Itoa(len(packed.Citations)),
	})
	return packed.Text, Response{}
}

func (r *Router) attachmentInput(ctx context.Context, request Request, rawID string) (string, Response) {
	if r.attachments == nil {
		return "", Response{Content: "Attachment lookup is not configured for this runtime.", Ephemeral: true}
	}
	if denied := r.ensureAttachmentsAllowed(ctx, request); denied.Content != "" {
		return "", denied
	}
	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || id == 0 {
		return "", Response{Content: "Provide a numeric `attachment_id`.", Ephemeral: true}
	}
	attachment, err := r.attachments.Get(ctx, request.GuildID, uint(id))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", Response{Content: "No matching extracted attachment was found.", Ephemeral: true}
		}
		return "", Response{Content: "Attachment lookup failed.", Ephemeral: true}
	}
	if strings.TrimSpace(attachment.ExtractedText) == "" {
		return "", Response{Content: "That attachment does not have extracted text.", Ephemeral: true}
	}
	r.admin.RecordSensitiveReadAudit(ctx, request.GuildID, request.UserID, "attachment", strconv.FormatUint(uint64(attachment.ID), 10), map[string]string{
		"command": request.Command,
	})
	return fmt.Sprintf("Extracted attachment `%s` (id %d):\n\n%s", attachment.Filename, attachment.ID, attachment.ExtractedText), Response{}
}

func (r *Router) ensureAssistantAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanUseAssistant(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to use Panda here.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureThreadsAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanUseThreads(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to use Panda thread mode.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureAttachmentsAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanUseAttachments(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to use Panda attachment context.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureMemoryReadAllowed(ctx context.Context, request Request) Response {
	allowed, err := r.admin.CanReadMemory(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: "You do not have permission to search server knowledge.", Ephemeral: true}
	}
	return Response{}
}

func (r *Router) ensureGuildControl(ctx context.Context, request Request, denial string) Response {
	allowed, err := r.admin.HasGuildControl(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: denial, Ephemeral: true}
	}
	return Response{}
}

func assistantAccessRequest(request Request) admin.AssistantAccessRequest {
	return admin.AssistantAccessRequest{
		GuildID:      request.GuildID,
		ChannelID:    request.ChannelID,
		UserID:       request.UserID,
		RoleIDs:      request.RoleIDs,
		IsGuildAdmin: request.IsGuildAdmin,
		IsOwner:      request.IsOwner,
	}
}

type toolFilter struct {
	allowed                 map[string]struct{}
	restricted              map[string]struct{}
	requireExplicitComposed bool
}

func (r *Router) toolFilter(ctx context.Context, request Request) toolFilter {
	if r.admin == nil || request.GuildID == "" {
		return toolFilter{}
	}
	accessRequest := assistantAccessRequest(request)
	hasControl, err := r.admin.HasGuildControl(ctx, accessRequest)
	if err != nil || hasControl {
		return toolFilter{}
	}
	roles, err := r.admin.ToolRoleAccess(ctx, request.GuildID, request.RoleIDs)
	if err != nil {
		return toolFilter{allowed: map[string]struct{}{}, restricted: map[string]struct{}{}, requireExplicitComposed: true}
	}
	return toolFilter{
		allowed:                 namesToSet(roles.AllowedTools),
		restricted:              namesToSet(roles.RestrictedTools),
		requireExplicitComposed: true,
	}
}

func (r *Router) toolAccess(ctx context.Context, request Request, policy string) toolsvc.ToolAccess {
	filter := r.toolFilter(ctx, request)
	return toolsvc.ToolAccess{
		Policy:                       policy,
		Permissions:                  r.allowedToolPermissions(ctx, request),
		AllowedTools:                 filter.allowed,
		RestrictedTools:              filter.restricted,
		RequireExplicitComposedTools: filter.requireExplicitComposed,
	}
}

func (r *Router) allowedToolPermissions(ctx context.Context, request Request) map[string]struct{} {
	if r.admin != nil {
		hasControl, err := r.admin.HasGuildControl(ctx, assistantAccessRequest(request))
		if err == nil && hasControl {
			permissions := permissionsFromNames(admin.GuildControlPermissionNames())
			if request.IsOwner {
				permissions[admin.PermissionOwnerOps] = struct{}{}
			}
			return permissions
		}
	}
	permissions := map[string]struct{}{}
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantUse, r.admin.CanUseAssistant)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantUseThreads, r.admin.CanUseThreads)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantAttachments, r.admin.CanUseAttachments)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantMemoryRead, r.admin.CanReadMemory)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantWebSearch, r.admin.CanUseWebSearch)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAssistantSoulWrite, r.admin.CanWriteSoul)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionModerationUse, r.admin.CanUseModeration)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminConfigRead, r.admin.CanReadConfig)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminConfigWrite, r.admin.CanWriteConfig)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminUsageRead, r.admin.CanReadUsage)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminAuditRead, r.admin.CanReadAudit)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionAdminMemoryManage, r.admin.CanManageMemory)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeDraft, r.admin.CanDraftComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeApprove, r.admin.CanApproveComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeInvoke, r.admin.CanInvokeComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionToolComposeAudit, r.admin.CanAuditComposedTool)
	r.addPermissionIfAllowed(ctx, request, permissions, admin.PermissionOwnerOps, r.admin.CanUseOwnerOps)
	if _, ok := permissions[admin.PermissionToolComposeInvoke]; !ok {
		filter := r.toolFilter(ctx, request)
		if filter.requireExplicitComposed && len(filter.allowed) > 0 {
			permissions[admin.PermissionToolComposeInvoke] = struct{}{}
		}
	}
	return permissions
}

func namesToSet(names []string) map[string]struct{} {
	values := map[string]struct{}{}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			values[name] = struct{}{}
		}
	}
	return values
}

func (r *Router) addPermissionIfAllowed(ctx context.Context, request Request, permissions map[string]struct{}, permission string, check func(context.Context, admin.AssistantAccessRequest) (bool, error)) {
	allowed, err := check(ctx, assistantAccessRequest(request))
	if err == nil && allowed {
		permissions[permission] = struct{}{}
	}
}

func permissionNames(permissions map[string]struct{}) []string {
	if len(permissions) == 0 {
		return nil
	}
	names := make([]string, 0, len(permissions))
	for permission := range permissions {
		names = append(names, permission)
	}
	sort.Strings(names)
	return names
}

func permissionsFromNames(names []string) map[string]struct{} {
	permissions := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			permissions[name] = struct{}{}
		}
	}
	return permissions
}

func (r *Router) ensureBudgetAvailable(ctx context.Context, request Request) Response {
	denial, denied, err := r.admin.ConsumeBudget(ctx, repository.BudgetCheckRequest{
		GuildID:   request.GuildID,
		UserID:    request.UserID,
		ChannelID: request.ChannelID,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		return Response{Content: "Budget lookup failed. Please try again later.", Ephemeral: true}
	}
	if denied {
		return Response{Content: fmt.Sprintf("This `%s` budget is exhausted. Try again in %s.", denial.Scope, denial.RetryAfter.Round(time.Second)), Ephemeral: true}
	}
	return Response{}
}

func (r *Router) allowUser(userID string) Response {
	if ok, retryAfter := r.rateLimit.Allow(userID); !ok {
		return Response{Content: fmt.Sprintf("You are sending requests too quickly. Try again in %s.", retryAfter.Round(time.Second)), Ephemeral: true}
	}
	return Response{}
}

func assistantError(err error) Response {
	switch {
	case errors.Is(err, llm.ErrNotConfigured):
		return Response{Content: "I cannot answer yet because `OPENROUTER_API_KEY` is not configured.", Ephemeral: true, Presentation: Presentation{Title: "Assistant not configured", Accent: AccentWarning}}
	case errors.Is(err, assistant.ErrAssistantDisabled):
		return Response{Content: "Assistant responses are disabled for this server.", Ephemeral: true, Presentation: Presentation{Title: "Assistant disabled", Accent: AccentWarning}}
	default:
		slog.Warn("assistant request failed", slog.Any("err", err))
		return Response{Content: "The model request failed. Please try again later.", Ephemeral: true, Presentation: Presentation{Title: "Model request failed", Accent: AccentDanger}}
	}
}

func responseFromAssistantAnswer(userID string, answer assistant.AskResponse, threadID, threadName string) Response {
	response := Response{
		Content:      answer.Content,
		ThreadID:     threadID,
		ThreadName:   threadName,
		Presentation: Presentation{Title: "Panda replied", Accent: AccentDefault},
	}
	if confirmation := ToolConfirmationFromAssistant(userID, answer.Confirmation); confirmation != nil {
		response.Confirmation = confirmation
		response.Content = appendConfirmationNotice(response.Content, answer.Confirmation.Summary)
		response.Presentation = Presentation{Title: "Confirmation required", Accent: AccentWarning}
		if confirmation.Danger {
			response.Presentation.Accent = AccentDanger
		}
	}
	return response
}

func appendConfirmationNotice(content, summary string) string {
	content = strings.TrimSpace(content)
	summary = strings.TrimSpace(summary)
	if summary != "" && !strings.Contains(content, summary) {
		if content != "" {
			content += "\n\n"
		}
		content += summary
	}
	if content != "" {
		content += "\n\n"
	}
	return content + "Press the confirmation button to continue."
}

func (r *Router) ensureToolConfirmationPermission(ctx context.Context, request Request, check func(context.Context, admin.AssistantAccessRequest) (bool, error), denial string) Response {
	allowed, err := check(ctx, assistantAccessRequest(request))
	if err != nil {
		return Response{Content: "Permission lookup failed. Please try again later.", Ephemeral: true}
	}
	if !allowed {
		return Response{Content: denial, Ephemeral: true}
	}
	return Response{}
}

func toolConfirmationError(err error, fallback, notFound string) Response {
	if errors.Is(err, repository.ErrNotFound) {
		return Response{Content: notFound, Ephemeral: true}
	}
	return Response{Content: fallback, Ephemeral: true}
}

func renderAudit(events []store.AuditEvent) string {
	var builder strings.Builder
	builder.WriteString("Recent audit events:\n")
	for _, event := range events {
		fmt.Fprintf(&builder, "- %s by `%s` at %s\n", event.Action, event.ActorID, event.CreatedAt.UTC().Format(time.RFC3339))
	}
	return security.SafeDiscordContent(builder.String())
}

func modelSettingsFromOptions(options map[string]string) (admin.ModelSettings, error) {
	settings := admin.ModelSettings{DefaultModel: strings.TrimSpace(options["model"])}

	if raw, ok := options["fallback_models"]; ok {
		settings.FallbackModelsSet = true
		settings.FallbackModels = csvValues(raw)
	}

	if raw := strings.TrimSpace(options["temperature"]); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return admin.ModelSettings{}, fmt.Errorf("Provide a numeric `temperature` between 0 and 2.")
		}
		settings.Temperature = value
		settings.TemperatureSet = true
	}

	if raw := firstNonEmpty(options["max_response_tokens"], options["max_tokens"]); strings.TrimSpace(raw) != "" {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return admin.ModelSettings{}, fmt.Errorf("Provide a numeric `max_response_tokens` value.")
		}
		settings.MaxResponseTokens = value
		settings.MaxResponseTokensSet = true
	}

	if raw, ok := options["tool_policy"]; ok {
		settings.ToolPolicy = strings.TrimSpace(raw)
		settings.ToolPolicySet = true
	}

	return settings, nil
}

func renderModelSettingsDryRun(settings admin.ModelSettings) string {
	var parts []string
	if strings.TrimSpace(settings.DefaultModel) != "" {
		parts = append(parts, fmt.Sprintf("default `%s`", settings.DefaultModel))
	}
	if settings.FallbackModelsSet {
		parts = append(parts, fmt.Sprintf("%d fallback model(s)", len(settings.FallbackModels)))
	}
	if settings.TemperatureSet {
		parts = append(parts, fmt.Sprintf("temperature %.2f", settings.Temperature))
	}
	if settings.MaxResponseTokensSet {
		parts = append(parts, fmt.Sprintf("max response %d tokens", settings.MaxResponseTokens))
	}
	if settings.ToolPolicySet {
		parts = append(parts, fmt.Sprintf("tool policy `%s`", settings.ToolPolicy))
	}
	if len(parts) == 0 {
		return "No model setting changes were provided."
	}
	return strings.Join(parts, ", ") + "."
}

func dryRunRequested(request Request) bool {
	return truthyOption(request.Options["dry_run"])
}

func truthyOption(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "y":
		return true
	default:
		return false
	}
}

func dryRunResponse(format string, args ...any) Response {
	return Response{Content: "Dry run: " + fmt.Sprintf(format, args...), Ephemeral: true, Presentation: Presentation{Title: "Dry run", Accent: AccentInfo}}
}

func validBudgetScope(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case repository.BudgetScopeGlobal, repository.BudgetScopeGuild, repository.BudgetScopeUser, repository.BudgetScopeChannel:
		return true
	default:
		return false
	}
}

func csvValues(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func fallbackModelCount(value string) int {
	var models []string
	if err := json.Unmarshal([]byte(value), &models); err != nil {
		return 0
	}
	return len(models)
}

func hasContextOptions(options map[string]string) bool {
	return strings.TrimSpace(options["message_id"]) != "" || strings.TrimSpace(options["recent_limit"]) != ""
}

func intOption(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func shouldBackgroundTask(request Request, input string) bool {
	if strings.ToLower(strings.TrimSpace(request.Command)) != "summarize" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(request.Options["_async"])) != "true" {
		return false
	}
	if len(input) >= 3000 {
		return true
	}
	return intOption(request.Options["recent_limit"], 0) >= 25
}

func chatThreadTitle(question string) string {
	title := strings.TrimSpace(question)
	if title == "" {
		return "Panda chat"
	}
	title = strings.ReplaceAll(title, "\n", " ")
	if len(title) > 72 {
		title = textutil.Truncate(title, 72, "")
	}
	return "Panda: " + title
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
