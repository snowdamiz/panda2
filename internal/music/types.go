package music

import (
	"context"
	"errors"
	"io"
	"time"
)

type Action string

const (
	ActionPlay     Action = "play"
	ActionPause    Action = "pause"
	ActionResume   Action = "resume"
	ActionSkip     Action = "skip"
	ActionStop     Action = "stop"
	ActionQueue    Action = "queue"
	ActionClear    Action = "clear"
	ActionNow      Action = "now"
	ActionControls Action = "controls"
)

var (
	ErrMissingSong       = errors.New("missing song")
	ErrMissingVoice      = errors.New("missing voice channel")
	ErrMissingGuild      = errors.New("missing guild")
	ErrNothingPlaying    = errors.New("nothing playing")
	ErrAlreadyPaused     = errors.New("already paused")
	ErrAlreadyPlaying    = errors.New("already playing")
	ErrDifferentVoice    = errors.New("already playing in another voice channel")
	ErrVoiceConnection   = errors.New("voice connection failed")
	ErrDependencyMissing = errors.New("music dependency missing")
	ErrTrackLookupFailed = errors.New("track lookup failed")
	ErrTrackStreamFailed = errors.New("track stream failed")
)

type Intent struct {
	Action Action
	Query  string
}

type Request struct {
	GuildID        string
	TextChannelID  string
	UserID         string
	VoiceChannelID string
	Intent         Intent
}

type Response struct {
	Content string
	Title   string
	URL     string
	Fields  []Field
	Actions []ResponseAction
}

type Field struct {
	Name   string
	Value  string
	Inline bool
}

type ResponseAction struct {
	Label string
	URL   string
}

type Track struct {
	ID            string
	Query         string
	Title         string
	URL           string
	Uploader      string
	Duration      time.Duration
	RequestedBy   string
	TextChannelID string
}

type Resolver interface {
	Resolve(ctx context.Context, query string) (Track, error)
}

type Streamer interface {
	Stream(ctx context.Context, track Track) (OpusFrameProvider, error)
}

type OpusFrameProvider interface {
	ProvideOpusFrame() ([]byte, error)
	Close()
}

type VoiceConnector interface {
	Connect(ctx context.Context, guildID string, channelID string) (VoiceSession, error)
}

type VoiceSession interface {
	ChannelID() string
	SetSpeaking(ctx context.Context, speaking bool) error
	WriteOpus(ctx context.Context, frame []byte) error
	Close(ctx context.Context)
}

func isExpectedPlaybackStop(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF)
}
