package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
)

type roleClient interface {
	CreateRole(guildID snowflake.ID, createRole disgoDiscord.RoleCreate, opts ...rest.RequestOpt) (*disgoDiscord.Role, error)
}

type RoleManager struct {
	client roleClient
}

func NewRoleManager(client roleClient) *RoleManager {
	return &RoleManager{client: client}
}

func (m *RoleManager) CreateRole(ctx context.Context, request commands.DiscordRoleRequest) (commands.DiscordRole, error) {
	guildID, err := snowflake.Parse(strings.TrimSpace(request.GuildID))
	if err != nil {
		return commands.DiscordRole{}, fmt.Errorf("parse guild id: %w", err)
	}
	name, err := roleCreateName(request.Name)
	if err != nil {
		return commands.DiscordRole{}, err
	}
	permissions := disgoDiscord.PermissionsNone
	role, err := m.client.CreateRole(guildID, disgoDiscord.RoleCreate{
		Name:        name,
		Permissions: &permissions,
	}, roleRequestOpts(ctx, request)...)
	if err != nil {
		if isRoleSetupError(err) {
			return commands.DiscordRole{}, fmt.Errorf("%w: %v", commands.ErrDiscordRoleSetup, err)
		}
		return commands.DiscordRole{}, err
	}
	return commands.DiscordRole{ID: role.ID.String(), Name: role.Name}, nil
}

func roleCreateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if len([]rune(name)) > 100 {
		return "", fmt.Errorf("name must be 100 characters or fewer")
	}
	return name, nil
}

func roleRequestOpts(ctx context.Context, request commands.DiscordRoleRequest) []rest.RequestOpt {
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

func isRoleSetupError(err error) bool {
	var restErr *rest.Error
	if errors.As(err, &restErr) {
		if restErr.Code == rest.JSONErrorCodeLackPermissionsToPerformAction || restErr.Code == rest.JSONErrorCodeMissingAccess {
			return true
		}
		return restErr.Response != nil && restErr.Response.StatusCode == http.StatusForbidden
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "missing permissions") ||
		strings.Contains(message, "missing manage_roles") ||
		strings.Contains(message, "missing manage roles") ||
		strings.Contains(message, "role hierarchy")
}
