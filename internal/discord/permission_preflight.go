package discord

import (
	"fmt"
	"strings"
	"time"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type discordPermissionLookup interface {
	GetMember(guildID snowflake.ID, userID snowflake.ID, opts ...rest.RequestOpt) (*disgoDiscord.Member, error)
	GetGuild(guildID snowflake.ID, withCounts bool, opts ...rest.RequestOpt) (*disgoDiscord.RestGuild, error)
	GetRoles(guildID snowflake.ID, opts ...rest.RequestOpt) ([]disgoDiscord.Role, error)
	GetChannel(channelID snowflake.ID, opts ...rest.RequestOpt) (disgoDiscord.Channel, error)
}

type discordPermissionPreflightRequest struct {
	Rest        discordPermissionLookup
	BotUserID   snowflake.ID
	GuildID     string
	ChannelID   string
	Permissions []string
}

func preflightDiscordPermissions(request discordPermissionPreflightRequest) error {
	required := permissionBits(request.Permissions)
	if required == disgoDiscord.PermissionsNone || request.BotUserID == 0 {
		return nil
	}
	guildID, err := requiredSnowflake(request.GuildID, "guild_id")
	if err != nil {
		return fmt.Errorf("discord permission preflight failed: %w", err)
	}
	if request.Rest == nil {
		return fmt.Errorf("discord permission preflight failed: rest adapter is not configured")
	}
	member, err := request.Rest.GetMember(guildID, request.BotUserID)
	if err != nil {
		return fmt.Errorf("discord permission preflight failed: bot member lookup: %w", err)
	}
	permissions, err := guildPermissions(request.Rest, guildID, *member)
	if err != nil {
		return err
	}
	if channelIDValue := strings.TrimSpace(request.ChannelID); channelIDValue != "" {
		channelID, err := requiredSnowflake(channelIDValue, "channel_id")
		if err != nil {
			return fmt.Errorf("discord permission preflight failed: %w", err)
		}
		channel, err := request.Rest.GetChannel(channelID)
		if err != nil {
			return fmt.Errorf("discord permission preflight failed: channel lookup: %w", err)
		}
		required = requiredPermissionsForChannel(required, channel)
		if parentID, ok := threadParentChannelID(channel); ok {
			parent, err := request.Rest.GetChannel(parentID)
			if err != nil {
				return fmt.Errorf("discord permission preflight failed: parent channel lookup: %w", err)
			}
			if parentGuildChannel, ok := parent.(disgoDiscord.GuildChannel); ok {
				permissions = applyChannelOverwrites(permissions, parentGuildChannel, *member)
			}
		} else if guildChannel, ok := channel.(disgoDiscord.GuildChannel); ok {
			permissions = applyChannelOverwrites(permissions, guildChannel, *member)
		}
	}
	if permissions.Has(disgoDiscord.PermissionAdministrator) || permissions.Has(required) {
		return nil
	}
	missing := required &^ permissions
	return fmt.Errorf("discord permission preflight failed: missing %s", missing.String())
}

func guildPermissions(restClient discordPermissionLookup, guildID snowflake.ID, member disgoDiscord.Member) (disgoDiscord.Permissions, error) {
	guild, err := restClient.GetGuild(guildID, false)
	if err != nil {
		return disgoDiscord.PermissionsNone, fmt.Errorf("discord permission preflight failed: guild lookup: %w", err)
	}
	if guild.OwnerID == member.User.ID {
		return disgoDiscord.PermissionsAll, nil
	}
	roles, err := restClient.GetRoles(guildID)
	if err != nil {
		return disgoDiscord.PermissionsNone, fmt.Errorf("discord permission preflight failed: role lookup: %w", err)
	}
	roleByID := map[snowflake.ID]disgoDiscord.Role{}
	for _, role := range roles {
		roleByID[role.ID] = role
	}
	permissions := disgoDiscord.PermissionsNone
	if publicRole, ok := roleByID[guildID]; ok {
		permissions = publicRole.Permissions
	}
	for _, roleID := range member.RoleIDs {
		role, ok := roleByID[roleID]
		if !ok {
			continue
		}
		permissions = permissions.Add(role.Permissions)
		if permissions.Has(disgoDiscord.PermissionAdministrator) {
			return disgoDiscord.PermissionsAll, nil
		}
	}
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		permissions &= disgoDiscord.PermissionViewChannel | disgoDiscord.PermissionReadMessageHistory
	}
	return permissions, nil
}

func requiredPermissionsForChannel(required disgoDiscord.Permissions, channel disgoDiscord.Channel) disgoDiscord.Permissions {
	if !isThreadChannel(channel) || !required.Has(disgoDiscord.PermissionSendMessages) {
		return required
	}
	required &^= disgoDiscord.PermissionSendMessages
	return required.Add(disgoDiscord.PermissionSendMessagesInThreads)
}

func threadParentChannelID(channel disgoDiscord.Channel) (snowflake.ID, bool) {
	if !isThreadChannel(channel) {
		return 0, false
	}
	if guildChannel, ok := channel.(disgoDiscord.GuildChannel); ok {
		if parentID := guildChannel.ParentID(); parentID != nil && *parentID != 0 {
			return *parentID, true
		}
	}
	return 0, false
}

func isThreadChannel(channel disgoDiscord.Channel) bool {
	if channel == nil {
		return false
	}
	switch channel.Type() {
	case disgoDiscord.ChannelTypeGuildNewsThread,
		disgoDiscord.ChannelTypeGuildPublicThread,
		disgoDiscord.ChannelTypeGuildPrivateThread:
		return true
	default:
		return false
	}
}

func applyChannelOverwrites(permissions disgoDiscord.Permissions, channel disgoDiscord.GuildChannel, member disgoDiscord.Member) disgoDiscord.Permissions {
	if permissions.Has(disgoDiscord.PermissionAdministrator) {
		return disgoDiscord.PermissionsAll
	}
	var allow disgoDiscord.Permissions
	var deny disgoDiscord.Permissions
	if overwrite, ok := channel.PermissionOverwrites().Role(channel.GuildID()); ok {
		permissions |= overwrite.Allow
		permissions &= ^overwrite.Deny
	}
	for _, roleID := range member.RoleIDs {
		if roleID == channel.GuildID() {
			continue
		}
		if overwrite, ok := channel.PermissionOverwrites().Role(roleID); ok {
			allow |= overwrite.Allow
			deny |= overwrite.Deny
		}
	}
	if overwrite, ok := channel.PermissionOverwrites().Member(member.User.ID); ok {
		allow |= overwrite.Allow
		deny |= overwrite.Deny
	}
	permissions &= ^deny
	permissions |= allow
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		permissions &= disgoDiscord.PermissionViewChannel | disgoDiscord.PermissionReadMessageHistory
	}
	return permissions
}

func requiredSnowflake(value string, name string) (snowflake.ID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	id, err := snowflake.Parse(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return id, nil
}
