package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const (
	frameDuration            = 20 * time.Millisecond
	playbackBufferFrames     = 250
	playbackPrebufferFrames  = 25
	playbackPrebufferTimeout = 30 * time.Second
	playbackFrameStall       = 5 * time.Second
	emptyVoiceDisconnectWait = 10 * time.Second
)

var (
	silenceOpusFrame         = []byte{0xF8, 0xFF, 0xFE}
	emptySkipReplacementWait = 30 * time.Second
)

type Manager struct {
	resolver  Resolver
	streamer  Streamer
	connector VoiceConnector
	store     MusicStore
	logger    *slog.Logger

	mu      sync.Mutex
	players map[string]*guildPlayer

	emptyVoiceDisconnectWait time.Duration
}

type MusicStore interface {
	ReplaceQueue(ctx context.Context, guildID string, items []store.MusicQueueItem) error
	Queue(ctx context.Context, guildID string) ([]store.MusicQueueItem, error)
	ClearQueue(ctx context.Context, guildID string) error
	EnsureSettings(ctx context.Context, guildID string) (store.MusicSettings, error)
	UpdateSettings(ctx context.Context, guildID string, values map[string]any) (store.MusicSettings, error)
	SavePlaylist(ctx context.Context, playlist store.MusicPlaylist) (store.MusicPlaylist, error)
	Playlist(ctx context.Context, guildID, name string) (store.MusicPlaylist, bool, error)
	Playlists(ctx context.Context, guildID string, limit int) ([]store.MusicPlaylist, error)
}

func NewManager(resolver Resolver, streamer Streamer, connector VoiceConnector, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		resolver:  resolver,
		streamer:  streamer,
		connector: connector,
		logger:    logger,
		players:   map[string]*guildPlayer{},

		emptyVoiceDisconnectWait: emptyVoiceDisconnectWait,
	}
}

func (m *Manager) WithRepository(store MusicStore) *Manager {
	m.store = store
	return m
}

func (m *Manager) Handle(ctx context.Context, request Request) (Response, error) {
	if strings.TrimSpace(request.GuildID) == "" {
		return Response{}, ErrMissingGuild
	}
	switch request.Intent.Action {
	case ActionPlay:
		return m.play(ctx, request)
	case ActionPause:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.pause(ctx)
		})
	case ActionResume:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.resume()
		})
	case ActionSkip:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.skip()
		})
	case ActionSkipPlay:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.skipPlay(ctx, request)
	case ActionStop:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.stop(ctx)
		})
	case ActionQueue:
		if player := m.existingPlayer(request.GuildID); player != nil {
			return player.queue()
		}
		return m.persistedQueue(ctx, request.GuildID)
	case ActionClear:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.clear()
		})
	case ActionNow:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.now()
		})
	case ActionLoop:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.setLoopMode(ctx, request)
	case ActionShuffle:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.shuffle()
		})
	case ActionRemove:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.remove(request.Intent.Position)
		})
	case ActionMove:
		if err := m.ensureDJ(ctx, request); err != nil {
			return Response{}, err
		}
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.move(request.Intent.Position, request.Intent.To)
		})
	case ActionVoteSkip:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.voteSkip(request.UserID)
		})
	case ActionSettings:
		if request.Intent.Volume > 0 {
			if err := m.ensureDJ(ctx, request); err != nil {
				return Response{}, err
			}
		}
		return m.musicSettings(ctx, request)
	case ActionPlaylist:
		if request.Intent.Mode == "save" || request.Intent.Mode == "load" {
			if err := m.ensureDJ(ctx, request); err != nil {
				return Response{}, err
			}
		}
		return m.playlist(ctx, request)
	case ActionControls:
		return musicResponse("Music controls", controlsMessage()), nil
	default:
		return Response{}, ErrMissingSong
	}
}

func (m *Manager) Close(ctx context.Context) {
	m.mu.Lock()
	players := make([]*guildPlayer, 0, len(m.players))
	for _, player := range m.players {
		players = append(players, player)
	}
	m.players = map[string]*guildPlayer{}
	m.mu.Unlock()
	for _, player := range players {
		_, _ = player.stop(ctx)
	}
}

func (m *Manager) ActiveVoiceChannelID(guildID string) string {
	player := m.existingPlayer(guildID)
	if player == nil {
		return ""
	}
	return player.activeVoiceChannelID()
}

func (m *Manager) UpdateVoiceOccupancy(guildID string, voiceChannelID string, hasListeners bool) {
	player := m.existingPlayer(guildID)
	if player == nil || !player.isInVoiceChannel(voiceChannelID) {
		return
	}
	if hasListeners {
		player.cancelEmptyVoiceDisconnect()
		return
	}
	player.scheduleEmptyVoiceDisconnect(voiceChannelID, m.emptyVoiceDisconnectWait)
}

