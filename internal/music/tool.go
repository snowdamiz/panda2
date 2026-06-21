package music

import (
	"context"
	"fmt"
	"strings"

	toolsvc "github.com/sn0w/panda2/internal/tools"
)

func (m *Manager) ManageMusic(ctx context.Context, request toolsvc.MusicManagementRequest) (any, error) {
	if m == nil {
		return nil, fmt.Errorf("music manager is not configured")
	}
	action := Action(strings.ToLower(strings.TrimSpace(request.Action)))
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}
	intent := Intent{
		Action:   action,
		Query:    strings.TrimSpace(request.Query),
		Mode:     strings.ToLower(strings.TrimSpace(request.Mode)),
		Name:     strings.TrimSpace(request.Name),
		Position: request.Position,
		To:       request.To,
		Volume:   request.Volume,
	}
	if action == ActionPlay && intent.Query == "" {
		return nil, ErrMissingSong
	}
	response, err := m.Handle(ctx, Request{
		GuildID:        request.GuildID,
		TextChannelID:  request.ChannelID,
		UserID:         request.ActorID,
		VoiceChannelID: request.VoiceChannelID,
		RoleIDs:        append([]string(nil), request.RoleIDs...),
		IsGuildAdmin:   request.IsGuildAdmin,
		IsOwner:        request.IsOwner,
		Intent:         intent,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": map[string]any{
		"action":  string(action),
		"title":   response.Title,
		"content": response.Content,
		"url":     response.URL,
		"fields":  musicToolFields(response.Fields),
		"actions": musicToolActions(response.Actions),
	}}, nil
}

func musicToolFields(fields []Field) []map[string]any {
	payloads := make([]map[string]any, 0, len(fields))
	for _, field := range fields {
		payloads = append(payloads, map[string]any{
			"name":   field.Name,
			"value":  field.Value,
			"inline": field.Inline,
		})
	}
	return payloads
}

func musicToolActions(actions []ResponseAction) []map[string]string {
	payloads := make([]map[string]string, 0, len(actions))
	for _, action := range actions {
		payloads = append(payloads, map[string]string{
			"label": action.Label,
			"url":   action.URL,
		})
	}
	return payloads
}
