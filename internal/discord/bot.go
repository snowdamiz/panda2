package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/attachments"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/config"
	contextsvc "github.com/sn0w/panda2/internal/context"
	"github.com/sn0w/panda2/internal/music"
	"github.com/sn0w/panda2/internal/polls"
	"github.com/sn0w/panda2/internal/store"
)

type Bot struct {
	cfg         config.Config
	router      *commands.Router
	logger      *slog.Logger
	client      *bot.Client
	jobs        InteractionJobQueue
	context     *contextsvc.Service
	attachments AttachmentRecorder
	events      DiscordEventRecorder
	httpClient  *http.Client
	music       *music.Manager
	closeOnce   sync.Once
}

type AttachmentRecorder interface {
	Record(ctx context.Context, attachment store.Attachment) (store.Attachment, error)
}

type DiscordEventRecorder interface {
	Record(ctx context.Context, event store.DiscordEvent) (store.DiscordEvent, error)
}

type InteractionJobQueue interface {
	Enqueue(ctx context.Context, job store.Job) (store.Job, error)
}

type typingSender interface {
	SendTyping(channelID snowflake.ID, opts ...rest.RequestOpt) error
}

type guildGetter interface {
	GetGuild(guildID snowflake.ID, withCounts bool, opts ...rest.RequestOpt) (*disgoDiscord.RestGuild, error)
}

type commandSyncer interface {
	SetGlobalCommands(applicationID snowflake.ID, commands []disgoDiscord.ApplicationCommandCreate, opts ...rest.RequestOpt) ([]disgoDiscord.ApplicationCommand, error)
	SetGuildCommands(applicationID snowflake.ID, guildID snowflake.ID, commands []disgoDiscord.ApplicationCommandCreate, opts ...rest.RequestOpt) ([]disgoDiscord.ApplicationCommand, error)
}

const maxAttachmentExtractBytes = 1 << 20
const InteractionJobKind = "discord.interaction"
const deferredProgressInterval = 8 * time.Second
const typingRefreshInterval = 5 * time.Second
const discordContentLimit = 2000
const discordEmbedDescriptionLimit = 4096
const discordEmbedFieldNameLimit = 256
const discordEmbedFieldValueLimit = 1024

const (
	pandaEmbedColor   = 0xff6fae
	infoEmbedColor    = 0x5865f2
	successEmbedColor = 0x57f287
	warningEmbedColor = 0xfee75c
	dangerEmbedColor  = 0xed4245
	musicEmbedColor   = 0xff66a8
)

type interactionJobPayload struct {
	ApplicationID string                  `json:"application_id"`
	Token         string                  `json:"token"`
	Task          commands.BackgroundTask `json:"task"`
}

func New(cfg config.Config, router *commands.Router, logger *slog.Logger) (*Bot, error) {
	if !cfg.DiscordConfigured() {
		return &Bot{cfg: cfg, router: router, logger: logger}, nil
	}

	instance := &Bot{cfg: cfg, router: router, logger: logger}
	daveSessions := newDaveSessionFactory(logger)
	client, err := disgo.New(cfg.DiscordBotToken,
		bot.WithGatewayConfigOpts(gateway.WithIntents(
			gateway.IntentsNonPrivileged.Remove(gateway.IntentDirectMessageReactions, gateway.IntentDirectMessageTyping, gateway.IntentDirectMessagePolls),
			gateway.IntentGuildMembers,
			gateway.IntentMessageContent,
		)),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagVoiceStates)),
		bot.WithVoiceManagerConfigOpts(
			voice.WithDaveSessionCreateFunc(daveSessions.New),
			voice.WithDaveSessionLogger(logger),
		),
		bot.WithEventListenerFunc(instance.onApplicationCommand),
		bot.WithEventListenerFunc(instance.onComponentInteraction),
		bot.WithEventListenerFunc(instance.onModalSubmit),
		bot.WithEventListenerFunc(instance.onMessageCreate),
		bot.WithEventListenerFunc(instance.onGuildMessageUpdate),
		bot.WithEventListenerFunc(instance.onGuildMessageDelete),
		bot.WithEventListenerFunc(instance.onGuildMessageReactionAdd),
		bot.WithEventListenerFunc(instance.onGuildMessageReactionRemove),
		bot.WithEventListenerFunc(instance.onGuildMessageReactionRemoveAll),
		bot.WithEventListenerFunc(instance.onGuildMessageReactionRemoveEmoji),
		bot.WithEventListenerFunc(instance.onGuildMessagePollVoteAdd),
		bot.WithEventListenerFunc(instance.onGuildMessagePollVoteRemove),
		bot.WithEventListenerFunc(instance.onGuildChannelCreate),
		bot.WithEventListenerFunc(instance.onGuildChannelUpdate),
		bot.WithEventListenerFunc(instance.onGuildChannelDelete),
		bot.WithEventListenerFunc(instance.onGuildChannelPinsUpdate),
		bot.WithEventListenerFunc(instance.onThreadCreate),
		bot.WithEventListenerFunc(instance.onThreadUpdate),
		bot.WithEventListenerFunc(instance.onThreadDelete),
		bot.WithEventListenerFunc(instance.onThreadMemberUpdate),
		bot.WithEventListenerFunc(instance.onGuildMemberJoin),
		bot.WithEventListenerFunc(instance.onGuildMemberUpdate),
		bot.WithEventListenerFunc(instance.onRoleCreate),
		bot.WithEventListenerFunc(instance.onRoleUpdate),
		bot.WithEventListenerFunc(instance.onRoleDelete),
		bot.WithEventListenerFunc(instance.onGuildBan),
		bot.WithEventListenerFunc(instance.onGuildUnban),
		bot.WithEventListenerFunc(instance.onInviteCreate),
		bot.WithEventListenerFunc(instance.onInviteDelete),
		bot.WithEventListenerFunc(instance.onWebhooksUpdate),
		bot.WithEventListenerFunc(instance.onAutoModerationRuleCreate),
		bot.WithEventListenerFunc(instance.onAutoModerationRuleUpdate),
		bot.WithEventListenerFunc(instance.onAutoModerationRuleDelete),
		bot.WithEventListenerFunc(instance.onAutoModerationActionExecution),
		bot.WithEventListenerFunc(instance.onGuildScheduledEventCreate),
		bot.WithEventListenerFunc(instance.onGuildScheduledEventUpdate),
		bot.WithEventListenerFunc(instance.onGuildScheduledEventDelete),
		bot.WithEventListenerFunc(instance.onGuildScheduledEventUserAdd),
		bot.WithEventListenerFunc(instance.onGuildScheduledEventUserRemove),
		bot.WithEventListenerFunc(instance.onGuildVoiceStateUpdate),
		bot.WithEventListenerFunc(instance.onVoiceServerUpdate),
	)
	if err != nil {
		return nil, err
	}
	instance.client = client
	instance.context = contextsvc.NewService(NewContextProvider(client.Rest))
	sidecars := music.NewSidecarManager(music.SidecarConfig{
		Dir:        cfg.MusicSidecarDir,
		YTDLPPath:  cfg.MusicYTDLPPath,
		FFmpegPath: cfg.MusicFFmpegPath,
		Logger:     logger,
	})
	ytdlp := music.NewYTDLP(music.YTDLPConfig{
		YTDLPPath:  cfg.MusicYTDLPPath,
		FFmpegPath: cfg.MusicFFmpegPath,
		Logger:     logger,
		Sidecars:   sidecars,
	})
	go func() {
		if _, err := sidecars.Ensure(context.Background()); err != nil && logger != nil {
			logger.Warn("music sidecar provisioning failed", slog.Any("err", err))
		}
	}()
	instance.music = music.NewManager(ytdlp, ytdlp, newMusicVoiceConnector(client, logger, daveSessions), logger)
	router.WithContextService(instance.context)
	router.WithThreadManager(NewThreadManager(client.Rest))
	router.WithMemberRoleManager(NewMemberRoleManager(client.Rest))
	router.WithDiscordRoleManager(NewRoleManager(client.Rest))
	router.WithMusicService(instance.music)
	return instance, nil
}

