package discord

import (
	"context"
	"fmt"
	"strings"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/security"
	"github.com/sn0w/panda2/internal/setup"
)

type SetupAdapter struct {
	rest rest.Rest
}

func NewSetupAdapter(restClient rest.Rest) *SetupAdapter {
	return &SetupAdapter{rest: restClient}
}

func (b *Bot) SetupAdapter() *SetupAdapter {
	if b == nil || b.client == nil {
		return nil
	}
	return NewSetupAdapter(b.client.Rest)
}

func (a *SetupAdapter) Snapshot(ctx context.Context, guildID string) (setup.GuildSnapshot, error) {
	if a == nil || a.rest == nil {
		return setup.GuildSnapshot{}, fmt.Errorf("discord setup adapter is not configured")
	}
	id, err := snowflake.Parse(strings.TrimSpace(guildID))
	if err != nil {
		return setup.GuildSnapshot{}, err
	}
	roles, err := a.rest.GetRoles(id, rest.WithCtx(ctx))
	if err != nil {
		return setup.GuildSnapshot{}, err
	}
	channels, err := a.rest.GetGuildChannels(id, rest.WithCtx(ctx))
	if err != nil {
		return setup.GuildSnapshot{}, err
	}
	snapshot := setup.GuildSnapshot{
		Roles:    make([]setup.RoleState, 0, len(roles)),
		Channels: make([]setup.ChannelState, 0, len(channels)),
	}
	for _, role := range roles {
		snapshot.Roles = append(snapshot.Roles, setup.RoleState{
			ID:          role.ID.String(),
			Name:        role.Name,
			Color:       role.Color,
			Hoist:       role.Hoist,
			Mentionable: role.Mentionable,
			Managed:     role.Managed,
			Position:    role.Position,
			Permissions: splitPermissionNames(role.Permissions.String()),
		})
	}
	for _, channel := range channels {
		state := setup.ChannelState{
			ID:       channel.ID().String(),
			Name:     channel.Name(),
			Type:     setupChannelType(channel.Type()),
			Position: channel.Position(),
		}
		if parentID := channel.ParentID(); parentID != nil {
			state.ParentID = parentID.String()
		}
		snapshot.Channels = append(snapshot.Channels, state)
	}
	return snapshot, nil
}

func (a *SetupAdapter) CreateRole(ctx context.Context, request setup.RoleApplyRequest) (setup.DiscordResource, error) {
	guildID, err := snowflake.Parse(strings.TrimSpace(request.GuildID))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	permissions := permissionBits(request.Permissions)
	role, err := a.rest.CreateRole(guildID, disgoDiscord.RoleCreate{
		Name:        strings.TrimSpace(request.Name),
		Permissions: &permissions,
		Color:       request.Color,
		Hoist:       request.Hoist,
		Mentionable: request.Mentionable,
	}, rest.WithCtx(ctx), rest.WithReason(setupReason(request.Reason)))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	return setup.DiscordResource{ID: role.ID.String(), Name: role.Name, Type: setup.ResourceTypeRole}, nil
}

func (a *SetupAdapter) UpdateRole(ctx context.Context, request setup.RoleApplyRequest) (setup.DiscordResource, error) {
	guildID, err := snowflake.Parse(strings.TrimSpace(request.GuildID))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	roleID, err := snowflake.Parse(strings.TrimSpace(request.RoleID))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	permissions := permissionBits(request.Permissions)
	name := strings.TrimSpace(request.Name)
	color := request.Color
	hoist := request.Hoist
	mentionable := request.Mentionable
	role, err := a.rest.UpdateRole(guildID, roleID, disgoDiscord.RoleUpdate{
		Name:        &name,
		Permissions: &permissions,
		Color:       &color,
		Hoist:       &hoist,
		Mentionable: &mentionable,
	}, rest.WithCtx(ctx), rest.WithReason(setupReason(request.Reason)))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	return setup.DiscordResource{ID: role.ID.String(), Name: role.Name, Type: setup.ResourceTypeRole}, nil
}

