package discord

import (
	"encoding/json"
	"strings"
	"testing"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type fakePermissionLookup struct {
	guild   *disgoDiscord.RestGuild
	member  *disgoDiscord.Member
	roles   []disgoDiscord.Role
	channel disgoDiscord.Channel
}

func (f fakePermissionLookup) GetMember(_ snowflake.ID, _ snowflake.ID, _ ...rest.RequestOpt) (*disgoDiscord.Member, error) {
	return f.member, nil
}

func (f fakePermissionLookup) GetGuild(_ snowflake.ID, _ bool, _ ...rest.RequestOpt) (*disgoDiscord.RestGuild, error) {
	return f.guild, nil
}

func (f fakePermissionLookup) GetRoles(_ snowflake.ID, _ ...rest.RequestOpt) ([]disgoDiscord.Role, error) {
	return f.roles, nil
}

func (f fakePermissionLookup) GetChannel(_ snowflake.ID, _ ...rest.RequestOpt) (disgoDiscord.Channel, error) {
	return f.channel, nil
}

func TestNaturalMessageReplyPreflightRejectsChannelSendDeny(t *testing.T) {
	guildID := snowflake.MustParse("100000000000000001")
	botID := snowflake.MustParse("100000000000000002")
	channelID := snowflake.MustParse("100000000000000003")
	lookup := fakePermissionLookup{
		guild: &disgoDiscord.RestGuild{Guild: disgoDiscord.Guild{
			ID:      guildID,
			OwnerID: snowflake.MustParse("100000000000000009"),
		}},
		member: &disgoDiscord.Member{User: disgoDiscord.User{ID: botID}},
		roles: []disgoDiscord.Role{{
			ID:          guildID,
			Permissions: naturalMessageReplyPermissionBits(),
		}},
		channel: testGuildTextChannel(t, guildID, channelID, []disgoDiscord.PermissionOverwrite{
			disgoDiscord.RolePermissionOverwrite{
				RoleID: guildID,
				Deny:   disgoDiscord.PermissionSendMessages,
			},
		}),
	}

	err := preflightDiscordPermissions(discordPermissionPreflightRequest{
		Rest:        lookup,
		BotUserID:   botID,
		GuildID:     guildID.String(),
		ChannelID:   channelID.String(),
		Permissions: naturalMessageReplyPermissions,
	})
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "Send Messages") {
		t.Fatalf("expected missing Send Messages preflight error, got %v", err)
	}
}

func TestNaturalMessageReplyPreflightAllowsGuildRolePermissions(t *testing.T) {
	guildID := snowflake.MustParse("100000000000000001")
	botID := snowflake.MustParse("100000000000000002")
	channelID := snowflake.MustParse("100000000000000003")
	lookup := fakePermissionLookup{
		guild: &disgoDiscord.RestGuild{Guild: disgoDiscord.Guild{
			ID:      guildID,
			OwnerID: snowflake.MustParse("100000000000000009"),
		}},
		member: &disgoDiscord.Member{User: disgoDiscord.User{ID: botID}},
		roles: []disgoDiscord.Role{{
			ID:          guildID,
			Permissions: naturalMessageReplyPermissionBits(),
		}},
		channel: testGuildTextChannel(t, guildID, channelID, nil),
	}

	if err := preflightDiscordPermissions(discordPermissionPreflightRequest{
		Rest:        lookup,
		BotUserID:   botID,
		GuildID:     guildID.String(),
		ChannelID:   channelID.String(),
		Permissions: naturalMessageReplyPermissions,
	}); err != nil {
		t.Fatalf("expected natural reply preflight to pass, got %v", err)
	}
}

func naturalMessageReplyPermissionBits() disgoDiscord.Permissions {
	return disgoDiscord.PermissionViewChannel |
		disgoDiscord.PermissionSendMessages |
		disgoDiscord.PermissionReadMessageHistory |
		disgoDiscord.PermissionEmbedLinks
}

func testGuildTextChannel(t *testing.T, guildID, channelID snowflake.ID, overwrites []disgoDiscord.PermissionOverwrite) disgoDiscord.GuildTextChannel {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"id":                    channelID.String(),
		"type":                  disgoDiscord.ChannelTypeGuildText,
		"guild_id":              guildID.String(),
		"name":                  "general",
		"permission_overwrites": overwrites,
	})
	if err != nil {
		t.Fatalf("marshal channel: %v", err)
	}
	var channel disgoDiscord.GuildTextChannel
	if err := json.Unmarshal(data, &channel); err != nil {
		t.Fatalf("unmarshal channel: %v", err)
	}
	return channel
}