func (b *Bot) WithAttachmentRecorder(recorder AttachmentRecorder) *Bot {
	b.attachments = recorder
	if b.httpClient == nil {
		b.httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return b
}

func (b *Bot) WithDiscordEventRecorder(recorder DiscordEventRecorder) *Bot {
	b.events = recorder
	return b
}

func (b *Bot) WithJobQueue(jobs InteractionJobQueue) *Bot {
	b.jobs = jobs
	return b
}

func (b *Bot) ContextService() *contextsvc.Service {
	return b.context
}

func (b *Bot) Start(ctx context.Context) error {
	if b.client == nil {
		b.logger.Info("discord gateway skipped; credentials are not configured")
		return nil
	}
	if err := b.registerCommands(); err != nil {
		if !b.canContinueAfterCommandRegistrationError(err) {
			return err
		}
		b.logger.Warn("discord command registration skipped",
			slog.String("err", err.Error()),
			slog.String("environment", b.cfg.Environment),
			slog.String("guild_id", b.cfg.DiscordGuildID),
			slog.String("hint", "install the app in this guild or clear DISCORD_GUILD_ID to skip guild-scoped command sync"),
		)
	}
	if err := b.client.OpenGateway(ctx); err != nil {
		return err
	}
	b.logger.Info("discord gateway connected")
	return nil
}

func (b *Bot) Close(ctx context.Context) {
	b.closeOnce.Do(func() {
		if b.music != nil {
			b.music.Close(ctx)
		}
		if b.client != nil {
			b.client.Close(ctx)
		}
	})
}

func (b *Bot) registerCommands() error {
	return syncApplicationCommands(b.client.Rest, b.client.ApplicationID, b.cfg.DiscordGuildID, applicationCommands())
}

func syncApplicationCommands(syncer commandSyncer, applicationID snowflake.ID, guildIDValue string, commands []disgoDiscord.ApplicationCommandCreate) error {
	guildIDValue = strings.TrimSpace(guildIDValue)
	if guildIDValue == "" {
		_, err := syncer.SetGlobalCommands(applicationID, commands)
		if err != nil {
			return fmt.Errorf("set global commands: %w", err)
		}
		return nil
	}

	guildID, err := snowflake.Parse(guildIDValue)
	if err != nil {
		return fmt.Errorf("parse DISCORD_GUILD_ID: %w", err)
	}
	if _, err := syncer.SetGlobalCommands(applicationID, []disgoDiscord.ApplicationCommandCreate{}); err != nil {
		return fmt.Errorf("clear global commands before guild sync: %w", err)
	}
	if _, err := syncer.SetGuildCommands(applicationID, guildID, commands); err != nil {
		return fmt.Errorf("set guild commands: %w", err)
	}
	return nil
}

func (b *Bot) canContinueAfterCommandRegistrationError(err error) bool {
	if strings.EqualFold(b.cfg.Environment, "production") {
		return false
	}
	if b.cfg.DiscordGuildID == "" {
		return false
	}
	return isRecoverableCommandRegistrationError(err)
}

func isRecoverableCommandRegistrationError(err error) bool {
	var restErr *rest.Error
	if !errors.As(err, &restErr) {
		return false
	}
	switch restErr.Code {
	case rest.JSONErrorCodeMissingAccess,
		rest.JSONErrorCodeLackPermissionsToPerformAction,
		rest.JSONErrorCodeMissingRequiredOAuth2Scope,
		rest.JSONErrorCodeUnknownGuild:
		return true
	default:
		return false
	}
}

func applicationCommands() []disgoDiscord.ApplicationCommandCreate {
	minQuestionLength := 1
	maxQuestionLength := 1800
	maxPollQuestionLength := polls.MaxQuestionRunes
	maxPollAnswersLength := polls.MaxAnswers * (polls.MaxAnswerRunes + 3)
	minPollDurationHours := 1
	maxPollDurationHours := polls.MaxDurationHours
	maxConfirmLength := 100
	maxTextLength := 4000
	confirmOption := disgoDiscord.ApplicationCommandOptionString{
		Name:        "confirm",
		Description: "Confirmation token from a pending dangerous action",
		Required:    false,
		MaxLength:   &maxConfirmLength,
	}
	dryRunOption := disgoDiscord.ApplicationCommandOptionBool{
		Name:        "dry_run",
		Description: "Preview the change without saving it",
		Required:    false,
	}
	roleProfileOption := disgoDiscord.ApplicationCommandOptionString{
		Name:        "profile",
		Description: "Panda role profile",
		Required:    false,
		Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
			{Name: "Admin", Value: "admin"},
			{Name: "Moderator", Value: "moderator"},
		},
	}

	return []disgoDiscord.ApplicationCommandCreate{
		disgoDiscord.SlashCommandCreate{
			Name:        "ping",
			Description: "Check whether Panda is responding",
		},
		disgoDiscord.SlashCommandCreate{
			Name:        "help",
			Description: "Show Panda commands",
		},
		disgoDiscord.SlashCommandCreate{
			Name:        "poll",
			Description: "Create a native Discord poll",
			Options: []disgoDiscord.ApplicationCommandOption{
				disgoDiscord.ApplicationCommandOptionString{
					Name:        "question",
					Description: "Poll question",
					Required:    true,
					MinLength:   &minQuestionLength,
					MaxLength:   &maxPollQuestionLength,
				},
				disgoDiscord.ApplicationCommandOptionString{
					Name:        "answers",
					Description: "Answers separated by |, semicolons, or new lines",
					Required:    true,
					MinLength:   &minQuestionLength,
					MaxLength:   &maxPollAnswersLength,
				},
				disgoDiscord.ApplicationCommandOptionInt{
					Name:        "duration_hours",
					Description: "How long voting stays open",
					Required:    false,
					MinValue:    &minPollDurationHours,
					MaxValue:    &maxPollDurationHours,
				},
				disgoDiscord.ApplicationCommandOptionBool{
					Name:        "allow_multiselect",
					Description: "Allow people to vote for multiple answers",
					Required:    false,
				},
			},
		},
		disgoDiscord.SlashCommandCreate{
			Name:        "ops",
			Description: "Owner operations",
			Options: []disgoDiscord.ApplicationCommandOption{
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "health",
					Description: "Check bot operational health",
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "guilds",
					Description: "Show configured guild count",
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "reload",
					Description: "Recheck runtime dependencies",
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "drain",
					Description: "Stop claiming new background jobs",
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "resume",
					Description: "Resume background job processing",
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "incident",
					Description: "Manage incident mode",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Incident action",
							Required:    false,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Enable", Value: "enable"},
								{Name: "Disable", Value: "disable"},
								{Name: "Status", Value: "status"},
							},
						},
					},
				},
			},
		},
		disgoDiscord.MessageCommandCreate{Name: "Explain with Panda"},
		disgoDiscord.MessageCommandCreate{Name: "Summarize with Panda"},
		disgoDiscord.SlashCommandCreate{
			Name:        "admin",
			Description: "Admin commands",
			Options: []disgoDiscord.ApplicationCommandOption{
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "role",
					Description: "Set which Discord roles mean Panda admin or moderator",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Role profile action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "List", Value: "list"},
								{Name: "Set", Value: "set"},
								{Name: "Remove", Value: "remove"},
							},
						},
						roleProfileOption,
						disgoDiscord.ApplicationCommandOptionRole{
							Name:        "role",
							Description: "Role to mark as admin/moderator",
							Required:    false,
						},
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "member-role",
					Description: "Assign or remove a Discord role for a user",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Member role action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Add", Value: "add"},
								{Name: "Remove", Value: "remove"},
							},
						},
						disgoDiscord.ApplicationCommandOptionUser{
							Name:        "user",
							Description: "User to update",
							Required:    true,
						},
						disgoDiscord.ApplicationCommandOptionRole{
							Name:        "role",
							Description: "Role to assign or remove",
							Required:    true,
						},
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "tool",
					Description: "Allow a role to use a specific Panda tool",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Tool access action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "List", Value: "list"},
								{Name: "Add", Value: "add"},
								{Name: "Remove", Value: "remove"},
							},
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "tool_name",
							Description: "Native or composed tool name, such as web.search or welcome_builder",
							Required:    false,
							MaxLength:   &maxConfirmLength,
						},
						disgoDiscord.ApplicationCommandOptionRole{
							Name:        "role",
							Description: "Role to allow or remove",
							Required:    false,
						},
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "channel",
					Description: "Choose which channels can use Panda assistant features",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Channel access action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "List", Value: "list"},
								{Name: "Allow", Value: "allow"},
								{Name: "Deny", Value: "deny"},
								{Name: "Remove", Value: "remove"},
							},
						},
						disgoDiscord.ApplicationCommandOptionChannel{
							Name:        "channel",
							Description: "Channel to allow, deny, or remove",
							Required:    false,
							ChannelTypes: []disgoDiscord.ChannelType{
								disgoDiscord.ChannelTypeGuildText,
								disgoDiscord.ChannelTypeGuildNews,
								disgoDiscord.ChannelTypeGuildForum,
								disgoDiscord.ChannelTypeGuildMedia,
								disgoDiscord.ChannelTypeGuildPublicThread,
								disgoDiscord.ChannelTypeGuildNewsThread,
								disgoDiscord.ChannelTypeGuildPrivateThread,
							},
						},
						dryRunOption,
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "model",
					Description: "Configure OpenRouter model routing",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "model",
							Description: "OpenRouter model slug",
							Required:    false,
							MinLength:   &minQuestionLength,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "fallback_models",
							Description: "Comma-separated fallback model slugs",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "temperature",
							Description: "Model temperature from 0 to 2",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "max_response_tokens",
							Description: "Maximum response tokens",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "tool_policy",
							Description: "Tool policy",
							Required:    false,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Off", Value: "off"},
								{Name: "Read Only", Value: "read_only"},
								{Name: "Assistive", Value: "assistive"},
								{Name: "Admin Only", Value: "admin_only"},
								{Name: "Moderator", Value: "moderator"},
								{Name: "Write Confirmed", Value: "write_confirmed"},
								{Name: "Owner Ops", Value: "owner_ops"},
							},
						},
						dryRunOption,
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "prompt",
					Description: "Set server-level assistant instructions",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "prompt",
							Description: "Instructions to layer onto the system prompt",
							Required:    false,
							MaxLength:   &maxTextLength,
						},
						dryRunOption,
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "soul",
					Description: "Set Panda's response style and personality",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "soul",
							Description: "Personality, style, and response voice",
							Required:    false,
							MaxLength:   &maxTextLength,
						},
						dryRunOption,
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "audit",
					Description: "Show recent privileged actions",
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "enable",
					Description: "Enable assistant responses",
					Options: []disgoDiscord.ApplicationCommandOption{
						dryRunOption,
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "disable",
					Description: "Disable assistant responses",
					Options: []disgoDiscord.ApplicationCommandOption{
						confirmOption,
						dryRunOption,
					},
				},
			},
		},
	}
}

