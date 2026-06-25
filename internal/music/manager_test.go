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

type fakeVoiceConnector struct {
	guildIDs   []string
	channelIDs []string
	session    *fakeVoiceSession
}

func (c *fakeVoiceConnector) Connect(_ context.Context, guildID string, channelID string) (VoiceSession, error) {
	c.guildIDs = append(c.guildIDs, guildID)
	c.channelIDs = append(c.channelIDs, channelID)
	if c.session != nil {
		return c.session, nil
	}
	return newFakeVoiceSession(channelID), nil
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

func TestManageMusicJoinConnectsToVoice(t *testing.T) {
	connector := &fakeVoiceConnector{}
	manager := NewManager(nil, nil, connector, nil)

	result, err := manager.ManageMusic(context.Background(), toolsvc.MusicManagementRequest{
		GuildID:        "guild-1",
		ChannelID:      "text-1",
		VoiceChannelID: "voice-1",
		ActorID:        "user-1",
		Action:         "join",
	})
	if err != nil {
		t.Fatalf("ManageMusic join: %v", err)
	}
	root, ok := result.(map[string]any)
	payload, ok := root["result"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected music result: %+v", result)
	}
	if payload["action"] != "join" || payload["title"] != "Connected to voice" {
		t.Fatalf("unexpected join payload: %+v", payload)
	}
	content, _ := payload["content"].(string)
	if !strings.Contains(content, "<#voice-1>") {
		t.Fatalf("expected voice channel mention in content, got %q", content)
	}
	if manager.ActiveVoiceChannelID("guild-1") != "voice-1" {
		t.Fatalf("expected active voice channel to be tracked, got %q", manager.ActiveVoiceChannelID("guild-1"))
	}
	if len(connector.guildIDs) != 1 || connector.guildIDs[0] != "guild-1" || connector.channelIDs[0] != "voice-1" {
		t.Fatalf("unexpected connector calls: %+v %+v", connector.guildIDs, connector.channelIDs)
	}
}

func TestManageMusicJoinRequiresUserVoiceChannel(t *testing.T) {
	manager := NewManager(nil, nil, &fakeVoiceConnector{}, nil)

	_, err := manager.ManageMusic(context.Background(), toolsvc.MusicManagementRequest{
		GuildID: "guild-1",
		ActorID: "user-1",
		Action:  "join",
	})
	if err == nil {
		t.Fatal("expected missing voice channel error")
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
