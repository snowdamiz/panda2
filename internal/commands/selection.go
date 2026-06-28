package commands

import (
	"strings"
	"sync"
	"time"
)

const (
	selectionPrefix        = "p2s"
	selectionOpPick        = "pick"
	selectionRequestOption = "_selection"
	selectionTTL           = 30 * time.Minute
)

type pendingSelection struct {
	userID    string
	options   map[string]SelectionOption
	expiresAt time.Time
}

type pendingSelectionStore struct {
	mu    sync.Mutex
	items map[string]pendingSelection
}

var pendingSelections = &pendingSelectionStore{items: map[string]pendingSelection{}}

func PrepareSelectionForUser(userID string, selection *Selection) *Selection {
	if selection == nil || len(selection.Options) == 0 {
		return nil
	}
	id := pendingSelections.put(userID, selection.Options)
	if id == "" {
		return nil
	}
	prepared := &Selection{
		ID:          id,
		Placeholder: strings.TrimSpace(selection.Placeholder),
		Options:     make([]SelectionOption, 0, len(selection.Options)),
	}
	seen := map[string]struct{}{}
	for _, option := range selection.Options {
		option = displaySelectionOption(option)
		if option.Label == "" || option.Value == "" {
			continue
		}
		if _, ok := seen[option.Value]; ok {
			continue
		}
		seen[option.Value] = struct{}{}
		prepared.Options = append(prepared.Options, option)
	}
	if len(prepared.Options) == 0 {
		return nil
	}
	return prepared
}

func RequestFromSelectionID(id string, values []string, base Request) (Request, bool) {
	if len(values) == 0 {
		return Request{}, false
	}
	selectedValue := strings.TrimSpace(values[0])
	if selectedValue == "" {
		return Request{}, false
	}
	option, ok := pendingSelections.take(id, base.UserID, selectedValue)
	if !ok {
		return Request{}, false
	}
	command := strings.TrimSpace(option.Command)
	if command == "" {
		command = "chat"
	}
	prompt := strings.TrimSpace(option.Prompt)
	if prompt == "" {
		return Request{}, false
	}
	base.Command = command
	base.Subcommand = ""
	base.Options = map[string]string{
		"question":             prompt,
		selectionRequestOption: "true",
	}
	if voiceChannelID := strings.TrimSpace(option.VoiceChannelID); voiceChannelID != "" {
		base.VoiceChannelID = voiceChannelID
	}
	return base, true
}

func displaySelectionOption(option SelectionOption) SelectionOption {
	return SelectionOption{
		Label:        strings.TrimSpace(option.Label),
		Description:  strings.TrimSpace(option.Description),
		Value:        strings.TrimSpace(option.Value),
		URL:          strings.TrimSpace(option.URL),
		ThumbnailURL: strings.TrimSpace(option.ThumbnailURL),
	}
}

func (s *pendingSelectionStore) put(userID string, options []SelectionOption) string {
	token, ok := randomConfirmationToken()
	if !ok {
		return ""
	}
	id := strings.Join([]string{selectionPrefix, selectionOpPick, cleanConfirmationPart(userID), token}, ":")
	items := map[string]SelectionOption{}
	for _, option := range options {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			continue
		}
		items[value] = option
	}
	if len(items) == 0 {
		return ""
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.items[id] = pendingSelection{
		userID:    cleanConfirmationPart(userID),
		options:   cloneSelectionOptions(items),
		expiresAt: now.Add(selectionTTL),
	}
	return id
}

func (s *pendingSelectionStore) take(id, userID, selectedValue string) (SelectionOption, bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 4 || parts[0] != selectionPrefix || parts[1] != selectionOpPick || parts[2] != cleanConfirmationPart(userID) {
		return SelectionOption{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.pruneLocked(now)
	item, ok := s.items[id]
	if !ok {
		return SelectionOption{}, false
	}
	if now.After(item.expiresAt) {
		delete(s.items, id)
		return SelectionOption{}, false
	}
	if item.userID != cleanConfirmationPart(userID) {
		return SelectionOption{}, false
	}
	delete(s.items, id)
	option, ok := item.options[strings.TrimSpace(selectedValue)]
	return option, ok
}

func (s *pendingSelectionStore) pruneLocked(now time.Time) {
	for id, item := range s.items {
		if now.After(item.expiresAt) {
			delete(s.items, id)
		}
	}
}

func cloneSelectionOptions(options map[string]SelectionOption) map[string]SelectionOption {
	clone := make(map[string]SelectionOption, len(options))
	for key, value := range options {
		clone[key] = value
	}
	return clone
}