func (b *Bot) onApplicationCommand(event *events.ApplicationCommandInteractionCreate) {
	switch data := event.Data.(type) {
	case disgoDiscord.SlashCommandInteractionData:
		b.handleSlashCommand(event, data)
	case disgoDiscord.MessageCommandInteractionData:
		b.handleMessageCommand(event, data)
	default:
		b.logger.Warn("unsupported application command type", slog.Any("type", event.Data.Type()))
	}
}

func (b *Bot) handleSlashCommand(event *events.ApplicationCommandInteractionCreate, data disgoDiscord.SlashCommandInteractionData) {
	request := commands.Request{
		RequestID:    interactionID(event),
		Command:      data.CommandName(),
		Options:      map[string]string{},
		UserID:       event.User().ID.String(),
		IsOwner:      b.cfg.IsOwner(event.User().ID.String()),
		IsGuildAdmin: b.isGuildAdmin(event),
	}
	if member := event.Member(); member != nil {
		request.RoleIDs = snowflakeStrings(member.RoleIDs)
	}
	if data.SubCommandName != nil {
		request.Subcommand = *data.SubCommandName
	}
	if guildID := event.GuildID(); guildID != nil {
		request.GuildID = guildID.String()
	}
	request.ChannelID = event.Channel().ID().String()
	if question, ok := data.OptString("question"); ok {
		request.Options["question"] = question
	}
	if answers, ok := data.OptString("answers"); ok {
		request.Options["answers"] = answers
	}
	if durationHours, ok := data.OptInt("duration_hours"); ok {
		request.Options["duration_hours"] = strconv.Itoa(durationHours)
	}
	if allowMultiselect, ok := data.OptBool("allow_multiselect"); ok && allowMultiselect {
		request.Options["allow_multiselect"] = "true"
	}
	for _, name := range []string{"model", "fallback_models", "temperature", "max_response_tokens", "max_tokens", "tool_policy", "prompt", "soul", "action", "confirm", "tool_name", "profile"} {
		if value, ok := data.OptString(name); ok {
			request.Options[name] = value
		}
	}
	if role, ok := data.OptRole("role"); ok {
		request.Options["role_id"] = role.ID.String()
		request.Options["role_name"] = role.Name
	}
	if channel, ok := data.OptChannel("channel"); ok {
		request.Options["channel_id"] = channel.ID.String()
		request.Options["channel_name"] = channel.Name
	}
	if user, ok := data.OptUser("user"); ok {
		request.Options["member_user_id"] = user.ID.String()
		request.Options["member_user_name"] = user.Username
	}
	if dryRun, ok := data.OptBool("dry_run"); ok && dryRun {
		request.Options["dry_run"] = "true"
	}
	b.respondToInteraction(event, request)
}

func (b *Bot) handleMessageCommand(event *events.ApplicationCommandInteractionCreate, data disgoDiscord.MessageCommandInteractionData) {
	target := data.TargetMessage()
	request := commands.Request{
		RequestID:    interactionID(event),
		Options:      map[string]string{"text": target.Content},
		UserID:       event.User().ID.String(),
		IsOwner:      b.cfg.IsOwner(event.User().ID.String()),
		IsGuildAdmin: b.isGuildAdmin(event),
		ChannelID:    target.ChannelID.String(),
	}
	if member := event.Member(); member != nil {
		request.RoleIDs = snowflakeStrings(member.RoleIDs)
	}
	if guildID := event.GuildID(); guildID != nil {
		request.GuildID = guildID.String()
	}
	switch data.CommandName() {
	case "Explain with Panda":
		request.Command = "explain"
	case "Summarize with Panda":
		request.Command = "summarize"
	default:
		request.Command = "unknown"
	}
	b.respondToInteraction(event, request)
}

