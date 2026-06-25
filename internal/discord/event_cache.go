package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/composed"
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
	if b == nil || strings.TrimSpace(event.GuildID) == "" {
		return
	}
	recorded := event
	if b.events != nil {
		saved, err := b.events.Record(ctx, event)
		if err != nil {
			discordEventDrops.Add(1)
		} else {
			recorded = saved
		}
	}
	if composed.SupportsEventType(event.EventType) {
		b.enqueueComposedEvent(ctx, recorded, metadataMap(event.Metadata))
	}
	if b.alerts != nil {
		b.alerts.HandleDiscordEvent(ctx, recorded)
	}
}

func (b *Bot) onGuildMessageUpdate(event *events.GuildMessageUpdate) {
	b.recordMessageEvent(context.Background(), composed.EventMessageUpdated, event.Message)
}

func (b *Bot) onGuildMessageDelete(event *events.GuildMessageDelete) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		MessageID: event.MessageID.String(),
		EventType: composed.EventMessageDeleted,
		Summary:   "Message deleted",
		Metadata:  metadataJSON(map[string]string{"message_id": event.MessageID.String()}),
	})
}

func (b *Bot) onGuildMessageReactionAdd(event *events.GuildMessageReactionAdd) {
	b.recordReactionEvent(composed.EventReactionAdded, event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.Emoji.Reaction())
}

func (b *Bot) onGuildMessageReactionRemove(event *events.GuildMessageReactionRemove) {
	b.recordReactionEvent(composed.EventReactionRemoved, event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.Emoji.Reaction())
}

func (b *Bot) onGuildMessageReactionRemoveAll(event *events.GuildMessageReactionRemoveAll) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		MessageID: event.MessageID.String(),
		EventType: composed.EventReactionsRemovedAll,
		Summary:   "All reactions removed from message",
	})
}

func (b *Bot) onGuildMessageReactionRemoveEmoji(event *events.GuildMessageReactionRemoveEmoji) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		MessageID: event.MessageID.String(),
		EventType: composed.EventReactionEmojiRemoved,
		Summary:   "All reactions for one emoji removed from message",
		Metadata:  metadataJSON(map[string]string{"emoji": event.Emoji.Reaction()}),
	})
}

func (b *Bot) onGuildMessagePollVoteAdd(event *events.GuildMessagePollVoteAdd) {
	b.recordPollVoteEvent(composed.EventPollVoteAdded, event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.AnswerID)
}

func (b *Bot) onGuildMessagePollVoteRemove(event *events.GuildMessagePollVoteRemove) {
	b.recordPollVoteEvent(composed.EventPollVoteRemoved, event.GuildID, event.ChannelID, event.MessageID, event.UserID, event.AnswerID)
}

func (b *Bot) onGuildChannelCreate(event *events.GuildChannelCreate) {
	b.recordChannelEvent(composed.EventChannelCreated, event.GuildID, event.ChannelID, event.Channel.Name())
}

func (b *Bot) onGuildChannelUpdate(event *events.GuildChannelUpdate) {
	b.recordChannelEvent(composed.EventChannelUpdated, event.GuildID, event.ChannelID, event.Channel.Name())
}

func (b *Bot) onGuildChannelDelete(event *events.GuildChannelDelete) {
	b.recordChannelEvent(composed.EventChannelDeleted, event.GuildID, event.ChannelID, event.Channel.Name())
}

func (b *Bot) onGuildChannelPinsUpdate(event *events.GuildChannelPinsUpdate) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ChannelID.String(),
		EventType: composed.EventChannelPinsUpdated,
		Summary:   "Channel pins updated",
		Metadata: metadataJSON(map[string]string{
			"last_pin_at": timePtrValue(event.NewLastPinTimestamp),
		}),
	})
}

func (b *Bot) onThreadCreate(event *events.ThreadCreate) {
	b.recordThreadEvent(composed.EventThreadCreated, event.GuildID, event.ThreadID, event.Thread.Name())
}

func (b *Bot) onThreadUpdate(event *events.ThreadUpdate) {
	b.recordThreadEvent(composed.EventThreadUpdated, event.GuildID, event.ThreadID, event.Thread.Name())
}

func (b *Bot) onThreadDelete(event *events.ThreadDelete) {
	b.recordThreadEvent(composed.EventThreadDeleted, event.GuildID, event.ThreadID, event.Thread.Name())
}

