package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/admin"
	"github.com/sn0w/panda2/internal/attachments"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/config"
	contextsvc "github.com/sn0w/panda2/internal/context"
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

const maxAttachmentExtractBytes = 1 << 20
const InteractionJobKind = "discord.interaction"
const deferredProgressInterval = 8 * time.Second

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
	client, err := disgo.New(cfg.DiscordBotToken,
		bot.WithGatewayConfigOpts(gateway.WithIntents(
			gateway.IntentsNonPrivileged.Remove(gateway.IntentDirectMessages, gateway.IntentDirectMessageReactions, gateway.IntentDirectMessageTyping, gateway.IntentDirectMessagePolls),
			gateway.IntentMessageContent,
		)),
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
	)
	if err != nil {
		return nil, err
	}
	instance.client = client
	instance.context = contextsvc.NewService(NewContextProvider(client.Rest))
	router.WithContextService(instance.context)
	router.WithThreadManager(NewThreadManager(client.Rest))
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
		return err
	}
	if err := b.client.OpenGateway(ctx); err != nil {
		return err
	}
	b.logger.Info("discord gateway connected")
	return nil
}

func (b *Bot) Close(ctx context.Context) {
	if b.client != nil {
		b.client.Close(ctx)
	}
}

func (b *Bot) registerCommands() error {
	commands := applicationCommands()
	if b.cfg.DiscordGuildID != "" {
		guildID, err := snowflake.Parse(b.cfg.DiscordGuildID)
		if err != nil {
			return fmt.Errorf("parse DISCORD_GUILD_ID: %w", err)
		}
		_, err = b.client.Rest.SetGuildCommands(b.client.ApplicationID, guildID, commands)
		return err
	}
	_, err := b.client.Rest.SetGlobalCommands(b.client.ApplicationID, commands)
	return err
}

