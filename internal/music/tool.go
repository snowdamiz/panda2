package music

import (
	"context"
	"errors"
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
	if (action == ActionPlay || action == ActionSkipPlay) && intent.Query == "" {
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
		return musicToolErrorResult(action, err), nil
	}
	return map[string]any{"result": map[string]any{
		"ok":      true,
		"action":  string(action),
		"title":   response.Title,
		"content": response.Content,
		"accent":  "music",
		"url":     response.URL,
		"fields":  musicToolFields(response.Fields),
		"actions": musicToolActions(response.Actions),
	}}, nil
}

func musicToolErrorResult(action Action, err error) map[string]any {
	title := "Music request failed"
	content := "I couldn't complete that music request. Please try again."
	switch {
	case errors.Is(err, ErrMissingVoice):
		title = "Voice channel needed"
		content = "Tell me which voice channel to join, or join a voice channel first, then ask me to play it again."
	case errors.Is(err, ErrVoiceConnection):
		title = "Voice connection failed"
		content = "Discord voice did not finish setting up the secure media session in time. I cleaned up the failed connection; please try the song again in a moment."
	case errors.Is(err, ErrMissingSong):
		title = "Song needed"
		content = "Tell me what song to play."
	case errors.Is(err, ErrNothingPlaying):
		title = "Nothing playing"
		content = "There is not a track playing right now."
	case errors.Is(err, ErrDifferentVoice):
		title = "Different voice channel"
		content = "I am already playing music in another voice channel."
	case errors.Is(err, ErrTrackLookupFailed):
		title = "Track lookup failed"
		content = "I couldn't find that track. Try a different title or link."
	case errors.Is(err, ErrTrackStreamFailed):
		title = "Track stream failed"
		content = "I found the track, but the audio stream failed. Try again or pick another result."
	}
	return map[string]any{"result": map[string]any{
		"ok":      false,
		"action":  string(action),
		"title":   title,
		"content": content,
		"accent":  "warning",
		"error":   err.Error(),
		"fields":  []map[string]any{},
		"actions": []map[string]string{},
	}}
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