func (a *SetupAdapter) DeleteRole(ctx context.Context, guildIDValue, roleIDValue, reason string) error {
	guildID, err := snowflake.Parse(strings.TrimSpace(guildIDValue))
	if err != nil {
		return err
	}
	roleID, err := snowflake.Parse(strings.TrimSpace(roleIDValue))
	if err != nil {
		return err
	}
	return a.rest.DeleteRole(guildID, roleID, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
}

func (a *SetupAdapter) MoveRoles(ctx context.Context, guildIDValue string, positions []setup.PositionUpdate, reason string) error {
	guildID, err := snowflake.Parse(strings.TrimSpace(guildIDValue))
	if err != nil {
		return err
	}
	updates := make([]disgoDiscord.RolePositionUpdate, 0, len(positions))
	for _, position := range positions {
		id, err := snowflake.Parse(strings.TrimSpace(position.ID))
		if err != nil {
			return err
		}
		pos := position.Position
		updates = append(updates, disgoDiscord.RolePositionUpdate{ID: id, Position: &pos})
	}
	_, err = a.rest.UpdateRolePositions(guildID, updates, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
	return err
}

func (a *SetupAdapter) CreateChannel(ctx context.Context, request setup.ChannelApplyRequest) (setup.DiscordResource, error) {
	guildID, err := snowflake.Parse(strings.TrimSpace(request.GuildID))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	create, err := channelCreatePayload(request)
	if err != nil {
		return setup.DiscordResource{}, err
	}
	channel, err := a.rest.CreateGuildChannel(guildID, create, rest.WithCtx(ctx), rest.WithReason(setupReason(request.Reason)))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	return setup.DiscordResource{ID: channel.ID().String(), Name: channel.Name(), Type: setupChannelType(channel.Type())}, nil
}

func (a *SetupAdapter) UpdateChannel(ctx context.Context, request setup.ChannelApplyRequest) (setup.DiscordResource, error) {
	channelID, err := snowflake.Parse(strings.TrimSpace(request.ChannelID))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	update, err := channelUpdatePayload(request)
	if err != nil {
		return setup.DiscordResource{}, err
	}
	channel, err := a.rest.UpdateChannel(channelID, update, rest.WithCtx(ctx), rest.WithReason(setupReason(request.Reason)))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	return setup.DiscordResource{ID: channel.ID().String(), Name: channel.Name(), Type: setupChannelType(channel.Type())}, nil
}

func (a *SetupAdapter) DeleteChannel(ctx context.Context, channelIDValue, reason string) error {
	channelID, err := snowflake.Parse(strings.TrimSpace(channelIDValue))
	if err != nil {
		return err
	}
	return a.rest.DeleteChannel(channelID, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
}

func (a *SetupAdapter) MoveChannels(ctx context.Context, guildIDValue string, positions []setup.PositionUpdate, reason string) error {
	guildID, err := snowflake.Parse(strings.TrimSpace(guildIDValue))
	if err != nil {
		return err
	}
	updates := make([]disgoDiscord.GuildChannelPositionUpdate, 0, len(positions))
	for _, position := range positions {
		id, err := snowflake.Parse(strings.TrimSpace(position.ID))
		if err != nil {
			return err
		}
		pos := position.Position
		update := disgoDiscord.GuildChannelPositionUpdate{ID: id, Position: omit.New(&pos)}
		if strings.TrimSpace(position.ParentID) != "" {
			parentID, err := snowflake.Parse(strings.TrimSpace(position.ParentID))
			if err != nil {
				return err
			}
			update.ParentID = &parentID
		}
		updates = append(updates, update)
	}
	return a.rest.UpdateChannelPositions(guildID, updates, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
}

func (a *SetupAdapter) SendMessage(ctx context.Context, request setup.MessageApplyRequest) (setup.DiscordResource, error) {
	channelID, err := snowflake.Parse(strings.TrimSpace(request.ChannelID))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	messageCreate := disgoDiscord.NewMessageCreate().
		WithContent(security.SafeDiscordContent(request.Content)).
		WithAllowedMentions(&disgoDiscord.AllowedMentions{Parse: []disgoDiscord.AllowedMentionType{}})
	if len(request.Components) > 0 {
		messageCreate = messageCreate.WithComponents(messageComponents(request.Components)...)
	}
	message, err := a.rest.CreateMessage(channelID, messageCreate, rest.WithCtx(ctx), rest.WithReason(setupReason(request.Reason)))
	if err != nil {
		return setup.DiscordResource{}, err
	}
	return setup.DiscordResource{ID: message.ID.String(), Name: message.ID.String(), Type: "message"}, nil
}

func (a *SetupAdapter) CreateTicketChannel(ctx context.Context, request setup.TicketChannelRequest) (setup.DiscordResource, error) {
	overwrites := []setup.ResolvedOverwrite{
		{TargetID: request.GuildID, TargetType: "role", Deny: []string{"VIEW_CHANNEL"}},
		{TargetID: request.RequesterUserID, TargetType: "member", Allow: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY", "ATTACH_FILES"}},
	}
	for _, roleID := range request.StaffRoleIDs {
		overwrites = append(overwrites, setup.ResolvedOverwrite{TargetID: roleID, TargetType: "role", Allow: []string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY", "MANAGE_MESSAGES"}})
	}
	channel, err := a.CreateChannel(ctx, setup.ChannelApplyRequest{
		GuildID:    request.GuildID,
		Type:       "text",
		Name:       request.Name,
		Topic:      request.Topic,
		ParentID:   request.CategoryID,
		Overwrites: overwrites,
		Reason:     request.Reason,
	})
	if err != nil {
		return setup.DiscordResource{}, err
	}
	if strings.TrimSpace(request.StarterMessage) != "" {
		_, _ = a.SendMessage(ctx, setup.MessageApplyRequest{ChannelID: channel.ID, Content: request.StarterMessage, Reason: request.Reason})
	}
	return channel, nil
}

func (a *SetupAdapter) AddTicketParticipant(ctx context.Context, _, channelIDValue, userIDValue, reason string) error {
	channelID, err := snowflake.Parse(strings.TrimSpace(channelIDValue))
	if err != nil {
		return err
	}
	userID, err := snowflake.Parse(strings.TrimSpace(userIDValue))
	if err != nil {
		return err
	}
	allow := permissionBits([]string{"VIEW_CHANNEL", "SEND_MESSAGES", "READ_MESSAGE_HISTORY", "ATTACH_FILES"})
	deny := disgoDiscord.Permissions(0)
	return a.rest.UpdatePermissionOverwrite(channelID, userID, disgoDiscord.MemberPermissionOverwriteUpdate{
		Allow: &allow,
		Deny:  &deny,
	}, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
}

func (a *SetupAdapter) RemoveTicketParticipant(ctx context.Context, _, channelIDValue, userIDValue, reason string) error {
	channelID, err := snowflake.Parse(strings.TrimSpace(channelIDValue))
	if err != nil {
		return err
	}
	userID, err := snowflake.Parse(strings.TrimSpace(userIDValue))
	if err != nil {
		return err
	}
	return a.rest.DeletePermissionOverwrite(channelID, userID, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
}

func (a *SetupAdapter) ExportTranscript(ctx context.Context, channelIDValue string, limit int) (map[string]any, error) {
	channelID, err := snowflake.Parse(strings.TrimSpace(channelIDValue))
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	messages, err := a.rest.GetMessages(channelID, 0, 0, 0, limit, rest.WithCtx(ctx))
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		items = append(items, map[string]any{
			"id":         message.ID.String(),
			"author_id":  message.Author.ID.String(),
			"created_at": message.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"content":    message.Content,
		})
	}
	return map[string]any{"channel_id": channelID.String(), "message_count": len(items), "messages": items}, nil
}

func (a *SetupAdapter) AddMemberRole(ctx context.Context, guildIDValue, userIDValue, roleIDValue, reason string) error {
	guildID, userID, roleID, err := guildUserRoleIDs(guildIDValue, userIDValue, roleIDValue)
	if err != nil {
		return err
	}
	return a.rest.AddMemberRole(guildID, userID, roleID, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
}

func (a *SetupAdapter) RemoveMemberRole(ctx context.Context, guildIDValue, userIDValue, roleIDValue, reason string) error {
	guildID, userID, roleID, err := guildUserRoleIDs(guildIDValue, userIDValue, roleIDValue)
	if err != nil {
		return err
	}
	return a.rest.RemoveMemberRole(guildID, userID, roleID, rest.WithCtx(ctx), rest.WithReason(setupReason(reason)))
}

func channelCreatePayload(request setup.ChannelApplyRequest) (disgoDiscord.GuildChannelCreate, error) {
	parentID, err := optionalID(request.ParentID)
	if err != nil {
		return nil, err
	}
	overwrites, err := permissionOverwrites(request.Overwrites)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(request.Type)) {
	case "category":
		return disgoDiscord.GuildCategoryChannelCreate{Name: request.Name, Position: request.Position, PermissionOverwrites: overwrites}, nil
	case "voice":
		return disgoDiscord.GuildVoiceChannelCreate{Name: request.Name, Position: request.Position, ParentID: parentID, Bitrate: request.Bitrate, UserLimit: request.UserLimit, PermissionOverwrites: overwrites}, nil
	case "stage":
		return disgoDiscord.GuildStageVoiceChannelCreate{Name: request.Name, Position: request.Position, ParentID: parentID, Bitrate: request.Bitrate, UserLimit: request.UserLimit, PermissionOverwrites: overwrites}, nil
	case "announcement":
		return disgoDiscord.GuildNewsChannelCreate{Name: request.Name, Topic: request.Topic, RateLimitPerUser: request.SlowmodeSeconds, Position: request.Position, ParentID: parentID, NSFW: request.NSFW, PermissionOverwrites: overwrites}, nil
	case "forum":
		return disgoDiscord.GuildForumChannelCreate{Name: request.Name, Topic: request.Topic, Position: request.Position, ParentID: parentID, RateLimitPerUser: request.SlowmodeSeconds, PermissionOverwrites: overwrites}, nil
	case "media":
		return disgoDiscord.GuildMediaChannelCreate{Name: request.Name, Topic: request.Topic, Position: request.Position, ParentID: parentID, RateLimitPerUser: request.SlowmodeSeconds, PermissionOverwrites: overwrites}, nil
	default:
		return disgoDiscord.GuildTextChannelCreate{Name: request.Name, Topic: request.Topic, RateLimitPerUser: request.SlowmodeSeconds, Position: request.Position, ParentID: parentID, NSFW: request.NSFW, PermissionOverwrites: overwrites}, nil
	}
}

func channelUpdatePayload(request setup.ChannelApplyRequest) (disgoDiscord.ChannelUpdate, error) {
	parentID, err := optionalID(request.ParentID)
	if err != nil {
		return nil, err
	}
	var parentPtr *snowflake.ID
	if strings.TrimSpace(request.ParentID) != "" {
		parentPtr = &parentID
	}
	overwrites, err := permissionOverwrites(request.Overwrites)
	if err != nil {
		return nil, err
	}
	name := request.Name
	position := request.Position
	slowmode := request.SlowmodeSeconds
	topic := request.Topic
	nsfw := request.NSFW
	switch strings.ToLower(strings.TrimSpace(request.Type)) {
	case "category":
		return disgoDiscord.GuildCategoryChannelUpdate{Name: &name, Position: &position, PermissionOverwrites: &overwrites}, nil
	case "voice":
		return disgoDiscord.GuildVoiceChannelUpdate{Name: &name, Position: &position, ParentID: parentPtr, RateLimitPerUser: &slowmode, Bitrate: &request.Bitrate, UserLimit: &request.UserLimit, PermissionOverwrites: &overwrites}, nil
	case "stage":
		return disgoDiscord.GuildStageVoiceChannelUpdate{Name: &name, Position: &position, ParentID: parentPtr, RateLimitPerUser: &slowmode, Bitrate: &request.Bitrate, UserLimit: &request.UserLimit, PermissionOverwrites: &overwrites}, nil
	case "announcement":
		return disgoDiscord.GuildNewsChannelUpdate{Name: &name, Position: &position, ParentID: parentPtr, RateLimitPerUser: &slowmode, Topic: &topic, PermissionOverwrites: &overwrites}, nil
	case "forum":
		return disgoDiscord.GuildForumChannelUpdate{Name: &name, Position: &position, ParentID: parentPtr, RateLimitPerUser: &slowmode, Topic: &topic, NSFW: &nsfw, PermissionOverwrites: &overwrites}, nil
	case "media":
		return disgoDiscord.GuildMediaChannelUpdate{Name: &name, Position: &position, ParentID: parentPtr, RateLimitPerUser: &slowmode, Topic: &topic, NSFW: &nsfw, PermissionOverwrites: &overwrites}, nil
	default:
		return disgoDiscord.GuildTextChannelUpdate{Name: &name, Position: &position, ParentID: parentPtr, RateLimitPerUser: &slowmode, Topic: &topic, NSFW: &nsfw, PermissionOverwrites: &overwrites}, nil
	}
}

func permissionOverwrites(input []setup.ResolvedOverwrite) ([]disgoDiscord.PermissionOverwrite, error) {
	overwrites := make([]disgoDiscord.PermissionOverwrite, 0, len(input))
	for _, item := range input {
		id, err := snowflake.Parse(strings.TrimSpace(item.TargetID))
		if err != nil {
			return nil, err
		}
		allow := permissionBits(item.Allow)
		deny := permissionBits(item.Deny)
		switch strings.ToLower(strings.TrimSpace(item.TargetType)) {
		case "member", "user":
			overwrites = append(overwrites, disgoDiscord.MemberPermissionOverwrite{UserID: id, Allow: allow, Deny: deny})
		default:
			overwrites = append(overwrites, disgoDiscord.RolePermissionOverwrite{RoleID: id, Allow: allow, Deny: deny})
		}
	}
	return overwrites, nil
}

func messageComponents(input []setup.MessageComponent) []disgoDiscord.LayoutComponent {
	var rows []disgoDiscord.LayoutComponent
	var row []disgoDiscord.InteractiveComponent
	for _, component := range input {
		if strings.EqualFold(strings.TrimSpace(component.Type), "select") {
			if len(row) > 0 {
				rows = append(rows, disgoDiscord.NewActionRow(row...))
				row = nil
			}
			if selectMenu, ok := stringSelectComponent(component); ok {
				rows = append(rows, disgoDiscord.NewActionRow(selectMenu))
			}
			continue
		}
		if len(row) == 5 {
			rows = append(rows, disgoDiscord.NewActionRow(row...))
			row = nil
		}
		label := strings.TrimSpace(component.Label)
		if label == "" {
			label = "Open"
		}
		switch strings.ToLower(strings.TrimSpace(component.Style)) {
		case "danger":
			row = append(row, disgoDiscord.NewDangerButton(label, component.CustomID))
		case "success":
			row = append(row, disgoDiscord.NewSuccessButton(label, component.CustomID))
		case "secondary":
			row = append(row, disgoDiscord.NewSecondaryButton(label, component.CustomID))
		default:
			row = append(row, disgoDiscord.NewPrimaryButton(label, component.CustomID))
		}
	}
	if len(row) > 0 {
		rows = append(rows, disgoDiscord.NewActionRow(row...))
	}
	return rows
}

func stringSelectComponent(component setup.MessageComponent) (disgoDiscord.StringSelectMenuComponent, bool) {
	options := make([]disgoDiscord.StringSelectMenuOption, 0, len(component.Options))
	for _, option := range component.Options {
		label := strings.TrimSpace(option.Label)
		value := strings.TrimSpace(option.Value)
		if label == "" || value == "" {
			continue
		}
		item := disgoDiscord.NewStringSelectMenuOption(label, value)
		if description := strings.TrimSpace(option.Description); description != "" {
			item = item.WithDescription(description)
		}
		if option.Default {
			item = item.WithDefault(true)
		}
		options = append(options, item)
		if len(options) == 25 {
			break
		}
	}
	if len(options) == 0 {
		return disgoDiscord.StringSelectMenuComponent{}, false
	}
	placeholder := strings.TrimSpace(component.Placeholder)
	if placeholder == "" {
		placeholder = "Choose an option"
	}
	selectMenu := disgoDiscord.NewStringSelectMenu(component.CustomID, placeholder, options...)
	minValues := component.MinValues
	if minValues < 0 {
		minValues = 0
	}
	maxValues := component.MaxValues
	if maxValues <= 0 || maxValues > len(options) {
		maxValues = len(options)
	}
	selectMenu = selectMenu.WithMinValues(minValues).WithMaxValues(maxValues)
	return selectMenu, true
}

func optionalID(value string) (snowflake.ID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	return snowflake.Parse(value)
}

func guildUserRoleIDs(guildIDValue, userIDValue, roleIDValue string) (snowflake.ID, snowflake.ID, snowflake.ID, error) {
	guildID, err := snowflake.Parse(strings.TrimSpace(guildIDValue))
	if err != nil {
		return 0, 0, 0, err
	}
	userID, err := snowflake.Parse(strings.TrimSpace(userIDValue))
	if err != nil {
		return 0, 0, 0, err
	}
	roleID, err := snowflake.Parse(strings.TrimSpace(roleIDValue))
	if err != nil {
		return 0, 0, 0, err
	}
	return guildID, userID, roleID, nil
}

func setupReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Panda server setup"
	}
	if len([]rune(reason)) > 512 {
		reason = string([]rune(reason)[:512])
	}
	return reason
}

func setupChannelType(channelType disgoDiscord.ChannelType) string {
	switch channelType {
	case disgoDiscord.ChannelTypeGuildCategory:
		return "category"
	case disgoDiscord.ChannelTypeGuildVoice:
		return "voice"
	case disgoDiscord.ChannelTypeGuildStageVoice:
		return "stage"
	case disgoDiscord.ChannelTypeGuildNews:
		return "announcement"
	case disgoDiscord.ChannelTypeGuildForum:
		return "forum"
	case disgoDiscord.ChannelTypeGuildMedia:
		return "media"
	default:
		return "text"
	}
}

func splitPermissionNames(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '|'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			result = append(result, field)
		}
	}
	return result
}