func applicationCommands() []disgoDiscord.ApplicationCommandCreate {
	minQuestionLength := 1
	maxQuestionLength := 1800
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
			Name:        "search-memory",
			Description: "Search saved server knowledge",
			Options: []disgoDiscord.ApplicationCommandOption{
				disgoDiscord.ApplicationCommandOptionString{
					Name:        "query",
					Description: "Search query",
					Required:    true,
					MinLength:   &minQuestionLength,
					MaxLength:   &maxQuestionLength,
				},
			},
		},
		disgoDiscord.SlashCommandCreate{
			Name:        "memory-consent",
			Description: "Manage user-specific memory consent",
			Options: []disgoDiscord.ApplicationCommandOption{
				disgoDiscord.ApplicationCommandOptionString{
					Name:        "action",
					Description: "Consent action",
					Required:    false,
					Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
						{Name: "Status", Value: "status"},
						{Name: "Enable", Value: "enable"},
						{Name: "Disable", Value: "disable"},
					},
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
		disgoDiscord.SlashCommandCreate{
			Name:        "mod",
			Description: "Moderator helper suggestions",
			Options: []disgoDiscord.ApplicationCommandOption{
				moderationSubcommand("triage", "Summarize an abuse or conflict report", maxTextLength),
				moderationSubcommand("note", "Draft a moderator note", maxTextLength),
				moderationSubcommand("slowmode", "Recommend slow-mode settings", maxTextLength),
				moderationSubcommand("cleanup", "Recommend message cleanup steps", maxTextLength),
				moderationHistorySubcommand(maxTextLength),
			},
		},
		disgoDiscord.MessageCommandCreate{Name: "Explain with Panda"},
		disgoDiscord.MessageCommandCreate{Name: "Summarize with Panda"},
		disgoDiscord.SlashCommandCreate{
			Name:        "admin",
			Description: "Admin commands",
			Options: []disgoDiscord.ApplicationCommandOption{
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "setup",
					Description: "Initialize Panda for this server",
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
					Name:        "usage",
					Description: "Show recent usage",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "window",
							Description: "Usage window",
							Required:    false,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Day", Value: "day"},
								{Name: "Week", Value: "week"},
								{Name: "All", Value: "all"},
							},
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "by",
							Description: "Break usage down by this field",
							Required:    false,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Command", Value: "command"},
								{Name: "Model", Value: "model"},
								{Name: "User", Value: "user"},
								{Name: "Channel", Value: "channel"},
							},
						},
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "limits",
					Description: "Configure request budget windows",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Limit action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Set", Value: "set"},
								{Name: "Remove", Value: "remove"},
								{Name: "List", Value: "list"},
							},
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "scope",
							Description: "Limit scope",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Guild", Value: "guild"},
								{Name: "User", Value: "user"},
								{Name: "Channel", Value: "channel"},
								{Name: "Global", Value: "global"},
							},
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "subject_id",
							Description: "User, channel, or guild id",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "limit",
							Description: "Allowed requests per window",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "window",
							Description: "Window duration, like 1h or 24h",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						confirmOption,
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
					Name:        "memory",
					Description: "Manage server knowledge",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Memory action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Enable", Value: "enable"},
								{Name: "Disable", Value: "disable"},
								{Name: "Add", Value: "add"},
								{Name: "Search", Value: "search"},
								{Name: "List", Value: "list"},
								{Name: "Export", Value: "export"},
								{Name: "Delete", Value: "delete"},
							},
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "title",
							Description: "Knowledge document title",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "content",
							Description: "Knowledge document content",
							Required:    false,
							MaxLength:   &maxTextLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "query",
							Description: "Search query",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "document_id",
							Description: "Knowledge document id",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						confirmOption,
						dryRunOption,
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "roles",
					Description: "Manage role permissions",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Role action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Add", Value: "add"},
								{Name: "Choose Permission", Value: "choose"},
								{Name: "Remove", Value: "remove"},
								{Name: "List", Value: "list"},
							},
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "role_id",
							Description: "Discord role id",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "permission",
							Description: "Permission name",
							Required:    false,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Assistant Use", Value: admin.PermissionAssistantUse},
								{Name: "Thread Mode", Value: admin.PermissionAssistantUseThreads},
								{Name: "Attachments", Value: admin.PermissionAssistantAttachments},
								{Name: "Memory Read", Value: admin.PermissionAssistantMemoryRead},
								{Name: "Memory Write", Value: admin.PermissionAssistantMemoryWrite},
								{Name: "Moderation Use", Value: admin.PermissionModerationUse},
								{Name: "Config Read", Value: admin.PermissionAdminConfigRead},
								{Name: "Config Write", Value: admin.PermissionAdminConfigWrite},
								{Name: "Usage Read", Value: admin.PermissionAdminUsageRead},
								{Name: "Audit Read", Value: admin.PermissionAdminAuditRead},
								{Name: "Memory Manage", Value: admin.PermissionAdminMemoryManage},
							},
						},
						confirmOption,
						dryRunOption,
					},
				},
				disgoDiscord.ApplicationCommandOptionSubCommand{
					Name:        "channels",
					Description: "Manage channel allow and deny rules",
					Options: []disgoDiscord.ApplicationCommandOption{
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "action",
							Description: "Channel action",
							Required:    true,
							Choices: []disgoDiscord.ApplicationCommandOptionChoiceString{
								{Name: "Allow", Value: "allow"},
								{Name: "Deny", Value: "deny"},
								{Name: "Remove", Value: "remove"},
								{Name: "List", Value: "list"},
							},
						},
						disgoDiscord.ApplicationCommandOptionString{
							Name:        "channel_id",
							Description: "Discord channel id",
							Required:    false,
							MaxLength:   &maxQuestionLength,
						},
						confirmOption,
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

func moderationSubcommand(name, description string, maxTextLength int) disgoDiscord.ApplicationCommandOptionSubCommand {
	maxQuestionLength := 1800
	return disgoDiscord.ApplicationCommandOptionSubCommand{
		Name:        name,
		Description: description,
		Options: []disgoDiscord.ApplicationCommandOption{
			disgoDiscord.ApplicationCommandOptionString{
				Name:        "text",
				Description: "Moderation context text",
				Required:    true,
				MaxLength:   &maxTextLength,
			},
			disgoDiscord.ApplicationCommandOptionString{
				Name:        "subject_id",
				Description: "Optional subject user id",
				Required:    false,
				MaxLength:   &maxQuestionLength,
			},
			disgoDiscord.ApplicationCommandOptionString{
				Name:        "tone",
				Description: "Optional note tone",
				Required:    false,
				MaxLength:   &maxQuestionLength,
			},
		},
	}
}

func moderationHistorySubcommand(maxTextLength int) disgoDiscord.ApplicationCommandOptionSubCommand {
	maxQuestionLength := 1800
	return disgoDiscord.ApplicationCommandOptionSubCommand{
		Name:        "history",
		Description: "Summarize a user's recent visible channel history",
		Options: []disgoDiscord.ApplicationCommandOption{
			disgoDiscord.ApplicationCommandOptionString{
				Name:        "subject_id",
				Description: "Subject user id",
				Required:    true,
				MaxLength:   &maxQuestionLength,
			},
			disgoDiscord.ApplicationCommandOptionString{
				Name:        "recent_limit",
				Description: "Recent message fetch limit",
				Required:    false,
				MaxLength:   &maxQuestionLength,
			},
			disgoDiscord.ApplicationCommandOptionString{
				Name:        "text",
				Description: "Optional moderator context note",
				Required:    false,
				MaxLength:   &maxTextLength,
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
	for _, name := range []string{"text", "tone", "language", "detail", "message_id", "attachment_id", "recent_limit", "model", "fallback_models", "temperature", "max_response_tokens", "max_tokens", "tool_policy", "window", "by", "prompt", "action", "title", "content", "query", "document_id", "role_id", "permission", "channel_id", "scope", "subject_id", "limit", "confirm"} {
		if value, ok := data.OptString(name); ok {
			request.Options[name] = value
		}
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
			_, err := b.client.Rest.UpdateInteractionResponse(
				b.client.ApplicationID,
				event.Token(),
				webhookMessageUpdateFromResponse(response),
			)
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
					disgoDiscord.NewMessageUpdate().WithContent(threadNotice(response)),
				)
				if err != nil {
					b.logger.Warn("failed to update thread interaction response", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
				}
				return
			}
		}
		_, err := b.client.Rest.UpdateInteractionResponse(
			b.client.ApplicationID,
			event.Token(),
			webhookMessageUpdateFromResponse(response),
		)
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
	if err := event.CreateMessage(messageCreateFromResponse(response)); err != nil {
		b.logger.Warn("failed to respond to command", slog.Any("err", err), slog.String("request_id", requestID), slog.String("command", request.Command))
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
				disgoDiscord.NewMessageUpdate().WithContent(deferredProgressContent(request.Command, progressCount)),
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
	case "mod":
		action = "Preparing moderator helper output"
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
	return commands.Response{Content: fmt.Sprintf("Queued long summary job #%d. This response will update when the result is ready.", job.ID)}
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
		disgoDiscord.NewMessageUpdate().WithContent(fmt.Sprintf("Running long summary job #%d...", job.ID)),
	)
	response := b.router.HandleBackgroundTask(ctx, payload.Task)
	_, err = b.client.Rest.UpdateInteractionResponse(
		applicationID,
		payload.Token,
		webhookMessageUpdateFromResponse(response),
	)
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
	case disgoDiscord.ComponentTypeStringSelectMenu:
		b.onStringSelectInteraction(event)
	default:
		return
	}
}

func (b *Bot) onButtonInteraction(event *events.ComponentInteractionCreate) {
	customID := event.ButtonInteractionData().CustomID()
	if customID == commands.ConfirmationCancelID {
		if err := event.UpdateMessage(disgoDiscord.NewMessageUpdate().WithContent("Cancelled.").WithComponents()); err != nil {
			b.logger.Warn("failed to cancel confirmation", slog.Any("err", err))
		}
		return
	}

	confirmedRequest, ok := commands.RequestFromConfirmationID(customID, b.requestFromComponentEvent(event))
	if !ok {
		if err := event.CreateMessage(disgoDiscord.NewMessageCreate().WithContent("That confirmation is no longer valid for this user.").WithEphemeral(true)); err != nil {
			b.logger.Warn("failed to reject confirmation", slog.Any("err", err))
		}
		return
	}

	response := b.router.Handle(context.Background(), confirmedRequest)
	if err := event.UpdateMessage(messageUpdateFromResponse(response)); err != nil {
		b.logger.Warn("failed to update confirmation response", slog.Any("err", err))
	}
}

func (b *Bot) onStringSelectInteraction(event *events.ComponentInteractionCreate) {
	data := event.StringSelectMenuInteractionData()
	request, ok := commands.RequestFromSelectID(data.CustomID(), data.Values, b.requestFromComponentEvent(event))
	if !ok {
		if err := event.CreateMessage(disgoDiscord.NewMessageCreate().WithContent("That selection is no longer valid for this user.").WithEphemeral(true)); err != nil {
			b.logger.Warn("failed to reject select interaction", slog.Any("err", err))
		}
		return
	}

	response := b.router.Handle(context.Background(), request)
	if err := event.UpdateMessage(messageUpdateFromResponse(response)); err != nil {
		b.logger.Warn("failed to update select response", slog.Any("err", err))
	}
}

func (b *Bot) requestFromComponentEvent(event *events.ComponentInteractionCreate) commands.Request {
	request := commands.Request{
		RequestID:    event.ID().String(),
		Options:      map[string]string{},
		UserID:       event.User().ID.String(),
		IsOwner:      b.cfg.IsOwner(event.User().ID.String()),
		IsGuildAdmin: memberIsGuildAdmin(event.Member()),
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
		if err := event.CreateMessage(disgoDiscord.NewMessageCreate().WithContent("That modal is no longer valid for this user.").WithEphemeral(true)); err != nil {
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
		IsGuildAdmin: memberIsGuildAdmin(event.Member()),
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
	message := disgoDiscord.NewMessageCreate().WithContent(response.Content).WithEphemeral(response.Ephemeral)
	if components := componentsFromResponse(response); len(components) > 0 {
		message = message.WithComponents(components...)
	}
	return message
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
	message := disgoDiscord.NewMessageUpdate().WithContent(response.Content)
	if components := componentsFromResponse(response); len(components) > 0 {
		message = message.WithComponents(components...)
	}
	return message
}

func messageUpdateFromResponse(response commands.Response) disgoDiscord.MessageUpdate {
	return disgoDiscord.NewMessageUpdate().WithContent(response.Content).WithComponents(componentsFromResponse(response)...)
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
	if response.Select != nil && strings.TrimSpace(response.Select.ID) != "" && len(response.Select.Options) > 0 {
		components = append(components, disgoDiscord.NewActionRow(selectMenuFromResponse(response.Select)))
	}
	return components
}

func selectMenuFromResponse(selectMenu *commands.Select) disgoDiscord.StringSelectMenuComponent {
	options := make([]disgoDiscord.StringSelectMenuOption, 0, len(selectMenu.Options))
	for _, option := range selectMenu.Options {
		item := disgoDiscord.NewStringSelectMenuOption(option.Label, option.Value)
		if strings.TrimSpace(option.Description) != "" {
			item = item.WithDescription(option.Description)
		}
		options = append(options, item)
	}
	return disgoDiscord.NewStringSelectMenu(selectMenu.ID, selectMenu.Placeholder, options...).WithMinValues(1).WithMaxValues(1)
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
	if _, err := b.client.Rest.CreateMessage(threadID, disgoDiscord.NewMessageCreate().WithContent(response.Content)); err != nil {
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

func shouldDefer(command string) bool {
	switch command {
	case "ask", "chat", "summarize", "explain", "rewrite", "translate", "mod":
		return true
	default:
		return false
	}
}

func shouldDeferEphemeral(request commands.Request) bool {
	return strings.ToLower(strings.TrimSpace(request.Command)) == "mod"
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
	if content == "" || !containsPandaWord(content) {
		return
	}

	guildID := ""
	if event.GuildID != nil {
		guildID = event.GuildID.String()
	}
	options := map[string]string{"message": content}
	if messageMentionsUser(event.Message, b.client.ID().String()) {
		options["bot_mentioned"] = "true"
	}
	if referenced := event.Message.ReferencedMessage; referenced != nil {
		options["reply_text"] = referenced.Content
		options["reply_message_id"] = referenced.ID.String()
		if referenced.Author.ID == b.client.ID() {
			options["reply_author_is_bot"] = "true"
		}
	}
	response := b.router.HandleNaturalMessage(context.Background(), commands.Request{
		RequestID: event.Message.ID.String(),
		Options:   options,
		GuildID:   guildID,
		ChannelID: event.ChannelID.String(),
		UserID:    event.Message.Author.ID.String(),
		RoleIDs:   messageRoleIDs(event.Message.Member),
		IsOwner:   b.cfg.IsOwner(event.Message.Author.ID.String()),
	})
	if response.Content == "" {
		return
	}
	_, err := event.Client().Rest.CreateMessage(event.ChannelID, disgoDiscord.NewMessageCreate().WithContent(response.Content))
	if err != nil {
		b.logger.Warn("failed to reply to natural message", slog.Any("err", err))
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
	return memberIsGuildAdmin(event.Member())
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