func (b *Bot) respondToInteraction(event *events.ApplicationCommandInteractionCreate, request commands.Request) {
	requestID := interactionID(event)
	if shouldDefer(request.Command) {
		if err := event.DeferCreateMessage(shouldDeferEphemeral(request)); err != nil {
			b.logger.Warn("failed to defer interaction", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
			return
		}
		request = b.prepareDeferredRequest(request)
		response := b.runDeferredInteraction(context.Background(), b.client.ApplicationID, event.Token(), requestID, request)
		if response.Background != nil {
			response = b.queueBackgroundInteraction(context.Background(), b.client.ApplicationID, event.Token(), request.GuildID, *response.Background)
			err := b.updateInteractionResponse(b.client.ApplicationID, event.Token(), response)
			if err != nil {
				b.logger.Warn("failed to update queued interaction response", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
			}
			return
		}
		if request.Command == "chat" && response.ThreadID != "" {
			if b.postThreadResponse(response) {
				_, err := b.client.Rest.UpdateInteractionResponse(
					b.client.ApplicationID,
					event.Token(),
					webhookMessageUpdateFromResponse(threadNoticeResponse(response)),
				)
				if err != nil {
					b.logger.Warn("failed to update thread interaction response", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
				}
				return
			}
		}
		err := b.updateInteractionResponse(b.client.ApplicationID, event.Token(), response)
		if err != nil {
			b.logger.Warn("failed to update interaction response", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
		}
		return
	}

	response := b.router.Handle(context.Background(), request)
	if response.Modal != nil {
		if err := event.Modal(modalCreateFromResponse(response.Modal)); err != nil {
			b.logger.Warn("failed to open modal", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
		}
		return
	}
	chunks := splitDiscordContent(response.Content)
	if err := event.CreateMessage(messageCreateFromResponsePart(response, chunks[0], len(chunks) == 1)); err != nil {
		b.logger.Warn("failed to respond to command", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
		return
	}
	if err := b.createInteractionFollowups(b.client.ApplicationID, event.Token(), response, chunks, 1); err != nil {
		b.logger.Warn("failed to send command followup", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
	}
}

func (b *Bot) runDeferredInteraction(ctx context.Context, applicationID snowflake.ID, token, requestID string, request commands.Request) commands.Response {
	done := make(chan commands.Response, 1)
	go func() {
		done <- b.router.Handle(ctx, request)
	}()

	ticker := time.NewTicker(deferredProgressInterval)
	defer ticker.Stop()
	progressCount := 0
	for {
		select {
		case response := <-done:
			return response
		case <-ticker.C:
			progressCount++
			_, err := b.client.Rest.UpdateInteractionResponse(
				applicationID,
				token,
				webhookMessageUpdateFromResponse(deferredProgressResponse(request.Command, progressCount)),
			)
			if err != nil {
				b.logger.Debug("failed to update deferred progress", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
			}
		case <-ctx.Done():
			return commands.Response{Content: "Request cancelled before Panda could finish.", Ephemeral: true}
		}
	}
}

func deferredProgressContent(command string, count int) string {
	action := "Working"
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "summarize":
		action = "Summarizing"
	case "explain":
		action = "Explaining"
	case "rewrite":
		action = "Rewriting"
	case "translate":
		action = "Translating"
	case "chat":
		action = "Continuing chat"
	case "ask":
		action = "Thinking"
	}
	if count <= 1 {
		return action + "..."
	}
	return fmt.Sprintf("%s... still working", action)
}

func deferredProgressResponse(command string, count int) commands.Response {
	return commands.Response{
		Content: deferredProgressContent(command, count),
		Presentation: commands.Presentation{
			Title:  "Working on it",
			Accent: commands.AccentInfo,
		},
	}
}

func (b *Bot) prepareDeferredRequest(request commands.Request) commands.Request {
	if b.jobs == nil || strings.ToLower(strings.TrimSpace(request.Command)) != "summarize" {
		return request
	}
	if request.Options == nil {
		request.Options = map[string]string{}
	}
	request.Options["_async"] = "true"
	return request
}

func (b *Bot) queueBackgroundInteraction(ctx context.Context, applicationID snowflake.ID, token, guildID string, task commands.BackgroundTask) commands.Response {
	if b.jobs == nil {
		return commands.Response{Content: "Long summary queue is not configured. Please try a smaller request.", Ephemeral: true}
	}
	payload, err := json.Marshal(interactionJobPayload{
		ApplicationID: applicationID.String(),
		Token:         token,
		Task:          task,
	})
	if err != nil {
		return commands.Response{Content: "Long summary could not be queued.", Ephemeral: true}
	}
	job, err := b.jobs.Enqueue(ctx, store.Job{
		Kind:        InteractionJobKind,
		GuildID:     guildID,
		Payload:     string(payload),
		MaxAttempts: 2,
	})
	if err != nil {
		return commands.Response{Content: "Long summary could not be queued.", Ephemeral: true}
	}
	return commands.Response{Content: fmt.Sprintf("Queued long summary job #%d. This response will update when the result is ready.", job.ID), Presentation: commands.Presentation{Title: "Summary queued", Accent: commands.AccentInfo}}
}

func (b *Bot) HandleInteractionJob(ctx context.Context, job store.Job) error {
	if b.client == nil {
		return errors.New("discord client is not configured")
	}
	var payload interactionJobPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return err
	}
	applicationID, err := snowflake.Parse(payload.ApplicationID)
	if err != nil {
		return err
	}
	_, _ = b.client.Rest.UpdateInteractionResponse(
		applicationID,
		payload.Token,
		webhookMessageUpdateFromResponse(commands.Response{Content: fmt.Sprintf("Running long summary job #%d...", job.ID), Presentation: commands.Presentation{Title: "Summary running", Accent: commands.AccentInfo}}),
	)
	response := b.router.HandleBackgroundTask(ctx, payload.Task)
	err = b.updateInteractionResponse(applicationID, payload.Token, response)
	if err != nil {
		b.logger.Warn("failed to update background interaction response", slog.Any("err", err), slog.Uint64("job_id", uint64(job.ID)), slog.String("command", payload.Task.Command))
	}
	return err
}

func interactionID(event *events.ApplicationCommandInteractionCreate) string {
	if event == nil {
		return ""
	}
	return event.ID().String()
}

func (b *Bot) onComponentInteraction(event *events.ComponentInteractionCreate) {
	data := event.ComponentInteraction.Data
	switch data.Type() {
	case disgoDiscord.ComponentTypeButton:
		b.onButtonInteraction(event)
	default:
		return
	}
}

func (b *Bot) onButtonInteraction(event *events.ComponentInteractionCreate) {
	customID := event.ButtonInteractionData().CustomID()
	if customID == commands.ConfirmationCancelID {
		if err := event.UpdateMessage(messageUpdateFromResponse(commands.Response{Content: "Cancelled.", Presentation: commands.Presentation{Title: "Cancelled", Accent: commands.AccentWarning}}).WithComponents()); err != nil {
			b.logger.Warn("failed to cancel confirmation", slog.Any("err", err))
		}
		return
	}

	baseRequest := b.requestFromComponentEvent(event)
	if confirmedToolRequest, ok := commands.RequestFromToolConfirmationID(customID, baseRequest); ok {
		response := b.router.HandleToolConfirmation(context.Background(), confirmedToolRequest)
		if err := event.UpdateMessage(messageUpdateFromResponse(response)); err != nil {
			b.logger.Warn("failed to update tool confirmation response", slog.Any("err", err))
		}
		return
	}

	confirmedRequest, ok := commands.RequestFromConfirmationID(customID, baseRequest)
	if !ok {
		if err := event.CreateMessage(messageCreateFromResponse(commands.Response{Content: "That confirmation is no longer valid for this user.", Ephemeral: true, Presentation: commands.Presentation{Title: "Confirmation expired", Accent: commands.AccentWarning}})); err != nil {
			b.logger.Warn("failed to reject confirmation", slog.Any("err", err))
		}
		return
	}

	response := b.router.Handle(context.Background(), confirmedRequest)
	if err := event.UpdateMessage(messageUpdateFromResponse(response)); err != nil {
		b.logger.Warn("failed to update confirmation response", slog.Any("err", err))
	}
}

func (b *Bot) requestFromComponentEvent(event *events.ComponentInteractionCreate) commands.Request {
	request := commands.Request{
		RequestID:    event.ID().String(),
		Options:      map[string]string{},
		UserID:       event.User().ID.String(),
		IsOwner:      b.cfg.IsOwner(event.User().ID.String()),
		IsGuildAdmin: b.isComponentGuildAdmin(event),
		ChannelID:    event.Channel().ID().String(),
	}
	if member := event.Member(); member != nil {
		request.RoleIDs = snowflakeStrings(member.RoleIDs)
	}
	if guildID := event.GuildID(); guildID != nil {
		request.GuildID = guildID.String()
	}
	return request
}

func (b *Bot) onModalSubmit(event *events.ModalSubmitInteractionCreate) {
	values := map[string]string{}
	for component := range event.Data.AllComponents() {
		if input, ok := component.(disgoDiscord.TextInputComponent); ok {
			values[input.CustomID] = input.Value
		}
	}
	request, ok := commands.RequestFromModalID(event.Data.CustomID, values, b.requestFromModalEvent(event))
	if !ok {
		if err := event.CreateMessage(messageCreateFromResponse(commands.Response{Content: "That modal is no longer valid for this user.", Ephemeral: true, Presentation: commands.Presentation{Title: "Modal expired", Accent: commands.AccentWarning}})); err != nil {
			b.logger.Warn("failed to reject modal submit", slog.Any("err", err))
		}
		return
	}

	response := b.router.Handle(context.Background(), request)
	if err := event.CreateMessage(messageCreateFromResponse(response)); err != nil {
		b.logger.Warn("failed to respond to modal submit", slog.Any("err", err))
	}
}

func (b *Bot) requestFromModalEvent(event *events.ModalSubmitInteractionCreate) commands.Request {
	request := commands.Request{
		RequestID:    event.ID().String(),
		Options:      map[string]string{},
		UserID:       event.User().ID.String(),
		IsOwner:      b.cfg.IsOwner(event.User().ID.String()),
		IsGuildAdmin: b.isModalGuildAdmin(event),
		ChannelID:    event.Channel().ID().String(),
	}
	if member := event.Member(); member != nil {
		request.RoleIDs = snowflakeStrings(member.RoleIDs)
	}
	if guildID := event.GuildID(); guildID != nil {
		request.GuildID = guildID.String()
	}
	return request
}

func messageCreateFromResponse(response commands.Response) disgoDiscord.MessageCreate {
	return messageCreateFromResponsePart(response, firstDiscordContentChunk(response.Content), true)
}

func messageCreateFromResponsePart(response commands.Response, content string, includeComponents bool) disgoDiscord.MessageCreate {
	message := disgoDiscord.NewMessageCreate().WithEphemeral(response.Ephemeral)
	if response.Poll != nil {
		if strings.TrimSpace(content) != "" {
			message = message.WithContent(content)
		}
		if includeComponents {
			message = message.WithComponents(componentsFromResponse(response)...)
		}
		return message.WithPoll(pollCreateFromPoll(*response.Poll))
	}
	if embed, ok := embedFromResponsePart(response, content); ok {
		message = message.WithContent("").WithEmbeds(embed).WithSuppressEmbeds(false)
	} else {
		message = message.WithContent(content).WithSuppressEmbeds(true)
	}
	if includeComponents {
		message = message.WithComponents(componentsFromResponse(response)...)
	}
	return message
}

func channelMessageCreateFromResponse(response commands.Response) disgoDiscord.MessageCreate {
	return channelMessageCreateFromResponsePart(response, firstDiscordContentChunk(response.Content), true)
}

func channelMessageCreateFromResponsePart(response commands.Response, content string, includeComponents bool) disgoDiscord.MessageCreate {
	return channelMessageCreateFromResponsePartWithReference(response, content, includeComponents, nil)
}

func channelMessageCreateFromResponsePartWithReference(response commands.Response, content string, includeComponents bool, reference *disgoDiscord.MessageReference) disgoDiscord.MessageCreate {
	message := disgoDiscord.NewMessageCreate()
	if response.Poll != nil {
		if strings.TrimSpace(content) != "" {
			message = message.WithContent(content)
		}
		if includeComponents {
			message = message.WithComponents(componentsFromResponse(response)...)
		}
		if reference != nil {
			message = message.WithMessageReference(reference).WithAllowedMentions(discordReplyAllowedMentions())
		}
		return message.WithPoll(pollCreateFromPoll(*response.Poll))
	}
	if embed, ok := embedFromResponsePart(response, content); ok {
		message = message.WithContent("").WithEmbeds(embed).WithSuppressEmbeds(false)
	} else {
		message = message.WithContent(content).WithSuppressEmbeds(true)
	}
	if includeComponents {
		message = message.WithComponents(componentsFromResponse(response)...)
	}
	if reference != nil {
		message = message.WithMessageReference(reference).WithAllowedMentions(discordReplyAllowedMentions())
	}
	return message
}

func pollCreateFromPoll(poll polls.Poll) disgoDiscord.PollCreate {
	answers := make([]disgoDiscord.PollMedia, 0, len(poll.Answers))
	for _, answer := range poll.Answers {
		text := answer.Text
		answers = append(answers, disgoDiscord.PollMedia{
			Text:  &text,
			Emoji: partialPollEmoji(answer.Emoji),
		})
	}
	return disgoDiscord.NewPollCreate(poll.Question, answers...).
		WithDuration(poll.DurationHours).
		WithAllowMultiselect(poll.AllowMultiselect)
}

func partialPollEmoji(raw string) *disgoDiscord.PartialEmoji {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if id, ok := customEmojiID(raw); ok {
		return &disgoDiscord.PartialEmoji{ID: &id}
	}
	name := raw
	return &disgoDiscord.PartialEmoji{Name: &name}
}

func customEmojiID(raw string) (snowflake.ID, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if id, err := snowflake.Parse(raw); err == nil {
		return id, true
	}
	if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
		raw = strings.TrimPrefix(strings.TrimSuffix(raw, ">"), "<")
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 {
		return 0, false
	}
	id, err := snowflake.Parse(parts[len(parts)-1])
	return id, err == nil
}

func firstDiscordContentChunk(content string) string {
	chunks := splitDiscordContent(content)
	if len(chunks) == 0 {
		return ""
	}
	return chunks[0]
}

func modalCreateFromResponse(response *commands.Modal) disgoDiscord.ModalCreate {
	modal := disgoDiscord.NewModalCreate(response.ID, response.Title)
	for _, input := range response.Inputs {
		textInput := disgoDiscord.NewShortTextInput(input.ID)
		if input.Paragraph {
			textInput = disgoDiscord.NewParagraphTextInput(input.ID)
		}
		textInput = textInput.WithRequired(input.Required)
		if input.MaxLength > 0 {
			textInput = textInput.WithMaxLength(input.MaxLength)
		}
		if strings.TrimSpace(input.Placeholder) != "" {
			textInput = textInput.WithPlaceholder(input.Placeholder)
		}
		if strings.TrimSpace(input.Value) != "" {
			textInput = textInput.WithValue(input.Value)
		}
		modal = modal.AddLabel(input.Label, textInput)
	}
	return modal
}

func webhookMessageUpdateFromResponse(response commands.Response) disgoDiscord.MessageUpdate {
	return webhookMessageUpdateFromResponsePart(response, firstDiscordContentChunk(response.Content), true)
}

func webhookMessageUpdateFromResponsePart(response commands.Response, content string, includeComponents bool) disgoDiscord.MessageUpdate {
	message := disgoDiscord.NewMessageUpdate()
	if embed, ok := embedFromResponsePart(response, content); ok {
		message = message.WithContent("").WithEmbeds(embed).WithSuppressEmbeds(false)
	} else {
		message = message.WithContent(content).WithEmbeds().WithSuppressEmbeds(true)
	}
	if includeComponents {
		message = message.WithComponents(componentsFromResponse(response)...)
	}
	return message
}

func messageUpdateFromResponse(response commands.Response) disgoDiscord.MessageUpdate {
	return webhookMessageUpdateFromResponse(response)
}

func (b *Bot) updateInteractionResponse(applicationID snowflake.ID, token string, response commands.Response) error {
	chunks := splitDiscordContent(response.Content)
	_, err := b.client.Rest.UpdateInteractionResponse(
		applicationID,
		token,
		webhookMessageUpdateFromResponsePart(response, chunks[0], len(chunks) == 1),
	)
	if err != nil {
		return err
	}
	return b.createInteractionFollowups(applicationID, token, response, chunks, 1)
}

func (b *Bot) createInteractionFollowups(applicationID snowflake.ID, token string, response commands.Response, chunks []string, start int) error {
	for index := start; index < len(chunks); index++ {
		_, err := b.client.Rest.CreateFollowupMessage(
			applicationID,
			token,
			messageCreateFromResponsePart(response, chunks[index], index == len(chunks)-1),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) sendChannelResponse(channelID snowflake.ID, response commands.Response, reference ...*disgoDiscord.MessageReference) error {
	var replyReference *disgoDiscord.MessageReference
	if len(reference) > 0 {
		replyReference = reference[0]
	}
	chunks := splitDiscordContent(response.Content)
	for index, chunk := range chunks {
		chunkReference := replyReference
		if index > 0 {
			chunkReference = nil
		}
		message := channelMessageCreateFromResponsePartWithReference(response, chunk, index == len(chunks)-1, chunkReference)
		if _, err := b.client.Rest.CreateMessage(channelID, message); err != nil {
			return err
		}
	}
	return nil
}

func splitDiscordContent(content string) []string {
	if content == "" {
		return []string{""}
	}
	runes := []rune(content)
	chunks := make([]string, 0, len(runes)/discordContentLimit+1)
	for len(runes) > discordContentLimit {
		splitAt := discordSplitIndex(runes, discordContentLimit)
		chunk := strings.TrimRightFunc(string(runes[:splitAt]), unicode.IsSpace)
		if chunk == "" {
			chunk = string(runes[:discordContentLimit])
			splitAt = discordContentLimit
		}
		chunks = append(chunks, chunk)
		runes = []rune(strings.TrimLeftFunc(string(runes[splitAt:]), unicode.IsSpace))
	}
	if len(runes) > 0 || len(chunks) == 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

func discordSplitIndex(runes []rune, limit int) int {
	if len(runes) <= limit {
		return len(runes)
	}
	minSplit := limit / 2
	for index := limit - 1; index >= minSplit; index-- {
		if runes[index] == '\n' {
			return index + 1
		}
	}
	for index := limit - 1; index >= minSplit; index-- {
		if unicode.IsSpace(runes[index]) {
			return index + 1
		}
	}
	return limit
}

func embedFromResponsePart(response commands.Response, content string) (disgoDiscord.Embed, bool) {
	if !presentationHasExplicitDisplay(response.Presentation) {
		return disgoDiscord.Embed{}, false
	}
	description := strings.TrimSpace(content)
	presentation := responsePresentation(response.Presentation, description)
	title := strings.TrimSpace(presentation.Title)
	description = trimDuplicateMarkdownHeading(description, title)
	if utf8RuneCount(description) > discordEmbedDescriptionLimit {
		description = limitRunes(description, discordEmbedDescriptionLimit)
	}

	fields := embedFieldsFromResponse(presentation.Fields)
	if title == "" && description == "" && len(fields) == 0 {
		return disgoDiscord.Embed{}, false
	}

	embed := disgoDiscord.NewEmbed().WithColor(embedColor(presentation.Accent))
	if title != "" {
		embed = embed.WithTitle(title)
	}
	if description != "" {
		embed = embed.WithDescription(description)
	}
	if validHTTPURL(presentation.URL) {
		embed = embed.WithURL(strings.TrimSpace(presentation.URL))
	}
	if footer := strings.TrimSpace(presentation.Footer); footer != "" {
		embed = embed.WithFooterText(footer)
	}
	if len(fields) > 0 {
		embed = embed.WithFields(fields...)
	}
	return embed, true
}

func responsePresentation(presentation commands.Presentation, content string) commands.Presentation {
	if strings.TrimSpace(presentation.Title) == "" && strings.TrimSpace(content) != "" {
		presentation.Title = "Panda"
	}
	return presentation
}

func presentationHasExplicitDisplay(presentation commands.Presentation) bool {
	return strings.TrimSpace(presentation.Title) != "" ||
		strings.TrimSpace(presentation.URL) != "" ||
		strings.TrimSpace(presentation.Footer) != "" ||
		presentation.Accent != commands.AccentDefault ||
		len(presentation.Fields) > 0
}

func embedFieldsFromResponse(fields []commands.Field) []disgoDiscord.EmbedField {
	embedFields := make([]disgoDiscord.EmbedField, 0, len(fields))
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		value := strings.TrimSpace(field.Value)
		if name == "" || value == "" {
			continue
		}
		inline := field.Inline
		embedFields = append(embedFields, disgoDiscord.EmbedField{
			Name:   limitRunes(name, discordEmbedFieldNameLimit),
			Value:  limitRunes(value, discordEmbedFieldValueLimit),
			Inline: &inline,
		})
	}
	return embedFields
}

func embedColor(accent commands.Accent) int {
	switch accent {
	case commands.AccentInfo:
		return infoEmbedColor
	case commands.AccentSuccess:
		return successEmbedColor
	case commands.AccentWarning:
		return warningEmbedColor
	case commands.AccentDanger:
		return dangerEmbedColor
	case commands.AccentMusic:
		return musicEmbedColor
	default:
		return pandaEmbedColor
	}
}

func trimDuplicateMarkdownHeading(content, title string) string {
	if content == "" || title == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return content
	}
	first := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(first, "#") {
		return content
	}
	heading := strings.TrimSpace(strings.TrimLeft(first, "# "))
	heading = strings.TrimSuffix(heading, ":")
	if !strings.EqualFold(heading, title) {
		return content
	}
	return strings.TrimLeftFunc(strings.Join(lines[1:], "\n"), unicode.IsSpace)
}

func limitRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func utf8RuneCount(value string) int {
	return len([]rune(value))
}

func componentsFromResponse(response commands.Response) []disgoDiscord.LayoutComponent {
	var components []disgoDiscord.LayoutComponent
	if response.Confirmation != nil && strings.TrimSpace(response.Confirmation.ID) != "" {
		confirmation := response.Confirmation
		confirmLabel := firstNonEmptyText(confirmation.ConfirmLabel, "Confirm")
		cancelLabel := firstNonEmptyText(confirmation.CancelLabel, "Cancel")
		cancelID := firstNonEmptyText(confirmation.CancelID, commands.ConfirmationCancelID)
		confirmButton := disgoDiscord.NewSuccessButton(confirmLabel, confirmation.ID)
		if confirmation.Danger {
			confirmButton = disgoDiscord.NewDangerButton(confirmLabel, confirmation.ID)
		}
		components = append(components, disgoDiscord.NewActionRow(
			confirmButton,
			disgoDiscord.NewSecondaryButton(cancelLabel, cancelID),
		))
	}
	if buttons := actionButtonsFromResponse(response.Actions); len(buttons) > 0 {
		components = append(components, disgoDiscord.NewActionRow(buttons...))
	}
	return components
}

func actionButtonsFromResponse(actions []commands.Action) []disgoDiscord.InteractiveComponent {
	buttons := make([]disgoDiscord.InteractiveComponent, 0, len(actions))
	for _, action := range actions {
		label := strings.TrimSpace(action.Label)
		rawURL := strings.TrimSpace(action.URL)
		if label == "" || !validHTTPURL(rawURL) {
			continue
		}
		buttons = append(buttons, disgoDiscord.NewLinkButton(limitRunes(label, 80), rawURL))
		if len(buttons) == 5 {
			break
		}
	}
	return buttons
}

func validHTTPURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (b *Bot) postThreadResponse(response commands.Response) bool {
	threadID, err := snowflake.Parse(response.ThreadID)
	if err != nil {
		return false
	}
	if err := b.sendChannelResponse(threadID, response); err != nil {
		b.logger.Warn("failed to post chat response in thread", slog.Any("err", err), slog.String("thread_id", response.ThreadID))
		return false
	}
	return true
}

func threadNotice(response commands.Response) string {
	name := strings.TrimSpace(response.ThreadName)
	if name == "" {
		return fmt.Sprintf("Continued this chat in <#%s>.", response.ThreadID)
	}
	return fmt.Sprintf("Continued this chat in <#%s> (`%s`).", response.ThreadID, name)
}

func threadNoticeResponse(response commands.Response) commands.Response {
	return commands.Response{
		Content: threadNotice(response),
		Presentation: commands.Presentation{
			Title:  "Continued in thread",
			Accent: commands.AccentInfo,
		},
	}
}

func shouldDefer(command string) bool {
	switch command {
	case "ask", "chat", "summarize", "explain", "rewrite", "translate":
		return true
	default:
		return false
	}
}

func shouldDeferEphemeral(request commands.Request) bool {
	return false
}

func (b *Bot) onMessageCreate(event *events.MessageCreate) {
	if b.client == nil {
		return
	}
	b.recordMessageEvent(context.Background(), "message_create", event.Message)
	if event.Message.Author.Bot {
		return
	}
	b.captureAttachments(context.Background(), event.Message)
	content := strings.TrimSpace(event.Message.Content)
	if content == "" {
		return
	}

	guildID := ""
	if eventGuildID := messageEventGuildID(event); eventGuildID != nil {
		guildID = eventGuildID.String()
	}
	options := map[string]string{"message": content}
	if messageMentionsUser(event.Message, b.client.ID().String()) {
		options["bot_mentioned"] = "true"
	}
	b.addReplyContextOptions(context.Background(), options, event.Message)
	if !shouldHandleNaturalMessage(content, options) {
		return
	}
	request := commands.Request{
		RequestID:      event.Message.ID.String(),
		Options:        options,
		GuildID:        guildID,
		ChannelID:      event.ChannelID.String(),
		VoiceChannelID: b.userVoiceChannelID(context.Background(), guildID, event.Message.Author.ID),
		UserID:         event.Message.Author.ID.String(),
		RoleIDs:        messageRoleIDs(event.Message.Member),
		IsOwner:        b.cfg.IsOwner(event.Message.Author.ID.String()),
		IsGuildAdmin:   b.isMessageGuildOwner(event, event.Message.Author.ID),
	}
	reference := messageReferenceFromMessage(event.Message)
	go b.respondToNaturalMessage(context.Background(), event.ChannelID, reference, request)
}

func (b *Bot) respondToNaturalMessage(ctx context.Context, channelID snowflake.ID, reference *disgoDiscord.MessageReference, request commands.Request) {
	if b == nil || b.client == nil || b.router == nil {
		return
	}
	stopTyping := startTypingIndicator(ctx, b.client.Rest, b.logger, channelID, request.RequestID, typingRefreshInterval)
	defer stopTyping()
	response := b.router.HandleNaturalMessage(ctx, request)
	if !hasChannelResponsePayload(response) {
		return
	}
	if err := b.sendChannelResponse(channelID, response, reference); err != nil {
		b.logger.Warn("failed to reply to natural message", slog.Any("err", err))
	}
}

func hasChannelResponsePayload(response commands.Response) bool {
	return strings.TrimSpace(response.Content) != "" || response.Poll != nil
}

func (b *Bot) userVoiceChannelID(ctx context.Context, guildIDValue string, userID snowflake.ID) string {
	if b.client == nil || strings.TrimSpace(guildIDValue) == "" || userID == 0 {
		return ""
	}
	guildID, err := snowflake.Parse(guildIDValue)
	if err != nil {
		return ""
	}
	state, ok := b.client.Caches.VoiceState(guildID, userID)
	if !ok || state.ChannelID == nil {
		return b.userVoiceChannelIDFromREST(ctx, guildID, userID)
	}
	return state.ChannelID.String()
}

func (b *Bot) userVoiceChannelIDFromREST(ctx context.Context, guildID snowflake.ID, userID snowflake.ID) string {
	if b.client == nil || b.client.Rest == nil {
		return ""
	}
	state, err := b.client.Rest.GetUserVoiceState(guildID, userID, rest.WithCtx(ctx))
	if err != nil || state == nil || state.ChannelID == nil {
		if err != nil && b.logger != nil {
			b.logger.Debug("user voice state lookup failed", slog.Any("err", err), slog.String("guild_id", guildID.String()), slog.String("user_id", userID.String()))
		}
		return ""
	}
	return state.ChannelID.String()
}

func messageReferenceFromMessage(message disgoDiscord.Message) *disgoDiscord.MessageReference {
	if message.ID == 0 || message.ChannelID == 0 {
		return nil
	}
	messageID := message.ID
	channelID := message.ChannelID
	reference := &disgoDiscord.MessageReference{
		MessageID:       &messageID,
		ChannelID:       &channelID,
		FailIfNotExists: false,
	}
	if message.GuildID != nil {
		guildID := *message.GuildID
		reference.GuildID = &guildID
	}
	return reference
}

func discordReplyAllowedMentions() *disgoDiscord.AllowedMentions {
	return &disgoDiscord.AllowedMentions{
		Parse:       []disgoDiscord.AllowedMentionType{disgoDiscord.AllowedMentionTypeUsers, disgoDiscord.AllowedMentionTypeRoles, disgoDiscord.AllowedMentionTypeEveryone},
		Roles:       []snowflake.ID{},
		Users:       []snowflake.ID{},
		RepliedUser: false,
	}
}

func startTypingIndicator(ctx context.Context, sender typingSender, logger *slog.Logger, channelID snowflake.ID, requestID string, interval time.Duration) func() {
	if sender == nil || channelID == 0 {
		return func() {}
	}
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = typingRefreshInterval
	}
	typingCtx, cancel := context.WithCancel(ctx)
	send := func() {
		if err := sender.SendTyping(channelID, rest.WithCtx(typingCtx)); err != nil {
			logger.Debug("failed to send typing indicator", slog.Any("err", err), slog.String("request_id", requestID), slog.String("channel_id", channelID.String()))
		}
	}
	send()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				send()
			case <-typingCtx.Done():
				return
			}
		}
	}()
	return cancel
}

func (b *Bot) addReplyContextOptions(ctx context.Context, options map[string]string, message disgoDiscord.Message) {
	if referenced := message.ReferencedMessage; referenced != nil {
		b.setReplyContextOptions(options, *referenced)
		return
	}
	if message.MessageReference == nil || message.MessageReference.MessageID == nil {
		return
	}
	options["reply_message_id"] = message.MessageReference.MessageID.String()
	if b.client == nil {
		return
	}
	channelID := message.ChannelID
	if message.MessageReference.ChannelID != nil {
		channelID = *message.MessageReference.ChannelID
	}
	referenced, err := b.client.Rest.GetMessage(channelID, *message.MessageReference.MessageID)
	if err != nil {
		if b.logger != nil {
			b.logger.Warn("failed to fetch referenced message for natural reply", slog.Any("err", err), slog.String("channel_id", channelID.String()), slog.String("message_id", message.MessageReference.MessageID.String()))
		}
		return
	}
	b.setReplyContextOptions(options, *referenced)
}

func (b *Bot) setReplyContextOptions(options map[string]string, referenced disgoDiscord.Message) {
	options["reply_text"] = referenced.Content
	options["reply_message_id"] = referenced.ID.String()
	if b.client != nil && referenced.Author.ID == b.client.ID() {
		options["reply_author_is_bot"] = "true"
	}
}

func shouldHandleNaturalMessage(content string, options map[string]string) bool {
	return strings.TrimSpace(content) != "" &&
		(containsPandaWord(content) || truthyDiscordOption(options["bot_mentioned"]) || truthyDiscordOption(options["reply_author_is_bot"]))
}

func truthyDiscordOption(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}

func (b *Bot) captureAttachments(ctx context.Context, message disgoDiscord.Message) {
	if b.attachments == nil || len(message.Attachments) == 0 || message.GuildID == nil {
		return
	}
	logger := b.logger
	if logger == nil {
		logger = slog.Default()
	}
	client := b.httpClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	for _, attachment := range message.Attachments {
		if strings.TrimSpace(attachment.URL) == "" {
			continue
		}
		data, contentType, err := downloadAttachment(ctx, client, attachment)
		if err != nil {
			logger.Debug("attachment download skipped", slog.Any("err", err), slog.String("filename", attachment.Filename))
			continue
		}
		text, err := attachments.ExtractText(attachments.ExtractRequest{
			Filename:    attachment.Filename,
			ContentType: contentType,
			Data:        data,
			MaxBytes:    maxAttachmentExtractBytes,
		})
		if err != nil || strings.TrimSpace(text) == "" {
			logger.Debug("attachment extraction skipped", slog.Any("err", err), slog.String("filename", attachment.Filename))
			continue
		}
		size := int64(attachment.Size)
		if size == 0 {
			size = int64(len(data))
		}
		_, err = b.attachments.Record(ctx, store.Attachment{
			GuildID:       message.GuildID.String(),
			ChannelID:     message.ChannelID.String(),
			MessageID:     message.ID.String(),
			Filename:      attachment.Filename,
			ContentType:   contentType,
			SizeBytes:     size,
			ExtractedText: text,
		})
		if err != nil {
			logger.Warn("failed to record extracted attachment", slog.Any("err", err), slog.String("filename", attachment.Filename))
		}
	}
}

func downloadAttachment(ctx context.Context, client *http.Client, attachment disgoDiscord.Attachment) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.URL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxAttachmentExtractBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxAttachmentExtractBytes {
		return nil, "", attachments.ErrTooLarge
	}
	contentType := attachmentContentType(attachment)
	if strings.TrimSpace(contentType) == "" {
		contentType = resp.Header.Get("Content-Type")
	}
	return data, contentType, nil
}

func attachmentContentType(attachment disgoDiscord.Attachment) string {
	if attachment.ContentType == nil {
		return ""
	}
	return *attachment.ContentType
}

func (b *Bot) isGuildAdmin(event *events.ApplicationCommandInteractionCreate) bool {
	if memberIsGuildAdmin(event.Member()) {
		return true
	}
	guild, ok := event.Guild()
	return b.userOwnsEventGuild(event.User().ID, guild, ok, event.GuildID())
}

func (b *Bot) isComponentGuildAdmin(event *events.ComponentInteractionCreate) bool {
	if memberIsGuildAdmin(event.Member()) {
		return true
	}
	guild, ok := event.Guild()
	return b.userOwnsEventGuild(event.User().ID, guild, ok, event.GuildID())
}

func (b *Bot) isModalGuildAdmin(event *events.ModalSubmitInteractionCreate) bool {
	if memberIsGuildAdmin(event.Member()) {
		return true
	}
	guild, ok := event.Guild()
	return b.userOwnsEventGuild(event.User().ID, guild, ok, event.GuildID())
}

func (b *Bot) isMessageGuildOwner(event *events.MessageCreate, userID snowflake.ID) bool {
	if event == nil {
		return false
	}
	guild, ok := event.Guild()
	return b.userOwnsEventGuild(userID, guild, ok, messageEventGuildID(event))
}

func messageEventGuildID(event *events.MessageCreate) *snowflake.ID {
	if event == nil || event.GenericMessage == nil {
		return nil
	}
	if event.GuildID != nil {
		return event.GuildID
	}
	return event.Message.GuildID
}

func (b *Bot) userOwnsEventGuild(userID snowflake.ID, guild disgoDiscord.Guild, ok bool, guildID *snowflake.ID) bool {
	if userOwnsGuild(userID, guild, ok) {
		return true
	}
	if guildID == nil || b.client == nil || b.client.Rest == nil {
		return false
	}
	return userOwnsGuildFromREST(b.client.Rest, *guildID, userID)
}

func userOwnsGuild(userID snowflake.ID, guild disgoDiscord.Guild, ok bool) bool {
	return ok && userID != 0 && guild.OwnerID == userID
}

func userOwnsGuildFromREST(getter guildGetter, guildID, userID snowflake.ID) bool {
	if getter == nil || guildID == 0 || userID == 0 {
		return false
	}
	guild, err := getter.GetGuild(guildID, false)
	return err == nil && guild != nil && guild.OwnerID == userID
}

func memberIsGuildAdmin(member *disgoDiscord.ResolvedMember) bool {
	if member == nil {
		return false
	}
	return member.Permissions.Has(disgoDiscord.PermissionAdministrator)
}

func messageRoleIDs(member *disgoDiscord.Member) []string {
	if member == nil {
		return nil
	}
	return snowflakeStrings(member.RoleIDs)
}

func messageMentionsUser(message disgoDiscord.Message, userID string) bool {
	for _, user := range message.Mentions {
		if user.ID.String() == userID {
			return true
		}
	}
	return strings.Contains(message.Content, "<@"+userID+">") || strings.Contains(message.Content, "<@!"+userID+">")
}

func containsPandaWord(content string) bool {
	wordStart := -1
	for index, value := range content {
		if isWordRune(value) {
			if wordStart < 0 {
				wordStart = index
			}
			continue
		}
		if wordStart >= 0 && strings.EqualFold(content[wordStart:index], "panda") {
			return true
		}
		wordStart = -1
	}
	return wordStart >= 0 && strings.EqualFold(content[wordStart:], "panda")
}

func isWordRune(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func snowflakeStrings(ids []snowflake.ID) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = append(result, id.String())
	}
	return result
}
