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
	if action == "search" {
		if intent.Query == "" {
			return m.musicToolErrorResult(ctx, ActionPlay, intent.Query, ErrMissingSong), nil
		}
		return m.musicSearchSelectionResult(ctx, request, intent), nil
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
		return m.musicToolErrorResult(ctx, action, intent.Query, err), nil
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

func (m *Manager) musicToolErrorResult(ctx context.Context, action Action, query string, err error) map[string]any {
	title := "Music request failed"
	content := "I couldn't complete that music request. Please try again."
	suggestions := []Track{}
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
		suggestions = m.musicSuggestions(ctx, query, 3)
		content = "I couldn't find that track. Try a different title or link."
	case errors.Is(err, ErrTrackStreamFailed):
		title = "Track stream failed"
		suggestions = m.musicSuggestions(ctx, query, 3)
		content = "I found the track, but the audio stream failed. Try again or pick another result."
	}
	if len(suggestions) > 0 {
		content += " Possible matches: " + musicSuggestionSummary(suggestions) + "."
	}
	return map[string]any{"result": map[string]any{
		"ok":          false,
		"action":      string(action),
		"title":       title,
		"content":     content,
		"accent":      "warning",
		"error":       err.Error(),
		"suggestions": musicSuggestionPayloads(suggestions),
		"fields":      []map[string]any{},
		"actions":     []map[string]string{},
	}}
}

func (m *Manager) musicSuggestions(ctx context.Context, query string, limit int) []Track {
	if strings.TrimSpace(query) == "" || m == nil || m.resolver == nil {
		return nil
	}
	resolver, ok := m.resolver.(SuggestingResolver)
	if !ok {
		return nil
	}
	suggestions, err := resolver.Suggestions(ctx, query, limit)
	if err != nil {
		return nil
	}
	return suggestions
}

func (m *Manager) musicSearchSelectionResult(ctx context.Context, request toolsvc.MusicManagementRequest, intent Intent) map[string]any {
	suggestions := m.musicSuggestions(ctx, intent.Query, 3)
	options := musicSelectionOptions(suggestions, request.VoiceChannelID, intent.Mode)
	if len(options) == 0 {
		return map[string]any{"result": map[string]any{
			"ok":      false,
			"action":  "search",
			"title":   "No track choices found",
			"content": "I could not find usable YouTube results for that track. Try a more specific title, artist, or link.",
			"accent":  "warning",
			"fields":  []map[string]any{},
			"actions": []map[string]string{},
		}}
	}
	return map[string]any{"result": map[string]any{
		"ok":       true,
		"action":   "search",
		"title":    "Choose a track",
		"content":  "I found a few possible matches. Pick the one you want me to play.",
		"accent":   "music",
		"terminal": true,
		"fields":   []map[string]any{},
		"actions":  []map[string]string{},
		"selection": map[string]any{
			"placeholder": "Choose a track",
			"options":     options,
		},
	}}
}

func musicSelectionOptions(tracks []Track, voiceChannelID, mode string) []map[string]any {
	options := make([]map[string]any, 0, len(tracks))
	for _, track := range tracks {
		url := strings.TrimSpace(track.URL)
		if url == "" {
			continue
		}
		index := len(options) + 1
		options = append(options, map[string]any{
			"label":            trackTitle(track),
			"description":      musicSelectionDescription(track),
			"value":            fmt.Sprintf("track_%d", index),
			"url":              url,
			"thumbnail_url":    track.ThumbnailURL,
			"command":          "chat",
			"prompt":           musicSelectionPrompt(mode, url),
			"voice_channel_id": voiceChannelID,
		})
		if len(options) == 3 {
			break
		}
	}
	return options
}

func musicSelectionDescription(track Track) string {
	parts := []string{}
	if uploader := strings.TrimSpace(track.Uploader); uploader != "" {
		parts = append(parts, uploader)
	}
	if suffix := strings.TrimSpace(durationSuffix(track.Duration)); suffix != "" {
		parts = append(parts, strings.Trim(suffix, "` "))
	}
	return strings.Join(parts, " - ")
}

func musicSelectionPrompt(mode, url string) string {
	if strings.EqualFold(strings.TrimSpace(mode), string(ActionSkipPlay)) {
		return "Skip the current track and play this exact YouTube result: " + url
	}
	return "Play this exact YouTube result: " + url
}

func musicSuggestionPayloads(tracks []Track) []map[string]any {
	payloads := make([]map[string]any, 0, len(tracks))
	for _, track := range tracks {
		payload := map[string]any{
			"title":         trackTitle(track),
			"url":           track.URL,
			"uploader":      track.Uploader,
			"thumbnail_url": track.ThumbnailURL,
		}
		if track.Duration > 0 {
			payload["duration_seconds"] = int(track.Duration.Seconds())
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func musicSuggestionSummary(tracks []Track) string {
	parts := make([]string, 0, len(tracks))
	for _, track := range tracks {
		label := trackTitle(track)
		if track.Uploader != "" {
			label += " by " + track.Uploader
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; ")
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