func (b *Bot) onThreadMemberUpdate(event *events.ThreadMemberUpdate) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildID.String(),
		ChannelID: event.ThreadID.String(),
		UserID:    event.ThreadMemberID.String(),
		EventType: composed.EventThreadMemberUpdated,
		Summary:   "Thread member updated",
	})
}

func (b *Bot) onGuildMemberJoin(event *events.GuildMemberJoin) {
	b.recordMemberEvent(context.Background(), composed.EventGuildMemberJoined, event.GuildID, event.Member)
}

func (b *Bot) onGuildMemberUpdate(event *events.GuildMemberUpdate) {
	oldRoles := snowflakeSet(event.OldMember.RoleIDs)
	for _, roleID := range event.Member.RoleIDs {
		if _, existed := oldRoles[roleID]; existed {
			continue
		}
		b.recordMemberRoleEvent(context.Background(), composed.EventGuildMemberRoleAdded, event.GuildID, event.Member.User.ID, roleID)
	}
	newRoles := snowflakeSet(event.Member.RoleIDs)
	for _, roleID := range event.OldMember.RoleIDs {
		if _, exists := newRoles[roleID]; exists {
			continue
		}
		b.recordMemberRoleEvent(context.Background(), composed.EventGuildMemberRoleRemoved, event.GuildID, event.Member.User.ID, roleID)
	}
}

func (b *Bot) onRoleCreate(event *events.RoleCreate) {
	b.recordRoleEvent(composed.EventRoleCreated, event.GuildID, event.RoleID, event.Role.Name)
}

func (b *Bot) onRoleUpdate(event *events.RoleUpdate) {
	b.recordRoleEvent(composed.EventRoleUpdated, event.GuildID, event.RoleID, event.Role.Name)
}

func (b *Bot) onRoleDelete(event *events.RoleDelete) {
	b.recordRoleEvent(composed.EventRoleDeleted, event.GuildID, event.RoleID, event.Role.Name)
}

func (b *Bot) onGuildBan(event *events.GuildBan) {
	b.recordGuildUserEvent(composed.EventGuildBan, event.GuildID, event.User.ID, "User banned")
}

func (b *Bot) onGuildUnban(event *events.GuildUnban) {
	b.recordGuildUserEvent(composed.EventGuildUnban, event.GuildID, event.User.ID, "User unbanned")
}

func (b *Bot) onInviteCreate(event *events.InviteCreate) {
	guildID := ""
	if event.GuildID != nil {
		guildID = event.GuildID.String()
	}
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID,
		ChannelID: event.ChannelID.String(),
		EventType: composed.EventInviteCreated,
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
		EventType: composed.EventInviteDeleted,
		Summary:   "Invite deleted",
		Metadata:  metadataJSON(map[string]string{"code": event.Code}),
	})
}

func (b *Bot) onWebhooksUpdate(event *events.WebhooksUpdate) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.GuildId.String(),
		ChannelID: event.ChannelID.String(),
		EventType: composed.EventWebhooksUpdated,
		Summary:   "Channel webhooks updated",
	})
}

func (b *Bot) onAutoModerationRuleCreate(event *events.AutoModerationRuleCreate) {
	b.recordAutoModerationRuleEvent(composed.EventAutoModerationCreated, event.GuildID, event.ID, event.Name)
}

func (b *Bot) onAutoModerationRuleUpdate(event *events.AutoModerationRuleUpdate) {
	b.recordAutoModerationRuleEvent(composed.EventAutoModerationUpdated, event.GuildID, event.ID, event.Name)
}

func (b *Bot) onAutoModerationRuleDelete(event *events.AutoModerationRuleDelete) {
	b.recordAutoModerationRuleEvent(composed.EventAutoModerationDeleted, event.GuildID, event.ID, event.Name)
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
		EventType: composed.EventAutoModerationAction,
		Summary:   "Auto moderation action executed",
		Metadata: metadataJSON(map[string]string{
			"rule_id":      event.RuleID.String(),
			"trigger_type": fmt.Sprint(event.RuleTriggerType),
		}),
	})
}

func (b *Bot) onGuildScheduledEventCreate(event *events.GuildScheduledEventCreate) {
	b.recordScheduledEvent(composed.EventScheduledCreated, event.GuildScheduled.GuildID, event.GuildScheduled.ID, event.GuildScheduled.Name)
}

