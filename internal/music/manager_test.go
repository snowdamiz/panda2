package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/store"
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

type fakeResolver struct {
	track Track
}

func (r fakeResolver) Resolve(_ context.Context, query string) (Track, error) {
	track := r.track
	if track.Title == "" {
		track.Title = query
	}
	return track, nil
}

type fakeSuggestingResolver struct {
	track       Track
	suggestions []Track
	err         error
}

func (r fakeSuggestingResolver) Resolve(_ context.Context, query string) (Track, error) {
	track := r.track
	if track.Title == "" {
		track.Title = query
	}
	return track, nil
}

func (r fakeSuggestingResolver) Suggestions(context.Context, string, int) ([]Track, error) {
	return r.suggestions, r.err
}

type eofStreamer struct{}

func (eofStreamer) Stream(context.Context, Track) (OpusFrameProvider, error) {
	return eofOpusFrameProvider{}, nil
}

type failingStreamer struct {
	err error
}

func (s failingStreamer) Stream(context.Context, Track) (OpusFrameProvider, error) {
	return nil, s.err
}

type frameStreamer struct {
	count int
}

func (s frameStreamer) Stream(context.Context, Track) (OpusFrameProvider, error) {
	return &frameOpusFrameProvider{remaining: s.count}, nil
}

type eofOpusFrameProvider struct{}

func (eofOpusFrameProvider) ProvideOpusFrame() ([]byte, error) {
	return nil, io.EOF
}

func (eofOpusFrameProvider) Close() {}

type frameOpusFrameProvider struct {
	remaining int
}

func (p *frameOpusFrameProvider) ProvideOpusFrame() ([]byte, error) {
	if p.remaining <= 0 {
		return nil, io.EOF
	}
	p.remaining--
	return []byte{0x01, 0x02, 0x03}, nil
}

func (p *frameOpusFrameProvider) Close() {}

type recordingStreamer struct {
	mu      sync.Mutex
	tracks  []Track
	factory func(Track) OpusFrameProvider
}

func (s *recordingStreamer) Stream(_ context.Context, track Track) (OpusFrameProvider, error) {
	s.mu.Lock()
	s.tracks = append(s.tracks, track)
	s.mu.Unlock()
	if s.factory != nil {
		return s.factory(track), nil
	}
	return &frameOpusFrameProvider{remaining: playbackPrebufferFrames}, nil
}

func (s *recordingStreamer) playedTracks() []Track {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Track(nil), s.tracks...)
}

type infiniteOpusFrameProvider struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func newInfiniteOpusFrameProvider() *infiniteOpusFrameProvider {
	return &infiniteOpusFrameProvider{closed: make(chan struct{})}
}

func (p *infiniteOpusFrameProvider) ProvideOpusFrame() ([]byte, error) {
	select {
	case <-p.closed:
		return nil, io.EOF
	default:
		return []byte{0x01, 0x02, 0x03}, nil
	}
}

func (p *infiniteOpusFrameProvider) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
	})
}

type fakeVoiceConnector struct {
	mu       sync.Mutex
	sessions []*fakeVoiceSession
}

func (c *fakeVoiceConnector) Connect(_ context.Context, _ string, channelID string) (VoiceSession, error) {
	session := newFakeVoiceSession(channelID)
	c.mu.Lock()
	c.sessions = append(c.sessions, session)
	c.mu.Unlock()
	return session, nil
}

type failingVoiceConnector struct {
	err error
}

func (c failingVoiceConnector) Connect(context.Context, string, string) (VoiceSession, error) {
	return nil, c.err
}

type fakeMusicStore struct {
	mu       sync.Mutex
	queue    []store.MusicQueueItem
	settings store.MusicSettings
}

func (s *fakeMusicStore) ReplaceQueue(_ context.Context, guildID string, items []store.MusicQueueItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append([]store.MusicQueueItem(nil), items...)
	for index := range s.queue {
		s.queue[index].GuildID = guildID
		s.queue[index].Position = index + 1
	}
	return nil
}

func (s *fakeMusicStore) Queue(context.Context, string) ([]store.MusicQueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.MusicQueueItem(nil), s.queue...), nil
}

func (s *fakeMusicStore) ClearQueue(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = nil
	return nil
}

func (s *fakeMusicStore) EnsureSettings(context.Context, string) (store.MusicSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings.LoopMode == "" {
		s.settings = store.MusicSettings{LoopMode: "off", DefaultVolume: 100, VoteSkipThreshold: 0.5}
	}
	return s.settings, nil
}

