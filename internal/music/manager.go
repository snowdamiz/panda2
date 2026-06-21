package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	frameDuration            = 20 * time.Millisecond
	playbackBufferFrames     = 250
	playbackPrebufferFrames  = 50
	playbackPrebufferTimeout = 30 * time.Second
	playbackFrameStall       = 5 * time.Second
	emptyVoiceDisconnectWait = 10 * time.Second
)

var silenceOpusFrame = []byte{0xF8, 0xFF, 0xFE}

type Manager struct {
	resolver  Resolver
	streamer  Streamer
	connector VoiceConnector
	logger    *slog.Logger

	mu      sync.Mutex
	players map[string]*guildPlayer

	emptyVoiceDisconnectWait time.Duration
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
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.skip()
		})
	case ActionStop:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.stop(ctx)
		})
	case ActionQueue:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.queue()
		})
	case ActionClear:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.clear()
		})
	case ActionNow:
		return m.withPlayer(request.GuildID, func(player *guildPlayer) (Response, error) {
			return player.now()
		})
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

	emptyDisconnectTimer *time.Timer
	emptyDisconnectToken *emptyDisconnectToken
}

type emptyDisconnectToken struct{}

func (p *guildPlayer) enqueue(ctx context.Context, track Track, voiceChannelID string) (Response, error) {
	p.mu.Lock()
	if p.playing && p.voiceChannelID != "" && p.voiceChannelID != voiceChannelID {
		p.mu.Unlock()
		return Response{}, ErrDifferentVoice
	}
	p.queueItems = append(p.queueItems, track)
	position := len(p.queueItems)
	shouldStart := !p.playing
	if shouldStart {
		p.playing = true
		p.stopping = false
		p.voiceChannelID = voiceChannelID
	}
	p.mu.Unlock()

	if !shouldStart {
		return trackResponse("Track queued", fmt.Sprintf("Queued **%s** at position %d.", trackTitle(track), position), track), nil
	}

	session, err := p.manager.connector.Connect(ctx, p.guildID, voiceChannelID)
	if err != nil {
		p.mu.Lock()
		p.queueItems = removeTrackFromQueue(p.queueItems, track)
		p.playing = false
		p.voiceChannelID = ""
		p.mu.Unlock()
		p.manager.removePlayer(p.guildID, p)
		return Response{}, err
	}

	p.mu.Lock()
	p.session = session
	p.voiceChannelID = session.ChannelID()
	p.mu.Unlock()
	go p.run()
	return trackResponse("Connected to voice", fmt.Sprintf("Joined <#%s> and started buffering **%s**.", voiceChannelID, trackTitle(track)), track), nil
}

func (p *guildPlayer) run() {
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
		p.mu.Unlock()

		err := p.playTrack(ctx, track)
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
	}
}

func (p *guildPlayer) nextTrack() (Track, bool) {
	p.mu.Lock()
	if p.stopping || len(p.queueItems) == 0 {
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
	track := p.queueItems[0]
	p.queueItems = p.queueItems[1:]
	p.mu.Unlock()
	return track, true
}

func (p *guildPlayer) playTrack(ctx context.Context, track Track) error {
	stream, err := p.manager.streamer.Stream(ctx, track)
	if err != nil {
		return err
	}
	provider := newBufferedOpusProvider(stream, playbackBufferFrames, playbackPrebufferFrames)
	defer provider.Close()

	session := p.currentSession()
	if session == nil {
		return ErrTrackStreamFailed
	}

	readyCtx, cancel := context.WithTimeout(ctx, playbackPrebufferTimeout)
	err = provider.WaitReady(readyCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("%w: audio prebuffer failed with %d frame(s) ready: %v", ErrTrackStreamFailed, provider.BufferedFrames(), err)
	}
	p.manager.logger.Info("music playback buffered", slog.String("guild_id", p.guildID), slog.String("track", track.Title), slog.Int("buffered_frames", provider.BufferedFrames()))

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
			p.manager.logger.Info("music playback finished", slog.String("guild_id", p.guildID), slog.String("track", track.Title), slog.Int("frames", writtenFrames))
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
		if writtenFrames == 1 {
			p.manager.logger.Info("music playback started", slog.String("guild_id", p.guildID), slog.String("track", track.Title), slog.Int("buffered_frames", provider.BufferedFrames()))
		}
		nextFrame = nextFrame.Add(frameDuration)
		if time.Since(nextFrame) > 3*frameDuration {
			nextFrame = time.Now().Add(frameDuration)
		}
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
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if remaining == 0 {
		return musicResponse("Track skipped", fmt.Sprintf("Skipped **%s**. The queue is empty, so I will leave voice.", title)), nil
	}
	return musicResponse("Track skipped", fmt.Sprintf("Skipped **%s**. %d song(s) left in queue.", title, remaining)), nil
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
	p.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if cancel != nil {
		cancel()
	}
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
	defer p.mu.Unlock()
	if p.current == nil && len(p.queueItems) == 0 {
		return Response{}, ErrNothingPlaying
	}
	lines := []string{"Music queue:"}
	if p.current != nil {
		prefix := "Now"
		if p.paused {
			prefix = "Paused"
		}
		lines = append(lines, fmt.Sprintf("- %s: **%s**%s", prefix, trackTitle(*p.current), durationSuffix(p.current.Duration)))
	}
	if len(p.queueItems) == 0 {
		lines = append(lines, "- Up next: empty")
	} else {
		for index, track := range p.queueItems {
			if index >= 10 {
				lines = append(lines, fmt.Sprintf("- ...and %d more", len(p.queueItems)-index))
				break
			}
			lines = append(lines, fmt.Sprintf("- %d. **%s**%s", index+1, trackTitle(track), durationSuffix(track.Duration)))
		}
	}
	return musicResponse("Music queue", strings.Join(lines, "\n")), nil
}

func (p *guildPlayer) currentSession() VoiceSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session
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

	p.manager.logger.Info("music voice channel empty; scheduled disconnect",
		slog.String("guild_id", p.guildID),
		slog.String("voice_channel_id", channelID),
		slog.Duration("delay", delay),
	)
}

func (p *guildPlayer) cancelEmptyVoiceDisconnect() {
	p.mu.Lock()
	timer := p.clearEmptyVoiceDisconnectLocked()
	p.mu.Unlock()
	if timer != nil {
		timer.Stop()
		p.manager.logger.Info("music voice channel occupied; canceled empty disconnect", slog.String("guild_id", p.guildID))
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

	p.manager.logger.Info("music voice channel still empty; disconnecting",
		slog.String("guild_id", p.guildID),
		slog.String("voice_channel_id", channelID),
	)
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

func controlsMessage() string {
	return "Music controls: `play <song>`, `pause`, `resume`, `skip`, `stop`, `queue`, `clear queue`, and `now playing`."
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
	return response
}
