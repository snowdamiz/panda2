package discord

import (
	"context"
	"errors"
	"net/http"
	"testing"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
)

type fakeRoleClient struct {
	requests []disgoDiscord.RoleCreate
	role     *disgoDiscord.Role
	err      error
}

func (f *fakeRoleClient) CreateRole(_ snowflake.ID, createRole disgoDiscord.RoleCreate, _ ...rest.RequestOpt) (*disgoDiscord.Role, error) {
	f.requests = append(f.requests, createRole)
	if f.err != nil {
		return nil, f.err
	}
	if f.role != nil {
		return f.role, nil
	}
	return &disgoDiscord.Role{
		ID:   snowflake.MustParse("100000000000000123"),
		Name: createRole.Name,
	}, nil
}

func TestRoleManagerCreatesZeroPermissionRole(t *testing.T) {
	client := &fakeRoleClient{}
	manager := NewRoleManager(client)

	role, err := manager.CreateRole(context.Background(), commands.DiscordRoleRequest{
		GuildID: "100000000000000001",
		Name:    "test",
		ActorID: "admin",
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if role.Name != "test" || role.ID == "" {
		t.Fatalf("unexpected role: %+v", role)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one create request, got %+v", client.requests)
	}
	request := client.requests[0]
	if request.Name != "test" || request.Permissions == nil || *request.Permissions != disgoDiscord.PermissionsNone {
		t.Fatalf("expected zero-permission role create payload, got %+v", request)
	}
}

func TestRoleManagerWrapsDiscordPermissionFailure(t *testing.T) {
	manager := NewRoleManager(&fakeRoleClient{err: &rest.Error{
		Response: &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden"},
		Code:     rest.JSONErrorCodeLackPermissionsToPerformAction,
		Message:  "Missing Permissions",
	}})

	_, err := manager.CreateRole(context.Background(), commands.DiscordRoleRequest{
		GuildID: "100000000000000001",
		Name:    "test",
	})
	if !errors.Is(err, commands.ErrDiscordRoleSetup) {
		t.Fatalf("expected setup error wrapper, got %v", err)
	}
}