func (b *Bot) onGuildScheduledEventUpdate(event *events.GuildScheduledEventUpdate) {
	b.recordScheduledEvent(composed.EventScheduledUpdated, event.GuildScheduled.GuildID, event.GuildScheduled.ID, event.GuildScheduled.Name)
}

func (b *Bot) onGuildScheduledEventDelete(event *events.GuildScheduledEventDelete) {
	b.recordScheduledEvent(composed.EventScheduledDeleted, event.GuildScheduled.GuildID, event.GuildScheduled.ID, event.GuildScheduled.Name)
}

func (b *Bot) onGuildScheduledEventUserAdd(event *events.GuildScheduledEventUserAdd) {
	b.recordScheduledEventUser(composed.EventScheduledUserAdded, event.GuildID, event.GuildScheduledEventID, event.UserID)
}

func (b *Bot) onGuildScheduledEventUserRemove(event *events.GuildScheduledEventUserRemove) {
	b.recordScheduledEventUser(composed.EventScheduledUserRemoved, event.GuildID, event.GuildScheduledEventID, event.UserID)
}

func (b *Bot) onGuildVoiceStateUpdate(event *events.GuildVoiceStateUpdate) {
	b.forwardOwnVoiceStateToVoiceManager(event)
	b.updateMusicVoiceOccupancy(event)

	channelID := ""
	if event.VoiceState.ChannelID != nil {
		channelID = event.VoiceState.ChannelID.String()
	}
	metadata := map[string]string{
		"username":       event.Member.User.Username,
		"effective_name": event.Member.EffectiveName(),
		"user_is_bot":    fmt.Sprintf("%t", event.Member.User.Bot),
	}
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   event.VoiceState.GuildID.String(),
		ChannelID: channelID,
		UserID:    event.VoiceState.UserID.String(),
		EventType: composed.EventVoiceStateUpdated,
		Summary:   "Voice state updated",
		Metadata:  metadataJSON(metadata),
	})
}

func (b *Bot) updateMusicVoiceOccupancy(event *events.GuildVoiceStateUpdate) {
	if b == nil || b.music == nil || b.client == nil || b.client.Caches == nil || event == nil {
		return
	}
	selfID := b.selfUserID()
	if selfID != 0 && event.VoiceState.UserID == selfID {
		return
	}

	guildID := event.VoiceState.GuildID
	channelIDValue := b.music.ActiveVoiceChannelID(guildID.String())
	if channelIDValue == "" {
		return
	}
	channelID, err := snowflake.Parse(channelIDValue)
	if err != nil {
		return
	}
	if !voiceStateUpdateTouchesChannel(event, channelID) {
		return
	}

	listeners := b.voiceListenerCount(guildID, channelID)
	b.music.UpdateVoiceOccupancy(guildID.String(), channelID.String(), listeners > 0)
}

func (b *Bot) voiceListenerCount(guildID snowflake.ID, channelID snowflake.ID) int {
	if b == nil || b.client == nil || b.client.Caches == nil || channelID == 0 {
		return 0
	}
	selfID := b.selfUserID()
	listeners := 0
	for state := range b.client.Caches.VoiceStates(guildID) {
		if state.ChannelID == nil || *state.ChannelID != channelID {
			continue
		}
		if selfID != 0 && state.UserID == selfID {
			continue
		}
		if b.cachedVoiceMemberIsBot(guildID, state.UserID) {
			continue
		}
		listeners++
	}
	return listeners
}

func (b *Bot) cachedVoiceMemberIsBot(guildID snowflake.ID, userID snowflake.ID) bool {
	if b == nil || b.client == nil || b.client.Caches == nil {
		return false
	}
	member, ok := b.client.Caches.Member(guildID, userID)
	return ok && member.User.Bot
}

func voiceStateUpdateTouchesChannel(event *events.GuildVoiceStateUpdate, channelID snowflake.ID) bool {
	if event == nil || channelID == 0 {
		return false
	}
	if event.VoiceState.ChannelID != nil && *event.VoiceState.ChannelID == channelID {
		return true
	}
	return event.OldVoiceState.ChannelID != nil && *event.OldVoiceState.ChannelID == channelID
}

