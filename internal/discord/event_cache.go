package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/store"
)

var discordEventDrops atomic.Int64

func EventDropCount() int64 {
	return discordEventDrops.Load()
}

func (b *Bot) ToolProvider(events *repository.DiscordEventRepository) *ToolProvider {
	if b == nil || b.client == nil {
		return nil
	}
	return NewToolProvider(b.client.Rest, events, b.client.ID())
}

func (b *Bot) recordDiscordEvent(ctx context.Context, event store.DiscordEvent) {
	if b == nil || b.events == nil || strings.TrimSpace(event.GuildID) == "" {
		return
	}
	if _, err := b.events.Record(ctx, event); err != nil {
		discordEventDrops.Add(1)
		logger := b.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Debug("failed to record discord event", slog.Any("err", err), slog.String("event_type", event.EventType))
	}
}

func (b *Bot) onGuildMessageUpdate(event *events.GuildMessageUpdate) {
	b.recordMessageEvent(context.Background(), "message_update", event.Message)
}

func (b *Bot) onGuildMessageDelete(event *events.GuildMessageDelete) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		MessageID: event.MessageID.String(),
		EventType: "message_delete",
		Summary:   "Message deleted",
		Metadata:  metadataJSON(map[string]string{"message_id": event.MessageID.String()}),
	})
}

func (b *Bot) onGuildMessageReactionAdd(event *events.GuildMessageReactionAdd) {
	b.recordReactionEvent("reaction_add", event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.Emoji.Reaction())
}

func (b *Bot) onGuildMessageReactionRemove(event *events.GuildMessageReactionRemove) {
	b.recordReactionEvent("reaction_remove", event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.Emoji.Reaction())
}

func (b *Bot) onGuildMessageReactionRemoveAll(event *events.GuildMessageReactionRemoveAll) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		MessageID: event.MessageID.String(),
		EventType: "reaction_remove_all",
		Summary:   "All reactions removed from message",
	})
}

func (b *Bot) onGuildMessageReactionRemoveEmoji(event *events.GuildMessageReactionRemoveEmoji) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		MessageID: event.MessageID.String(),
		EventType: "reaction_remove_emoji",
		Summary:   "All reactions for one emoji removed from message",
		Metadata:  metadataJSON(map[string]string{"emoji": event.Emoji.Reaction()}),
	})
}

func (b *Bot) onGuildMessagePollVoteAdd(event *events.GuildMessagePollVoteAdd) {
	b.recordPollVoteEvent("poll_vote_add", event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.AnswerID)
}

func (b *Bot) onGuildMessagePollVoteRemove(event *events.GuildMessagePollVoteRemove) {
	b.recordPollVoteEvent("poll_vote_remove", event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.AnswerID)
}

func (b *Bot) onGuildChannelCreate(event *events.GuildChannelCreate) {
	b.recordChannelEvent("channel_create", event.GuildID, event.ChannelID, event.Channel.Name())
}

func (b *Bot) onGuildChannelUpdate(event *events.GuildChannelUpdate) {
	b.recordChannelEvent("channel_update", event.GuildID, event.ChannelID, event.Channel.Name())
}

func (b *Bot) onGuildChannelDelete(event *events.GuildChannelDelete) {
	b.recordChannelEvent("channel_delete", event.GuildID, event.ChannelID, event.Channel.Name())
}

func (b *Bot) onGuildChannelPinsUpdate(event *events.GuildChannelPinsUpdate) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		EventType: "channel_pins_update",
		Summary:   "Channel pins updated",
		Metadata: metadataJSON(map[string]string{
			"last_pin_at": timePtrValue(event.NewLastPinTimestamp),
		}),
	})
}

func (b *Bot) onThreadCreate(event *events.ThreadCreate) {
	b.recordThreadEvent("thread_create", event.GuildID, event.ThreadID, event.Thread.Name())
}

func (b *Bot) onThreadUpdate(event *events.ThreadUpdate) {
	b.recordThreadEvent("thread_update", event.GuildID, event.ThreadID, event.Thread.Name())
}

func (b *Bot) onThreadDelete(event *events.ThreadDelete) {
	b.recordThreadEvent("thread_delete", event.GuildID, event.ThreadID, event.Thread.Name())
}

func (b *Bot) onThreadMemberUpdate(event *events.ThreadMemberUpdate) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ThreadID.String(),
		UserID:    event.ThreadMemberID.String(),
		EventType: "thread_member_update",
		Summary:   "Thread member updated",
	})
}