func (s *fakeMusicStore) UpdateSettings(_ context.Context, _ string, values map[string]any) (store.MusicSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings.LoopMode == "" {
		s.settings = store.MusicSettings{LoopMode: "off", DefaultVolume: 100, VoteSkipThreshold: 0.5}
	}
	if mode, ok := values["loop_mode"].(string); ok {
		s.settings.LoopMode = mode
	}
	if volume, ok := values["default_volume"].(int); ok {
		s.settings.DefaultVolume = volume
	}
	return s.settings, nil
}

func (s *fakeMusicStore) SavePlaylist(context.Context, store.MusicPlaylist) (store.MusicPlaylist, error) {
	return store.MusicPlaylist{}, nil
}

func (s *fakeMusicStore) Playlist(context.Context, string, string) (store.MusicPlaylist, bool, error) {
	return store.MusicPlaylist{}, false, nil
}

func (s *fakeMusicStore) Playlists(context.Context, string, int) ([]store.MusicPlaylist, error) {
	return nil, nil
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

func TestSkipWithEmptyQueueKeepsVoiceForImmediatePlay(t *testing.T) {
	connector := &fakeVoiceConnector{}
	manager := NewManager(fakeResolver{track: Track{Title: "next track"}}, eofStreamer{}, connector, nil)
	oldSession := newFakeVoiceSession("voice-1")
	cancelled := false
	manager.players["guild-1"] = &guildPlayer{
		manager:        manager,
		guildID:        "guild-1",
		session:        oldSession,
		voiceChannelID: "voice-1",
		current:        &Track{Title: "old track"},
		playing:        true,
		trackCancel: func() {
			cancelled = true
		},
	}

	skipResponse, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent:         Intent{Action: ActionSkip},
	})
	if err != nil {
		t.Fatalf("skip: %v", err)
	}
	if skipResponse.Title != "Track skipped" || !strings.Contains(skipResponse.Content, "queue is empty") {
		t.Fatalf("unexpected skip response: %+v", skipResponse)
	}
	if !cancelled {
		t.Fatal("expected skip to cancel the active track")
	}
	select {
	case <-oldSession.closedCh:
		t.Fatal("expected skip with an empty queue to keep voice briefly for a replacement track")
	default:
	}
	player := manager.existingPlayer("guild-1")
	if player == nil {
		t.Fatal("expected empty-queue skip to keep the player for a replacement track")
	}
	player.mu.Lock()
	player.current = nil
	player.trackCancel = nil
	player.mu.Unlock()

	nextTrack := make(chan Track, 1)
	go func() {
		track, ok := player.nextTrack()
		if ok {
			nextTrack <- track
		}
	}()

	playResponse, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "next track",
		},
	})
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if playResponse.Title != "Starting track" {
		t.Fatalf("expected immediate play to reuse existing voice session, got %+v", playResponse)
	}
	select {
	case track := <-nextTrack:
		if track.Title != "next track" {
			t.Fatalf("expected replacement track next, got %+v", track)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected existing playback loop to pick up replacement track")
	}
}

func TestSkipPlayReplacesCurrentTrackWithoutLeavingVoice(t *testing.T) {
	connector := &fakeVoiceConnector{}
	streamer := &recordingStreamer{
		factory: func(Track) OpusFrameProvider {
			return newInfiniteOpusFrameProvider()
		},
	}
	manager := NewManager(fakeResolver{}, streamer, connector, nil)
	t.Cleanup(func() {
		manager.Close(context.Background())
	})

	_, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "old track",
		},
	})
	if err != nil {
		t.Fatalf("play old track: %v", err)
	}

	response, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionSkipPlay,
			Query:  "replacement track",
		},
	})
	if err != nil {
		t.Fatalf("skip_play: %v", err)
	}
	if response.Title != "Track replaced" || !strings.Contains(response.Content, "replacement track") {
		t.Fatalf("unexpected skip_play response: %+v", response)
	}
	tracks := streamer.playedTracks()
	if len(tracks) < 2 || tracks[0].Title != "old track" || tracks[1].Title != "replacement track" {
		t.Fatalf("expected old track then replacement to start, got %+v", tracks)
	}
	if len(connector.sessions) != 1 {
		t.Fatalf("expected skip_play to reuse the existing voice session, got %d session(s)", len(connector.sessions))
	}
	session := connector.sessions[0]
	select {
	case <-session.closedCh:
		t.Fatal("expected skip_play to keep the voice session open")
	default:
	}
}

