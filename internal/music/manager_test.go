package music

import (
	"context"
	"sync"
	"testing"
	"time"
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