func (b *Bot) onRoleCreate(event *events.RoleCreate) {
	b.recordRoleEvent("role_create", event.GuildID, event.RoleID, event.Role.Name)
}

func (b *Bot) onRoleUpdate(event *events.RoleUpdate) {
	b.recordRoleEvent("role_update", event.GuildID, event.RoleID, event.Role.Name)
}

func (b *Bot) onRoleDelete(event *events.RoleDelete) {
	b.recordRoleEvent("role_delete", event.GuildID, event.RoleID, event.Role.Name)
}

func (b *Bot) onGuildBan(event *events.GuildBan) {
	b.recordGuildUserEvent("guild_ban", event.GuildID, event.User.ID, "User banned")
}

func (b *Bot) onGuildUnban(event *events.GuildUnban) {
	b.recordGuildUserEvent("guild_unban", event.GuildID, event.User.ID, "User unbanned")
}

func (b *Bot) onInviteCreate(event *events.InviteCreate) {
	guildID := ""
	if event.GuildID != nil {
		guildID = event.GuildID.String()
	}
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID,
		ChannelID: event.ChannelID.String(),
		EventType: "invite_create",
		Summary:   "Invite created",
		Metadata:  metadataJSON(map[string]string{"code": event.Code}),
	})
}

func (b *Bot) onInviteDelete(event *events.InviteDelete) {
	guildID := ""
	if event.GuildID != nil {
		guildID = event.GuildID.String()
	}
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID,
		ChannelID: event.ChannelID.String(),
		EventType: "invite_delete",
		Summary:   "Invite deleted",
		Metadata:  metadataJSON(map[string]string{"code": event.Code}),
	})
}

func (b *Bot) onWebhooksUpdate(event *events.WebhooksUpdate) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildId.String(),
		ChannelID: event.ChannelID.String(),
		EventType: "webhooks_update",
		Summary:   "Channel webhooks updated",
	})
}

func (b *Bot) onAutoModerationRuleCreate(event *events.AutoModerationRuleCreate) {
	b.recordAutoModerationRuleEvent("auto_moderation_rule_create", event.GuildID, event.ID, event.Name)
}

func (b *Bot) onAutoModerationRuleUpdate(event *events.AutoModerationRuleUpdate) {
	b.recordAutoModerationRuleEvent("auto_moderation_rule_update", event.GuildID, event.ID, event.Name)
}

func (b *Bot) onAutoModerationRuleDelete(event *events.AutoModerationRuleDelete) {
	b.recordAutoModerationRuleEvent("auto_moderation_rule_delete", event.GuildID, event.ID, event.Name)
}

func (b *Bot) onAutoModerationActionExecution(event *events.AutoModerationActionExecution) {
	channelID := ""
	if event.ChannelID != nil {
		channelID = event.ChannelID.String()
	}
	messageID := ""
	if event.MessageID != nil {
		messageID = event.MessageID.String()
	}
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: channelID,
		UserID:    event.UserID.String(),
		MessageID: messageID,
		EventType: "auto_moderation_action",
		Summary:   "Auto moderation action executed",
		Metadata: metadataJSON(map[string]string{
			"rule_id":      event.RuleID.String(),
			"trigger_type": fmt.Sprint(event.RuleTriggerType),
		}),
	})
}

func (b *Bot) onGuildScheduledEventCreate(event *events.GuildScheduledEventCreate) {
	b.recordScheduledEvent("scheduled_event_create", event.GuildScheduled.GuildID, event.GuildScheduled.ID, event.GuildScheduled.Name)
}

func (b *Bot) onGuildScheduledEventUpdate(event *events.GuildScheduledEventUpdate) {
	b.recordScheduledEvent("scheduled_event_update", event.GuildScheduled.GuildID, event.GuildScheduled.ID, event.GuildScheduled.Name)
}

func (b *Bot) onGuildScheduledEventDelete(event *events.GuildScheduledEventDelete) {
	b.recordScheduledEvent("scheduled_event_delete", event.GuildScheduled.GuildID, event.GuildScheduled.ID, event.GuildScheduled.Name)
}

func (b *Bot) onGuildScheduledEventUserAdd(event *events.GuildScheduledEventUserAdd) {
	b.recordScheduledEventUser("scheduled_event_user_add", event.GuildID, event.GuildScheduledEventID, event.UserID)
}