func TestSkipPlayReturnsFailureWhenReplacementHasNoAudioFrames(t *testing.T) {
	streamer := &recordingStreamer{
		factory: func(track Track) OpusFrameProvider {
			if track.Title == "replacement track" {
				return eofOpusFrameProvider{}
			}
			return newInfiniteOpusFrameProvider()
		},
	}
	manager := NewManager(fakeResolver{}, streamer, &fakeVoiceConnector{}, nil)
	t.Cleanup(func() {
		manager.Close(context.Background())
	})

	_, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "old track",
		},
	})
	if err != nil {
		t.Fatalf("play old track: %v", err)
	}

	response, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionSkipPlay,
			Query:  "replacement track",
		},
	})
	if !errors.Is(err, ErrTrackStreamFailed) {
		t.Fatalf("expected replacement stream failure, got response=%+v err=%v", response, err)
	}
	if response.Title != "" || response.Content != "" {
		t.Fatalf("failed replacement should not return a success response: %+v", response)
	}
}

func TestQueuedTrackDropsTransientStreamURL(t *testing.T) {
	manager := NewManager(fakeResolver{track: Track{
		Title:         "queued track",
		URL:           "https://example.com/watch",
		StreamURL:     "https://media.example.com/audio",
		StreamHeaders: map[string]string{"User-Agent": "Panda"},
	}}, eofStreamer{}, &fakeVoiceConnector{}, nil)
	manager.players["guild-1"] = &guildPlayer{
		manager:        manager,
		guildID:        "guild-1",
		session:        newFakeVoiceSession("voice-1"),
		voiceChannelID: "voice-1",
		current:        &Track{Title: "current track"},
		playing:        true,
	}

	response, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "queued track",
		},
	})
	if err != nil {
		t.Fatalf("play queued track: %v", err)
	}
	if response.Title != "Track queued" {
		t.Fatalf("expected queued response, got %+v", response)
	}
	player := manager.existingPlayer("guild-1")
	if player == nil {
		t.Fatal("expected player")
	}
	player.mu.Lock()
	defer player.mu.Unlock()
	if len(player.queueItems) != 1 {
		t.Fatalf("expected one queued track, got %+v", player.queueItems)
	}
	if player.queueItems[0].StreamURL != "" || player.queueItems[0].StreamHeaders != nil {
		t.Fatalf("expected transient stream data to be dropped for delayed queue item, got %+v", player.queueItems[0])
	}
}

func TestPlayStartsRequestedTrackInsteadOfStalePersistedQueue(t *testing.T) {
	store := &fakeMusicStore{queue: []store.MusicQueueItem{
		{Title: "stale queued one", Position: 1},
		{Title: "stale queued two", Position: 2},
	}}
	streamer := &recordingStreamer{}
	manager := NewManager(fakeResolver{}, streamer, &fakeVoiceConnector{}, nil).WithRepository(store)
	t.Cleanup(func() {
		manager.Close(context.Background())
	})

	response, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "fresh request",
		},
	})
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if response.Title != "Connected to voice" || !strings.Contains(response.Content, "fresh request") {
		t.Fatalf("expected fresh request to start immediately, got %+v", response)
	}
	tracks := streamer.playedTracks()
	if len(tracks) == 0 || tracks[0].Title != "fresh request" {
		t.Fatalf("expected fresh request to be streamed first, got %+v", tracks)
	}
	items, err := store.Queue(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected fresh play to clear stale persisted queue, got %+v", items)
	}
}

func TestPlayWaitsForInitialPrebufferSuccess(t *testing.T) {
	manager := NewManager(
		fakeResolver{track: Track{Title: "ready track"}},
		frameStreamer{count: playbackPrebufferFrames},
		&fakeVoiceConnector{},
		nil,
	)

	response, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "ready track",
		},
	})
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	if response.Title != "Connected to voice" || !strings.Contains(response.Content, "started **ready track**") {
		t.Fatalf("expected response after startup prebuffer, got %+v", response)
	}
}

