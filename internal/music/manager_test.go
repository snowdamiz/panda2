package music

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	toolsvc "github.com/sn0w/panda2/internal/tools"
)

type fakeVoiceSession struct {
	channelID string

	closeOnce sync.Once
	closedCh  chan struct{}
}

func newFakeVoiceSession(channelID string) *fakeVoiceSession {
	return &fakeVoiceSession{channelID: channelID, closedCh: make(chan struct{})}
}

func (s *fakeVoiceSession) ChannelID() string {
	return s.channelID
}

func (s *fakeVoiceSession) SetSpeaking(context.Context, bool) error {
	return nil
}

func (s *fakeVoiceSession) WriteOpus(context.Context, []byte) error {
	return nil
}

func (s *fakeVoiceSession) Close(context.Context) {
	s.closeOnce.Do(func() {
		close(s.closedCh)
	})
}

func TestManagerDisconnectsAfterVoiceChannelEmpties(t *testing.T) {
	manager, session := newTestManagerWithPlayer("guild-1", "voice-1")
	manager.emptyVoiceDisconnectWait = 5 * time.Millisecond

	manager.UpdateVoiceOccupancy("guild-1", "voice-1", false)

	select {
	case <-session.closedCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected empty voice channel to disconnect")
	}
}

func TestManagerCancelsEmptyDisconnectWhenListenerReturns(t *testing.T) {
	manager, session := newTestManagerWithPlayer("guild-1", "voice-1")
	manager.emptyVoiceDisconnectWait = 25 * time.Millisecond

	manager.UpdateVoiceOccupancy("guild-1", "voice-1", false)
	manager.UpdateVoiceOccupancy("guild-1", "voice-1", true)

	select {
	case <-session.closedCh:
		t.Fatal("expected occupied voice channel to keep playing")
	case <-time.After(75 * time.Millisecond):
	}
}

func TestManagerIgnoresEmptyUpdateForDifferentVoiceChannel(t *testing.T) {
	manager, session := newTestManagerWithPlayer("guild-1", "voice-1")
	manager.emptyVoiceDisconnectWait = 5 * time.Millisecond

	manager.UpdateVoiceOccupancy("guild-1", "voice-2", false)

	select {
	case <-session.closedCh:
		t.Fatal("expected unrelated voice channel update to be ignored")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestManageMusicControlsReturnsToolPayload(t *testing.T) {
	manager := NewManager(nil, nil, nil, nil)

	result, err := manager.ManageMusic(context.Background(), toolsvc.MusicManagementRequest{
		GuildID: "guild-1",
		ActorID: "user-1",
		Action:  "controls",
	})
	if err != nil {
		t.Fatalf("ManageMusic: %v", err)
	}
	root, ok := result.(map[string]any)
	payload, ok := root["result"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected music result: %+v", result)
	}
	if payload["action"] != "controls" || payload["title"] != "Music controls" {
		t.Fatalf("unexpected music payload: %+v", payload)
	}
	content, _ := payload["content"].(string)
	if !strings.Contains(content, "play <song>") || !strings.Contains(content, "queue") {
		t.Fatalf("unexpected music controls content: %q", content)
	}
}

func newTestManagerWithPlayer(guildID string, voiceChannelID string) (*Manager, *fakeVoiceSession) {
	manager := NewManager(nil, nil, nil, nil)
	session := newFakeVoiceSession(voiceChannelID)
	manager.players[guildID] = &guildPlayer{
		manager:        manager,
		guildID:        guildID,
		session:        session,
		voiceChannelID: voiceChannelID,
		current:        &Track{Title: "test track"},
		playing:        true,
	}
	return manager, session
}