func (b *Bot) onGuildScheduledEventUserRemove(event *events.GuildScheduledEventUserRemove) {
	b.recordScheduledEventUser("scheduled_event_user_remove", event.GuildID, event.GuildScheduledEventID, event.UserID)
}

func (b *Bot) onGuildVoiceStateUpdate(event *events.GuildVoiceStateUpdate) {
	channelID := ""
	if event.VoiceState.ChannelID != nil {
		channelID = event.VoiceState.ChannelID.String()
	}
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.VoiceState.GuildID.String(),
		ChannelID: channelID,
		UserID:    event.VoiceState.UserID.String(),
		EventType: "voice_state_update",
		Summary:   "Voice state updated",
	})
}

func (b *Bot) recordMessageEvent(ctx context.Context, eventType string, message disgoDiscord.Message) {
	guildID := ""
	if message.GuildID != nil {
		guildID = message.GuildID.String()
	}
	contentPreview := ""
	b.recordDiscordEvent(ctx, store.DiscordEvent{
		GuildID:        guildID,
		ChannelID:      message.ChannelID.String(),
		UserID:         message.Author.ID.String(),
		MessageID:      message.ID.String(),
		EventType:      eventType,
		Summary:        "Message activity",
		ContentPreview: contentPreview,
		Metadata: metadataJSON(map[string]string{
			"author_bot":       fmt.Sprintf("%t", message.Author.Bot),
			"attachment_count": fmt.Sprintf("%d", len(message.Attachments)),
			"embed_count":      fmt.Sprintf("%d", len(message.Embeds)),
		}),
	})
}

func (b *Bot) recordReactionEvent(eventType string, guildID, channelID, messageID, userID snowflake.ID, emoji string) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		ChannelID: channelID.String(),
		UserID:    userID.String(),
		MessageID: messageID.String(),
		EventType: eventType,
		Summary:   "Message reaction activity",
		Metadata:  metadataJSON(map[string]string{"emoji": emoji}),
	})
}

func (b *Bot) recordPollVoteEvent(eventType string, guildID, channelID, messageID, userID snowflake.ID, answerID int) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		ChannelID: channelID.String(),
		UserID:    userID.String(),
		MessageID: messageID.String(),
		EventType: eventType,
		Summary:   "Poll vote activity",
		Metadata:  metadataJSON(map[string]string{"answer_id": fmt.Sprintf("%d", answerID)}),
	})
}

func (b *Bot) recordChannelEvent(eventType string, guildID, channelID snowflake.ID, name string) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		ChannelID: channelID.String(),
		EventType: eventType,
		Summary:   "Channel changed",
		Metadata:  metadataJSON(map[string]string{"name": name}),
	})
}

func (b *Bot) recordThreadEvent(eventType string, guildID, threadID snowflake.ID, name string) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		ChannelID: threadID.String(),
		EventType: eventType,
		Summary:   "Thread changed",
		Metadata:  metadataJSON(map[string]string{"name": name}),
	})
}

func (b *Bot) recordRoleEvent(eventType string, guildID, roleID snowflake.ID, name string) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		EventType: eventType,
		Summary:   "Role changed",
		Metadata:  metadataJSON(map[string]string{"role_id": roleID.String(), "name": name}),
	})
}

func (b *Bot) recordGuildUserEvent(eventType string, guildID, userID snowflake.ID, summary string) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		UserID:    userID.String(),
		EventType: eventType,
		Summary:   summary,
	})
}

func (b *Bot) recordAutoModerationRuleEvent(eventType string, guildID, ruleID snowflake.ID, name string) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		EventType: eventType,
		Summary:   "Auto moderation rule changed",
		Metadata:  metadataJSON(map[string]string{"rule_id": ruleID.String(), "name": name}),
	})
}

func (b *Bot) recordScheduledEvent(eventType string, guildID, scheduledEventID snowflake.ID, name string) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		EventType: eventType,
		Summary:   "Scheduled event changed",
		Metadata:  metadataJSON(map[string]string{"event_id": scheduledEventID.String(), "name": name}),
	})
}

func (b *Bot) recordScheduledEventUser(eventType string, guildID, scheduledEventID, userID snowflake.ID) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		UserID:    userID.String(),
		EventType: eventType,
		Summary:   "Scheduled event user activity",
		Metadata:  metadataJSON(map[string]string{"event_id": scheduledEventID.String()}),
	})
}

func metadataJSON(values map[string]string) string {
	for key, value := range values {
		values[key] = security.RedactSecrets(value)
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(data)
}