func (b *Bot) forwardOwnVoiceStateToVoiceManager(event *events.GuildVoiceStateUpdate) {
	if b == nil || b.client == nil || b.client.VoiceManager == nil || event == nil {
		return
	}
	selfID := b.selfUserID()
	if selfID == 0 || event.VoiceState.UserID != selfID {
		return
	}
	b.client.VoiceManager.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{
		VoiceState: event.VoiceState,
		Member:     event.Member,
	})
}

func (b *Bot) selfUserID() snowflake.ID {
	if b == nil || b.client == nil {
		return 0
	}
	if b.client.ApplicationID != 0 {
		return b.client.ApplicationID
	}
	if b.client.Caches == nil {
		return 0
	}
	if selfUser, ok := b.client.Caches.SelfUser(); ok {
		return selfUser.ID
	}
	return 0
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
			"sticker_count":    fmt.Sprintf("%d", len(message.StickerItems)),
			"snapshot_count":   fmt.Sprintf("%d", len(message.MessageSnapshots)),
			"image_ref_count":  fmt.Sprintf("%d", len(imageReferencesFromMessage(message, "event"))),
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

func (b *Bot) recordMemberEvent(ctx context.Context, eventType string, guildID snowflake.ID, member disgoDiscord.Member) {
	metadata := map[string]string{
		"username":       member.User.Username,
		"effective_name": member.EffectiveName(),
		"user_is_bot":    fmt.Sprintf("%t", member.User.Bot),
	}
	event := store.DiscordEvent{
		GuildID:   guildID.String(),
		UserID:    member.User.ID.String(),
		EventType: eventType,
		Summary:   "Member joined",
		Metadata:  metadataJSON(metadata),
	}
	b.recordDiscordEvent(ctx, event)
}

func (b *Bot) recordMemberRoleEvent(ctx context.Context, eventType string, guildID, userID, roleID snowflake.ID) {
	roleName := b.roleName(guildID, roleID)
	metadata := map[string]string{"role_id": roleID.String()}
	if roleName != "" {
		metadata["role_name"] = roleName
	}
	event := store.DiscordEvent{
		GuildID:   guildID.String(),
		UserID:    userID.String(),
		EventType: eventType,
		Summary:   "Member role changed",
		Metadata:  metadataJSON(metadata),
	}
	b.recordDiscordEvent(ctx, event)
}

func (b *Bot) enqueueComposedEvent(ctx context.Context, event store.DiscordEvent, metadata map[string]string) {
	if b == nil || b.jobs == nil || event.GuildID == "" {
		return
	}
	eventID := ""
	if event.ID != 0 {
		eventID = fmt.Sprintf("%d", event.ID)
	}
	payload, err := json.Marshal(composed.EventJobPayload{
		GuildID:   event.GuildID,
		EventID:   eventID,
		EventType: event.EventType,
		UserID:    event.UserID,
		ChannelID: event.ChannelID,
		MessageID: event.MessageID,
		Metadata:  cleanStringMap(metadata),
		CreatedAt: event.CreatedAt,
	})
	if err != nil {
		return
	}
	_, _ = b.jobs.Enqueue(ctx, store.Job{
		Kind:        composed.EventJobKind,
		GuildID:     event.GuildID,
		Payload:     string(payload),
		MaxAttempts: 3,
	})
}

func (b *Bot) roleName(guildID, roleID snowflake.ID) string {
	if b == nil || b.client == nil {
		return ""
	}
	role, err := b.client.Rest.GetRole(guildID, roleID)
	if err != nil || role == nil {
		return ""
	}
	return role.Name
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
		Metadata:  metadataJSON(map[string]string{"scheduled_event_id": scheduledEventID.String(), "name": name}),
	})
}

func (b *Bot) recordScheduledEventUser(eventType string, guildID, scheduledEventID, userID snowflake.ID) {
	b.recordDiscordEvent(context.Background(), store.DiscordEvent{
		GuildID:   guildID.String(),
		UserID:    userID.String(),
		EventType: eventType,
		Summary:   "Scheduled event user activity",
		Metadata:  metadataJSON(map[string]string{"scheduled_event_id": scheduledEventID.String()}),
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

func metadataMap(raw string) map[string]string {
	values := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return values
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return map[string]string{}
	}
	return values
}

func snowflakeSet(ids []snowflake.ID) map[snowflake.ID]struct{} {
	result := make(map[snowflake.ID]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result
}

func cleanStringMap(values map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			result[key] = security.RedactSecrets(value)
		}
	}
	return result
}