func TestPlayReturnsInitialEOFBeforeAudioAsStreamFailure(t *testing.T) {
	connector := &fakeVoiceConnector{}
	manager := NewManager(
		fakeResolver{track: Track{Title: "empty track"}},
		eofStreamer{},
		connector,
		nil,
	)

	response, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "empty track",
		},
	})
	if !errors.Is(err, ErrTrackStreamFailed) {
		t.Fatalf("expected EOF before audio to fail startup, got response=%+v err=%v", response, err)
	}
	if response.Content != "" || response.Title != "" {
		t.Fatalf("failed startup should not return a success response: %+v", response)
	}
	if len(connector.sessions) != 1 {
		t.Fatalf("expected one voice session attempt, got %+v", connector.sessions)
	}
	select {
	case <-connector.sessions[0].closedCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected empty startup to close the voice session")
	}
}

func TestPlayReturnsInitialPrebufferFailure(t *testing.T) {
	streamErr := fmt.Errorf("%w: ffmpeg: exit status 1", ErrTrackStreamFailed)
	connector := &fakeVoiceConnector{}
	manager := NewManager(
		fakeResolver{track: Track{Title: "blocked track"}},
		failingStreamer{err: streamErr},
		connector,
		nil,
	)

	response, err := manager.Handle(context.Background(), Request{
		GuildID:        "guild-1",
		TextChannelID:  "channel-1",
		UserID:         "user-1",
		VoiceChannelID: "voice-1",
		Intent: Intent{
			Action: ActionPlay,
			Query:  "blocked track",
		},
	})
	if !errors.Is(err, ErrTrackStreamFailed) {
		t.Fatalf("expected track stream failure, got response=%+v err=%v", response, err)
	}
	if response.Content != "" || response.Title != "" {
		t.Fatalf("failed startup should not return a success response: %+v", response)
	}
	if len(connector.sessions) != 1 {
		t.Fatalf("expected one voice session attempt, got %+v", connector.sessions)
	}
	select {
	case <-connector.sessions[0].closedCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected failed startup to close the voice session")
	}
}

func TestManageMusicReturnsStructuredVoiceConnectionFailure(t *testing.T) {
	manager := NewManager(
		fakeResolver{track: Track{Title: "test track"}},
		eofStreamer{},
		failingVoiceConnector{err: fmt.Errorf("%w: dave media session not ready: context deadline exceeded", ErrVoiceConnection)},
		nil,
	)

	result, err := manager.ManageMusic(context.Background(), toolsvc.MusicManagementRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		ActorID:        "user-1",
		Action:         "play",
		Query:          "test track",
	})
	if err != nil {
		t.Fatalf("ManageMusic: %v", err)
	}
	root, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result root: %+v", result)
	}
	payload, ok := root["result"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected music payload: %+v", result)
	}
	if payload["ok"] != false || payload["title"] != "Voice connection failed" || payload["accent"] != "warning" {
		t.Fatalf("expected structured voice failure payload, got %+v", payload)
	}
	content, _ := payload["content"].(string)
	if !strings.Contains(content, "secure media session") || strings.Contains(strings.ToLower(content), "join a voice channel") {
		t.Fatalf("unexpected voice failure content: %q", content)
	}
}

func TestManageMusicReturnsSuggestionsForStreamFailure(t *testing.T) {
	manager := NewManager(
		fakeSuggestingResolver{
			track: Track{Title: "bad stream"},
			suggestions: []Track{
				{Title: "Better Match", Uploader: "Artist", URL: "https://example.test/watch"},
				{Title: "Live Version", Uploader: "Artist"},
			},
		},
		failingStreamer{err: fmt.Errorf("%w: ffmpeg decode failed", ErrTrackStreamFailed)},
		&fakeVoiceConnector{},
		nil,
	)

	result, err := manager.ManageMusic(context.Background(), toolsvc.MusicManagementRequest{
		GuildID:        "guild-1",
		ChannelID:      "channel-1",
		VoiceChannelID: "voice-1",
		ActorID:        "user-1",
		Action:         "play",
		Query:          "bad stream",
	})
	if err != nil {
		t.Fatalf("ManageMusic: %v", err)
	}
	payload := result.(map[string]any)["result"].(map[string]any)
	if payload["ok"] != false || payload["title"] != "Track stream failed" {
		t.Fatalf("expected structured stream failure payload, got %+v", payload)
	}
	content, _ := payload["content"].(string)
	if !strings.Contains(content, "Better Match by Artist") {
		t.Fatalf("expected content to include suggested tracks, got %q", content)
	}
	suggestions, ok := payload["suggestions"].([]map[string]any)
	if !ok || len(suggestions) != 2 || suggestions[0]["title"] != "Better Match" {
		t.Fatalf("expected structured suggestions, got %+v", payload["suggestions"])
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