func (m *Manager) play(ctx context.Context, request Request) (Response, error) {
	query := strings.TrimSpace(request.Intent.Query)
	if query == "" {
		return Response{}, ErrMissingSong
	}
	if strings.TrimSpace(request.VoiceChannelID) == "" {
		return Response{}, ErrMissingVoice
	}
	track, err := m.resolver.Resolve(ctx, query)
	if err != nil {
		m.logger.Warn("music track lookup failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("query", query))
		return Response{}, err
	}
	track.Query = query
	track.RequestedBy = request.UserID
	track.TextChannelID = request.TextChannelID
	player := m.player(request.GuildID)
	response, err := player.enqueue(ctx, track, request.VoiceChannelID)
	if err != nil {
		m.logger.Warn("music voice enqueue failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("voice_channel_id", request.VoiceChannelID), slog.String("track", track.Title))
	}
	return response, err
}

func (m *Manager) skipPlay(ctx context.Context, request Request) (Response, error) {
	query := strings.TrimSpace(request.Intent.Query)
	if query == "" {
		return Response{}, ErrMissingSong
	}
	if strings.TrimSpace(request.VoiceChannelID) == "" {
		return Response{}, ErrMissingVoice
	}
	track, err := m.resolver.Resolve(ctx, query)
	if err != nil {
		m.logger.Warn("music track lookup failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("query", query))
		return Response{}, err
	}
	track.Query = query
	track.RequestedBy = request.UserID
	track.TextChannelID = request.TextChannelID
	player := m.existingPlayer(request.GuildID)
	if player == nil {
		player = m.player(request.GuildID)
		return player.enqueue(ctx, track, request.VoiceChannelID)
	}
	response, err := player.skipAndPlay(ctx, track, request.VoiceChannelID)
	if err != nil {
		m.logger.Warn("music skip-and-play failed", slog.Any("err", err), slog.String("guild_id", request.GuildID), slog.String("voice_channel_id", request.VoiceChannelID), slog.String("track", track.Title))
	}
	return response, err
}

func (m *Manager) withPlayer(guildID string, fn func(*guildPlayer) (Response, error)) (Response, error) {
	player := m.existingPlayer(guildID)
	if player == nil {
		return Response{}, ErrNothingPlaying
	}
	return fn(player)
}

func (m *Manager) player(guildID string) *guildPlayer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if player := m.players[guildID]; player != nil {
		return player
	}
	player := &guildPlayer{manager: m, guildID: guildID}
	m.players[guildID] = player
	return player
}

func (m *Manager) existingPlayer(guildID string) *guildPlayer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.players[guildID]
}

func (m *Manager) persistedQueue(ctx context.Context, guildID string) (Response, error) {
	if m.store == nil {
		return Response{}, ErrNothingPlaying
	}
	items, err := m.store.Queue(ctx, guildID)
	if err != nil {
		return Response{}, err
	}
	if len(items) == 0 {
		return Response{}, ErrNothingPlaying
	}
	return queueResponse("Saved music queue", tracksFromQueueItems(items), nil, false), nil
}

func (m *Manager) ensureDJ(ctx context.Context, request Request) error {
	if request.IsOwner || request.IsGuildAdmin || m.store == nil {
		return nil
	}
	settings, err := m.store.EnsureSettings(ctx, request.GuildID)
	if err != nil {
		return err
	}
	djRoleID := strings.TrimSpace(settings.DJRoleID)
	if djRoleID == "" {
		return nil
	}
	for _, roleID := range request.RoleIDs {
		if strings.TrimSpace(roleID) == djRoleID {
			return nil
		}
	}
	return ErrMissingDJ
}

func (m *Manager) setLoopMode(ctx context.Context, request Request) (Response, error) {
	if m.store == nil {
		return musicResponse("Music settings unavailable", "Music settings storage is not configured."), nil
	}
	mode := strings.ToLower(strings.TrimSpace(request.Intent.Mode))
	switch mode {
	case "", "track":
		mode = "track"
	case "queue", "off":
	default:
		return musicResponse("Loop mode not changed", "`loop` must be `track`, `queue`, or `off`."), nil
	}
	settings, err := m.store.UpdateSettings(ctx, request.GuildID, map[string]any{"loop_mode": mode})
	if err != nil {
		return Response{}, err
	}
	return musicResponse("Loop updated", fmt.Sprintf("Loop mode is now `%s`.", settings.LoopMode)), nil
}

func (m *Manager) musicSettings(ctx context.Context, request Request) (Response, error) {
	if m.store == nil {
		return musicResponse("Music settings unavailable", "Music settings storage is not configured."), nil
	}
	if request.Intent.Volume > 0 {
		volume := request.Intent.Volume
		if volume < 1 || volume > 200 {
			return musicResponse("Volume not changed", "Default volume must be between 1 and 200."), nil
		}
		settings, err := m.store.UpdateSettings(ctx, request.GuildID, map[string]any{"default_volume": volume})
		if err != nil {
			return Response{}, err
		}
		return musicResponse("Default volume updated", fmt.Sprintf("Default volume is now %d%%.", settings.DefaultVolume)), nil
	}
	settings, err := m.store.EnsureSettings(ctx, request.GuildID)
	if err != nil {
		return Response{}, err
	}
	content := fmt.Sprintf("Music settings:\n- loop: `%s`\n- default volume: `%d%%`\n- vote skip threshold: `%.2f`", settings.LoopMode, settings.DefaultVolume, settings.VoteSkipThreshold)
	if strings.TrimSpace(settings.DJRoleID) != "" {
		content += fmt.Sprintf("\n- DJ role: `%s`", settings.DJRoleID)
	} else {
		content += "\n- DJ role: not configured"
	}
	return musicResponse("Music settings", content), nil
}

func (m *Manager) playlist(ctx context.Context, request Request) (Response, error) {
	if m.store == nil {
		return musicResponse("Playlists unavailable", "Music playlist storage is not configured."), nil
	}
	switch request.Intent.Mode {
	case "save":
		tracks := m.tracksForPlaylist(ctx, request.GuildID)
		if len(tracks) == 0 {
			return Response{}, ErrNothingPlaying
		}
		items := queueItemsFromTracks(tracks)
		raw, err := repository.MarshalPlaylistTracks(items)
		if err != nil {
			return Response{}, err
		}
		playlist, err := m.store.SavePlaylist(ctx, store.MusicPlaylist{
			GuildID:    request.GuildID,
			Name:       request.Intent.Name,
			CreatedBy:  request.UserID,
			TracksJSON: raw,
		})
		if err != nil {
			return Response{}, err
		}
		return musicResponse("Playlist saved", fmt.Sprintf("Saved `%s` with %d track(s).", playlist.Name, len(items))), nil
	case "load":
		if strings.TrimSpace(request.VoiceChannelID) == "" {
			return Response{}, ErrMissingVoice
		}
		playlist, ok, err := m.store.Playlist(ctx, request.GuildID, request.Intent.Name)
		if err != nil {
			return Response{}, err
		}
		if !ok {
			return musicResponse("Playlist not found", fmt.Sprintf("No saved playlist named `%s`.", request.Intent.Name)), nil
		}
		items, err := repository.UnmarshalPlaylistTracks(playlist.TracksJSON)
		if err != nil {
			return Response{}, err
		}
		tracks := tracksFromQueueItems(items)
		if len(tracks) == 0 {
			return musicResponse("Playlist empty", fmt.Sprintf("Playlist `%s` has no tracks.", playlist.Name)), nil
		}
		player := m.player(request.GuildID)
		for index, track := range tracks {
			track.RequestedBy = request.UserID
			track.TextChannelID = request.TextChannelID
			if _, err := player.enqueue(ctx, track, request.VoiceChannelID); err != nil {
				if index == 0 {
					return Response{}, err
				}
				break
			}
		}
		return musicResponse("Playlist queued", fmt.Sprintf("Queued `%s` with %d track(s).", playlist.Name, len(tracks))), nil
	default:
		playlists, err := m.store.Playlists(ctx, request.GuildID, 25)
		if err != nil {
			return Response{}, err
		}
		if len(playlists) == 0 {
			return musicResponse("Saved playlists", "No saved playlists yet."), nil
		}
		lines := []string{"Saved playlists:"}
		for _, playlist := range playlists {
			items, _ := repository.UnmarshalPlaylistTracks(playlist.TracksJSON)
			lines = append(lines, fmt.Sprintf("- `%s` (%d track(s))", playlist.Name, len(items)))
		}
		return musicResponse("Saved playlists", strings.Join(lines, "\n")), nil
	}
}

func (m *Manager) ConfigureSettings(ctx context.Context, guildID string, update SettingsUpdate) (SettingsSnapshot, error) {
	if m.store == nil {
		return SettingsSnapshot{}, ErrDependencyMissing
	}
	values := map[string]any{}
	if strings.TrimSpace(update.LoopMode) != "" {
		mode := strings.ToLower(strings.TrimSpace(update.LoopMode))
		if mode != "off" && mode != "track" && mode != "queue" {
			return SettingsSnapshot{}, fmt.Errorf("loop mode must be off, track, or queue")
		}
		values["loop_mode"] = mode
	}
	if update.DefaultVolumeSet {
		if update.DefaultVolume < 1 || update.DefaultVolume > 200 {
			return SettingsSnapshot{}, fmt.Errorf("default volume must be between 1 and 200")
		}
		values["default_volume"] = update.DefaultVolume
	}
	if update.DJRoleSet {
		values["dj_role_id"] = strings.TrimSpace(update.DJRoleID)
	}
	if update.VoteSkipThresholdSet {
		if update.VoteSkipThreshold <= 0 || update.VoteSkipThreshold > 1 {
			return SettingsSnapshot{}, fmt.Errorf("vote skip threshold must be greater than 0 and at most 1")
		}
		values["vote_skip_threshold"] = update.VoteSkipThreshold
	}
	var settings store.MusicSettings
	var err error
	if len(values) == 0 {
		settings, err = m.store.EnsureSettings(ctx, guildID)
	} else {
		settings, err = m.store.UpdateSettings(ctx, guildID, values)
	}
	if err != nil {
		return SettingsSnapshot{}, err
	}
	return SettingsSnapshot{
		LoopMode:          settings.LoopMode,
		DefaultVolume:     settings.DefaultVolume,
		DJRoleID:          settings.DJRoleID,
		VoteSkipThreshold: settings.VoteSkipThreshold,
	}, nil
}

func (m *Manager) tracksForPlaylist(ctx context.Context, guildID string) []Track {
	if player := m.existingPlayer(guildID); player != nil {
		return player.allTracks()
	}
	if m.store == nil {
		return nil
	}
	items, err := m.store.Queue(ctx, guildID)
	if err != nil {
		return nil
	}
	return tracksFromQueueItems(items)
}

func (m *Manager) removePlayer(guildID string, player *guildPlayer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.players[guildID] == player {
		delete(m.players, guildID)
	}
}

type guildPlayer struct {
	manager *Manager
	guildID string

	mu             sync.Mutex
	session        VoiceSession
	voiceChannelID string
	current        *Track
	queueItems     []Track
	playing        bool
	paused         bool
	stopping       bool
	trackCancel    context.CancelFunc
	nextStart      chan error

	emptyDisconnectTimer *time.Timer
	emptyDisconnectToken *emptyDisconnectToken
	emptySkipWaitUntil   time.Time
	skipVotes            map[string]struct{}
}

type emptyDisconnectToken struct{}

func (p *guildPlayer) enqueue(ctx context.Context, track Track, voiceChannelID string) (Response, error) {
	p.mu.Lock()
	if p.playing && p.voiceChannelID != "" && p.voiceChannelID != voiceChannelID {
		p.mu.Unlock()
		return Response{}, ErrDifferentVoice
	}
	startingAfterSkip := p.playing && p.current == nil && !p.emptySkipWaitUntil.IsZero()
	if p.playing && !startingAfterSkip {
		track = withoutTransientStream(track)
	}
	p.queueItems = append(p.queueItems, track)
	position := len(p.queueItems)
	shouldStart := !p.playing
	if shouldStart {
		p.playing = true
		p.stopping = false
		p.voiceChannelID = voiceChannelID
	}
	snapshot := append([]Track(nil), p.queueItems...)
	p.mu.Unlock()
	p.persistQueueSnapshot(snapshot)

	if !shouldStart {
		if startingAfterSkip {
			return trackResponse("Starting track", fmt.Sprintf("Starting **%s**.", trackTitle(track)), track), nil
		}
		return trackResponse("Track queued", fmt.Sprintf("Queued **%s** at position %d.", trackTitle(track), position), track), nil
	}

	session, err := p.manager.connector.Connect(ctx, p.guildID, voiceChannelID)
	if err != nil {
		p.mu.Lock()
		p.queueItems = removeTrackFromQueue(p.queueItems, track)
		snapshot := append([]Track(nil), p.queueItems...)
		p.playing = false
		p.voiceChannelID = ""
		p.mu.Unlock()
		p.persistQueueSnapshot(snapshot)
		p.manager.removePlayer(p.guildID, p)
		return Response{}, err
	}

	p.mu.Lock()
	p.session = session
	p.voiceChannelID = session.ChannelID()
	p.mu.Unlock()
	started := make(chan error, 1)
	go p.run(started)
	select {
	case err := <-started:
		if err != nil {
			return Response{}, err
		}
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	return trackResponse("Connected to voice", fmt.Sprintf("Joined <#%s> and started **%s**.", voiceChannelID, trackTitle(track)), track), nil
}

func (p *guildPlayer) run(firstStart chan<- error) {
	defer p.manager.removePlayer(p.guildID, p)
	defer p.closeSession(context.Background())

	for {
		track, ok := p.nextTrack()
		if !ok {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		p.mu.Lock()
		p.current = &track
		p.trackCancel = cancel
		p.skipVotes = map[string]struct{}{}
		p.mu.Unlock()

		started := firstStart
		if started == nil {
			started = p.takeNextStartWaiter()
		}
		err := p.playTrack(ctx, track, started)
		firstStart = nil
		cancel()

		p.mu.Lock()
		stopping := p.stopping
		p.current = nil
		p.trackCancel = nil
		p.mu.Unlock()

		if err != nil && !isExpectedPlaybackStop(err) {
			p.manager.logger.Warn("music playback failed", slog.Any("err", err), slog.String("guild_id", p.guildID), slog.String("track", track.Title))
		}
		if stopping {
			return
		}
		if err == nil {
			p.loopCompletedTrack(track)
		}
	}
}

func (p *guildPlayer) nextTrack() (Track, bool) {
	for {
		p.mu.Lock()
		if p.stopping {
			p.emptySkipWaitUntil = time.Time{}
			p.playing = false
			p.paused = false
			p.voiceChannelID = ""
			timer := p.clearEmptyVoiceDisconnectLocked()
			p.mu.Unlock()
			if timer != nil {
				timer.Stop()
			}
			return Track{}, false
		}
		if len(p.queueItems) > 0 {
			p.emptySkipWaitUntil = time.Time{}
			track := p.queueItems[0]
			p.queueItems = p.queueItems[1:]
			snapshot := append([]Track(nil), p.queueItems...)
			p.mu.Unlock()
			p.persistQueueSnapshot(snapshot)
			return track, true
		}
		waitUntil := p.emptySkipWaitUntil
		if waitUntil.IsZero() || !time.Now().Before(waitUntil) {
			p.emptySkipWaitUntil = time.Time{}
			p.playing = false
			p.paused = false
			p.voiceChannelID = ""
			timer := p.clearEmptyVoiceDisconnectLocked()
			p.mu.Unlock()
			if timer != nil {
				timer.Stop()
			}
			return Track{}, false
		}
		wait := time.Until(waitUntil)
		if wait > 250*time.Millisecond {
			wait = 250 * time.Millisecond
		}
		p.mu.Unlock()
		timer := time.NewTimer(wait)
		<-timer.C
	}
}

func (p *guildPlayer) playTrack(ctx context.Context, track Track, started chan<- error) error {
	stream, err := p.manager.streamer.Stream(ctx, track)
	if err != nil {
		signalPlaybackStart(started, err)
		return err
	}
	provider := newBufferedOpusProvider(stream, playbackBufferFrames, playbackPrebufferFrames)
	defer provider.Close()

	session := p.currentSession()
	if session == nil {
		signalPlaybackStart(started, ErrTrackStreamFailed)
		return ErrTrackStreamFailed
	}

	readyCtx, cancel := context.WithTimeout(ctx, playbackPrebufferTimeout)
	err = provider.WaitReady(readyCtx)
	cancel()
	if err != nil {
		err = fmt.Errorf("%w: audio prebuffer failed with %d frame(s) ready: %v", ErrTrackStreamFailed, provider.BufferedFrames(), err)
		signalPlaybackStart(started, err)
		return err
	}
	signalPlaybackStart(started, nil)

	speaking := false
	writtenFrames := 0
	defer func() {
		speakCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = session.SetSpeaking(speakCtx, false)
	}()

	nextFrame := time.Now()
	for {
		if p.isPaused() {
			if speaking {
				if err := session.SetSpeaking(ctx, false); err != nil {
					return err
				}
				speaking = false
			}
			if err := p.waitWhilePaused(ctx); err != nil {
				return err
			}
			nextFrame = time.Now()
		}

		frame, stalled, err := provider.ProvideOpusFrameWithin(ctx, playbackFrameStall)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if stalled {
			return fmt.Errorf("%w: audio buffer stalled after %d frame(s)", ErrTrackStreamFailed, writtenFrames)
		}
		if len(frame) == 0 {
			continue
		}
		if !speaking {
			if err := session.SetSpeaking(ctx, true); err != nil {
				return err
			}
			speaking = true
			if err := session.WriteOpus(ctx, silenceOpusFrame); err != nil {
				return err
			}
			nextFrame = time.Now().Add(frameDuration)
		}
		if sleep := time.Until(nextFrame); sleep > 0 {
			timer := time.NewTimer(sleep)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		if err := session.WriteOpus(ctx, frame); err != nil {
			return err
		}
		writtenFrames++
		nextFrame = nextFrame.Add(frameDuration)
		if time.Since(nextFrame) > 3*frameDuration {
			nextFrame = time.Now().Add(frameDuration)
		}
	}
}

func signalPlaybackStart(started chan<- error, err error) {
	if started == nil {
		return
	}
	select {
	case started <- err:
	default:
	}
}

func (p *guildPlayer) pause(ctx context.Context) (Response, error) {
	p.mu.Lock()
	if p.current == nil {
		p.mu.Unlock()
		return Response{}, ErrNothingPlaying
	}
	if p.paused {
		p.mu.Unlock()
		return Response{}, ErrAlreadyPaused
	}
	p.paused = true
	session := p.session
	p.mu.Unlock()
	if session != nil {
		_ = session.SetSpeaking(ctx, false)
	}
	return musicResponse("Music paused", "Paused the current track."), nil
}

func (p *guildPlayer) resume() (Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current == nil {
		return Response{}, ErrNothingPlaying
	}
	if !p.paused {
		return Response{}, ErrAlreadyPlaying
	}
	p.paused = false
	return musicResponse("Music resumed", "Resumed the current track."), nil
}

func (p *guildPlayer) skip() (Response, error) {
	p.mu.Lock()
	if p.current == nil {
		p.mu.Unlock()
		return Response{}, ErrNothingPlaying
	}
	title := trackTitle(*p.current)
	remaining := len(p.queueItems)
	cancel := p.trackCancel
	var timer *time.Timer
	if remaining == 0 {
		p.paused = false
		p.emptySkipWaitUntil = time.Now().Add(emptySkipReplacementWait)
		timer = p.clearEmptyVoiceDisconnectLocked()
	}
	p.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if cancel != nil {
		cancel()
	}
	if remaining == 0 {
		return musicResponse("Track skipped", fmt.Sprintf("Skipped **%s**. The queue is empty; I will leave voice if nothing else is queued.", title)), nil
	}
	return musicResponse("Track skipped", fmt.Sprintf("Skipped **%s**. %d song(s) left in queue.", title, remaining)), nil
}

func (p *guildPlayer) skipAndPlay(ctx context.Context, track Track, voiceChannelID string) (Response, error) {
	p.mu.Lock()
	if p.playing && p.voiceChannelID != "" && p.voiceChannelID != voiceChannelID {
		p.mu.Unlock()
		return Response{}, ErrDifferentVoice
	}
	if p.current == nil {
		p.mu.Unlock()
		return p.enqueue(ctx, track, voiceChannelID)
	}
	skippedTitle := trackTitle(*p.current)
	cancel := p.trackCancel
	if cancel == nil {
		p.mu.Unlock()
		return Response{}, ErrTrackStreamFailed
	}
	started := make(chan error, 1)
	previousStart := p.nextStart
	p.queueItems = append([]Track{track}, p.queueItems...)
	p.paused = false
	p.emptySkipWaitUntil = time.Time{}
	p.nextStart = started
	snapshot := append([]Track(nil), p.queueItems...)
	p.mu.Unlock()

	signalPlaybackStart(previousStart, context.Canceled)
	p.persistQueueSnapshot(snapshot)
	cancel()
	select {
	case err := <-started:
		if err != nil {
			return Response{}, err
		}
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	return trackResponse("Track replaced", fmt.Sprintf("Skipped **%s** and started **%s**.", skippedTitle, trackTitle(track)), track), nil
}

func (p *guildPlayer) stop(ctx context.Context) (Response, error) {
	p.mu.Lock()
	hadCurrent := p.current != nil
	queued := len(p.queueItems)
	p.queueItems = nil
	p.paused = false
	p.stopping = true
	timer := p.clearEmptyVoiceDisconnectLocked()
	cancel := p.trackCancel
	nextStart := p.nextStart
	p.nextStart = nil
	p.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	signalPlaybackStart(nextStart, context.Canceled)
	if cancel != nil {
		cancel()
	}
	p.persistQueueSnapshot(nil)
	p.closeSession(ctx)
	if !hadCurrent && queued == 0 {
		return Response{}, ErrNothingPlaying
	}
	return musicResponse("Music stopped", "Stopped playback and cleared the queue."), nil
}

func (p *guildPlayer) clear() (Response, error) {
	p.mu.Lock()
	removed := len(p.queueItems)
	p.queueItems = nil
	p.mu.Unlock()
	p.persistQueueSnapshot(nil)
	if removed == 0 {
		return musicResponse("Queue already clear", "The queue is already empty."), nil
	}
	return musicResponse("Queue cleared", fmt.Sprintf("Cleared %d queued song(s).", removed)), nil
}

func (p *guildPlayer) now() (Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current == nil {
		return Response{}, ErrNothingPlaying
	}
	status := "Playing"
	if p.paused {
		status = "Paused"
	}
	return trackResponse("Now playing", fmt.Sprintf("%s **%s**%s.", status, trackTitle(*p.current), durationSuffix(p.current.Duration)), *p.current), nil
}

func (p *guildPlayer) queue() (Response, error) {
	p.mu.Lock()
	if p.current == nil && len(p.queueItems) == 0 {
		p.mu.Unlock()
		return Response{}, ErrNothingPlaying
	}
	current := p.current
	if current != nil {
		copyTrack := *current
		current = &copyTrack
	}
	queued := append([]Track(nil), p.queueItems...)
	paused := p.paused
	p.mu.Unlock()
	return queueResponse("Music queue", queued, current, paused), nil
}

func queueResponse(title string, queued []Track, current *Track, paused bool) Response {
	lines := []string{"Music queue:"}
	if current != nil {
		prefix := "Now"
		if paused {
			prefix = "Paused"
		}
		lines = append(lines, fmt.Sprintf("- %s: **%s**%s%s", prefix, trackTitle(*current), durationSuffix(current.Duration), requesterSuffix(current.RequestedBy)))
	}
	if len(queued) == 0 {
		lines = append(lines, "- Up next: empty")
	} else {
		for index, track := range queued {
			if index >= 10 {
				lines = append(lines, fmt.Sprintf("- ...and %d more", len(queued)-index))
				break
			}
			lines = append(lines, fmt.Sprintf("- %d. **%s**%s%s", index+1, trackTitle(track), durationSuffix(track.Duration), requesterSuffix(track.RequestedBy)))
		}
	}
	return musicResponse(title, strings.Join(lines, "\n"))
}

func (p *guildPlayer) shuffle() (Response, error) {
	p.mu.Lock()
	if len(p.queueItems) < 2 {
		p.mu.Unlock()
		return musicResponse("Queue unchanged", "Need at least two queued songs to shuffle."), nil
	}
	rand.Shuffle(len(p.queueItems), func(i, j int) {
		p.queueItems[i], p.queueItems[j] = p.queueItems[j], p.queueItems[i]
	})
	snapshot := append([]Track(nil), p.queueItems...)
	p.mu.Unlock()
	p.persistQueueSnapshot(snapshot)
	return musicResponse("Queue shuffled", fmt.Sprintf("Shuffled %d queued song(s).", len(snapshot))), nil
}

func (p *guildPlayer) remove(position int) (Response, error) {
	if position <= 0 {
		return Response{}, ErrInvalidQueueIndex
	}
	p.mu.Lock()
	if position > len(p.queueItems) {
		p.mu.Unlock()
		return Response{}, ErrInvalidQueueIndex
	}
	removed := p.queueItems[position-1]
	p.queueItems = append(p.queueItems[:position-1], p.queueItems[position:]...)
	snapshot := append([]Track(nil), p.queueItems...)
	p.mu.Unlock()
	p.persistQueueSnapshot(snapshot)
	return trackResponse("Track removed", fmt.Sprintf("Removed **%s** from queue position %d.", trackTitle(removed), position), removed), nil
}

func (p *guildPlayer) move(from, to int) (Response, error) {
	if from <= 0 || to <= 0 {
		return Response{}, ErrInvalidQueueIndex
	}
	p.mu.Lock()
	if from > len(p.queueItems) || to > len(p.queueItems) {
		p.mu.Unlock()
		return Response{}, ErrInvalidQueueIndex
	}
	track := p.queueItems[from-1]
	p.queueItems = append(p.queueItems[:from-1], p.queueItems[from:]...)
	if to > len(p.queueItems)+1 {
		to = len(p.queueItems) + 1
	}
	insertAt := to - 1
	p.queueItems = append(p.queueItems, Track{})
	copy(p.queueItems[insertAt+1:], p.queueItems[insertAt:])
	p.queueItems[insertAt] = track
	snapshot := append([]Track(nil), p.queueItems...)
	p.mu.Unlock()
	p.persistQueueSnapshot(snapshot)
	return trackResponse("Track moved", fmt.Sprintf("Moved **%s** from position %d to %d.", trackTitle(track), from, to), track), nil
}

func (p *guildPlayer) voteSkip(userID string) (Response, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Response{}, ErrMissingDJ
	}
	p.mu.Lock()
	if p.current == nil {
		p.mu.Unlock()
		return Response{}, ErrNothingPlaying
	}
	if p.skipVotes == nil {
		p.skipVotes = map[string]struct{}{}
	}
	p.skipVotes[userID] = struct{}{}
	votes := len(p.skipVotes)
	required := p.voteSkipRequiredLocked()
	title := trackTitle(*p.current)
	cancel := p.trackCancel
	p.mu.Unlock()
	if votes < required {
		return musicResponse("Skip vote counted", fmt.Sprintf("Vote counted for **%s**. %d/%d vote(s).", title, votes, required)), nil
	}
	if cancel != nil {
		cancel()
	}
	return musicResponse("Vote skip passed", fmt.Sprintf("Skipped **%s** after %d vote(s).", title, votes)), nil
}

func (p *guildPlayer) voteSkipRequiredLocked() int {
	threshold := 0.5
	if p.manager.store != nil {
		settings, err := p.manager.store.EnsureSettings(context.Background(), p.guildID)
		if err == nil && settings.VoteSkipThreshold > 0 {
			threshold = settings.VoteSkipThreshold
		}
	}
	return int(math.Max(2, math.Ceil(1/threshold)))
}

func (p *guildPlayer) allTracks() []Track {
	p.mu.Lock()
	defer p.mu.Unlock()
	var tracks []Track
	if p.current != nil {
		tracks = append(tracks, *p.current)
	}
	tracks = append(tracks, p.queueItems...)
	return tracks
}

func (p *guildPlayer) loopCompletedTrack(track Track) {
	if p.manager.store == nil {
		return
	}
	settings, err := p.manager.store.EnsureSettings(context.Background(), p.guildID)
	if err != nil {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(settings.LoopMode))
	if mode != "track" && mode != "queue" {
		return
	}
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	track = withoutTransientStream(track)
	if mode == "track" {
		p.queueItems = append([]Track{track}, p.queueItems...)
	} else {
		p.queueItems = append(p.queueItems, track)
	}
	snapshot := append([]Track(nil), p.queueItems...)
	p.mu.Unlock()
	p.persistQueueSnapshot(snapshot)
}

func (p *guildPlayer) persistQueueSnapshot(tracks []Track) {
	if p.manager.store == nil {
		return
	}
	items := queueItemsFromTracks(tracks)
	if err := p.manager.store.ReplaceQueue(context.Background(), p.guildID, items); err != nil {
		p.manager.logger.Warn("music queue persist failed", slog.Any("err", err), slog.String("guild_id", p.guildID), slog.Int("track_count", len(tracks)))
	}
}

func (p *guildPlayer) currentSession() VoiceSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session
}

func (p *guildPlayer) takeNextStartWaiter() chan error {
	p.mu.Lock()
	defer p.mu.Unlock()
	started := p.nextStart
	p.nextStart = nil
	return started
}

func (p *guildPlayer) activeVoiceChannelID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.voiceChannelID
}

func (p *guildPlayer) isInVoiceChannel(channelID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.voiceChannelID != "" && p.voiceChannelID == strings.TrimSpace(channelID)
}

func (p *guildPlayer) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

func (p *guildPlayer) waitWhilePaused(ctx context.Context) error {
	for {
		if !p.isPaused() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *guildPlayer) closeSession(ctx context.Context) {
	p.mu.Lock()
	session := p.session
	p.session = nil
	p.voiceChannelID = ""
	timer := p.clearEmptyVoiceDisconnectLocked()
	p.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if session != nil {
		session.Close(ctx)
	}
}

func (p *guildPlayer) scheduleEmptyVoiceDisconnect(channelID string, delay time.Duration) {
	channelID = strings.TrimSpace(channelID)
	if delay <= 0 {
		delay = emptyVoiceDisconnectWait
	}

	p.mu.Lock()
	if p.voiceChannelID != channelID || !p.playing || p.stopping {
		p.mu.Unlock()
		return
	}
	if p.emptyDisconnectToken != nil {
		p.mu.Unlock()
		return
	}
	token := &emptyDisconnectToken{}
	p.emptyDisconnectToken = token
	p.emptyDisconnectTimer = time.AfterFunc(delay, func() {
		p.disconnectIfStillEmpty(channelID, token)
	})
	p.mu.Unlock()
}

func (p *guildPlayer) cancelEmptyVoiceDisconnect() {
	p.mu.Lock()
	timer := p.clearEmptyVoiceDisconnectLocked()
	p.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (p *guildPlayer) disconnectIfStillEmpty(channelID string, token *emptyDisconnectToken) {
	p.mu.Lock()
	if p.emptyDisconnectToken != token || p.voiceChannelID != channelID || !p.playing || p.stopping {
		p.mu.Unlock()
		return
	}
	p.clearEmptyVoiceDisconnectLocked()
	p.mu.Unlock()

	if _, err := p.stop(context.Background()); err != nil && !errors.Is(err, ErrNothingPlaying) {
		p.manager.logger.Warn("music empty voice disconnect failed", slog.Any("err", err), slog.String("guild_id", p.guildID))
	}
}

func (p *guildPlayer) clearEmptyVoiceDisconnectLocked() *time.Timer {
	timer := p.emptyDisconnectTimer
	p.emptyDisconnectTimer = nil
	p.emptyDisconnectToken = nil
	return timer
}

func removeTrackFromQueue(queue []Track, track Track) []Track {
	for index, item := range queue {
		if item.Title == track.Title && item.URL == track.URL {
			return append(queue[:index], queue[index+1:]...)
		}
	}
	return queue
}

func withoutTransientStream(track Track) Track {
	track.StreamURL = ""
	track.StreamHeaders = nil
	return track
}

func trackTitle(track Track) string {
	if strings.TrimSpace(track.Title) != "" {
		return track.Title
	}
	return firstNonEmpty(track.Query, "unknown song")
}

func durationSuffix(duration time.Duration) string {
	if duration <= 0 {
		return ""
	}
	totalSeconds := int(duration.Round(time.Second).Seconds())
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf(" `%d:%02d`", minutes, seconds)
}

func requesterSuffix(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	return fmt.Sprintf(" requested by <@%s>", userID)
}

func controlsMessage() string {
	return "Music controls: `play <song>`, `skip and play <song>`, `pause`, `resume`, `vote skip`, `skip`, `stop`, `queue`, `remove <#>`, `move <#> to <#>`, `shuffle`, `loop track|queue|off`, `save playlist <name>`, `load playlist <name>`, `volume <1-200>`, `clear queue`, and `now playing`."
}

func musicResponse(title, content string) Response {
	return Response{Title: title, Content: content}
}

func trackResponse(title, content string, track Track) Response {
	response := musicResponse(title, content)
	if strings.TrimSpace(track.URL) != "" {
		response.URL = track.URL
		response.Actions = append(response.Actions, ResponseAction{Label: "Open track", URL: track.URL})
	}
	if strings.TrimSpace(track.Uploader) != "" {
		response.Fields = append(response.Fields, Field{Name: "Uploader", Value: track.Uploader, Inline: true})
	}
	if track.Duration > 0 {
		response.Fields = append(response.Fields, Field{Name: "Duration", Value: strings.TrimSpace(durationSuffix(track.Duration)), Inline: true})
	}
	if strings.TrimSpace(track.RequestedBy) != "" {
		response.Fields = append(response.Fields, Field{Name: "Requester", Value: "<@" + strings.TrimSpace(track.RequestedBy) + ">", Inline: true})
	}
	return response
}

func queueItemsFromTracks(tracks []Track) []store.MusicQueueItem {
	items := make([]store.MusicQueueItem, 0, len(tracks))
	for _, track := range tracks {
		items = append(items, store.MusicQueueItem{
			TrackID:       track.ID,
			Query:         track.Query,
			Title:         track.Title,
			URL:           track.URL,
			Uploader:      track.Uploader,
			DurationMS:    track.Duration.Milliseconds(),
			RequestedBy:   track.RequestedBy,
			TextChannelID: track.TextChannelID,
		})
	}
	return items
}

func tracksFromQueueItems(items []store.MusicQueueItem) []Track {
	tracks := make([]Track, 0, len(items))
	for _, item := range items {
		tracks = append(tracks, Track{
			ID:            item.TrackID,
			Query:         item.Query,
			Title:         item.Title,
			URL:           item.URL,
			Uploader:      item.Uploader,
			Duration:      time.Duration(item.DurationMS) * time.Millisecond,
			RequestedBy:   item.RequestedBy,
			TextChannelID: item.TextChannelID,
		})
	}
	return tracks
}
