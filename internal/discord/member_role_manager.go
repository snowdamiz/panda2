package discord

import (
	"context"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
)

type memberRoleClient interface {
	AddMemberRole(guildID snowflake.ID, userID snowflake.ID, roleID snowflake.ID, opts ...rest.RequestOpt) error
	RemoveMemberRole(guildID snowflake.ID, userID snowflake.ID, roleID snowflake.ID, opts ...rest.RequestOpt) error
}

type MemberRoleManager struct {
	client memberRoleClient
}

func NewMemberRoleManager(client memberRoleClient) *MemberRoleManager {
	return &MemberRoleManager{client: client}
}

func (m *MemberRoleManager) AddMemberRole(ctx context.Context, request commands.MemberRoleRequest) error {
	guildID, userID, roleID, err := memberRoleIDs(request)
	if err != nil {
		return err
	}
	return m.client.AddMemberRole(guildID, userID, roleID, memberRoleRequestOpts(ctx, request)...)
}

func (m *MemberRoleManager) RemoveMemberRole(ctx context.Context, request commands.MemberRoleRequest) error {
	guildID, userID, roleID, err := memberRoleIDs(request)
	if err != nil {
		return err
	}
	return m.client.RemoveMemberRole(guildID, userID, roleID, memberRoleRequestOpts(ctx, request)...)
}

func memberRoleIDs(request commands.MemberRoleRequest) (snowflake.ID, snowflake.ID, snowflake.ID, error) {
	guildID, err := snowflake.Parse(strings.TrimSpace(request.GuildID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse guild id: %w", err)
	}
	userID, err := snowflake.Parse(strings.TrimSpace(request.UserID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse user id: %w", err)
	}
	roleID, err := snowflake.Parse(strings.TrimSpace(request.RoleID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse role id: %w", err)
	}
	return guildID, userID, roleID, nil
}

func memberRoleRequestOpts(ctx context.Context, request commands.MemberRoleRequest) []rest.RequestOpt {
	opts := []rest.RequestOpt{rest.WithCtx(ctx)}
	reason := strings.TrimSpace(request.Reason)
	if request.ActorID != "" {
		if reason != "" {
			reason += " "
		}
		reason += "by " + request.ActorID
	}
	if reason != "" {
		opts = append(opts, rest.WithReason(reason))
	}
	return opts
}
