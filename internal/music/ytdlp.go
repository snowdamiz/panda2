package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	defaultLookupTimeout = 20 * time.Second
	defaultAudioBitrate  = "128k"
)

type YTDLPConfig struct {
	YTDLPPath     string
	FFmpegPath    string
	LookupTimeout time.Duration
	AudioBitrate  string
	Logger        *slog.Logger
	Sidecars      *SidecarManager
}

type YTDLP struct {
	ytdlpPath     string
	ffmpegPath    string
	lookupTimeout time.Duration
	audioBitrate  string
	logger        *slog.Logger
	sidecars      *SidecarManager
	toolsMu       sync.RWMutex
}

func NewYTDLP(config YTDLPConfig) *YTDLP {
	lookupTimeout := config.LookupTimeout
	if lookupTimeout <= 0 {
		lookupTimeout = defaultLookupTimeout
	}
	audioBitrate := strings.TrimSpace(config.AudioBitrate)
	if audioBitrate == "" {
		audioBitrate = defaultAudioBitrate
	}
	ytdlpPath := strings.TrimSpace(config.YTDLPPath)
	ffmpegPath := strings.TrimSpace(config.FFmpegPath)
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &YTDLP{
		ytdlpPath:     ytdlpPath,
		ffmpegPath:    ffmpegPath,
		lookupTimeout: lookupTimeout,
		audioBitrate:  audioBitrate,
		logger:        logger,
		sidecars:      config.Sidecars,
	}
}

func (y *YTDLP) Resolve(ctx context.Context, query string) (Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Track{}, ErrMissingSong
	}
	tools, err := y.ensureTools(ctx)
	if err != nil {
		return Track{}, err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, y.lookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(lookupCtx, tools.YTDLPPath,
		"--dump-json",
		"--no-playlist",
		"--default-search", "ytsearch1",
		"--format", "bestaudio/best",
		"--no-warnings",
		"--no-cache-dir",
		"--skip-download",
		query,
	)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return Track{}, fmt.Errorf("%w: %s", ErrTrackLookupFailed, stderr.String())
	}
	var metadata ytdlpMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return Track{}, fmt.Errorf("%w: parse metadata: %v", ErrTrackLookupFailed, err)
	}
	url := strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, metadata.URL))
	title := strings.TrimSpace(metadata.Title)
	if url == "" || title == "" {
		return Track{}, fmt.Errorf("%w: missing title or url", ErrTrackLookupFailed)
	}
	return Track{
		ID:       strings.TrimSpace(metadata.ID),
		Query:    query,
		Title:    title,
		URL:      url,
		Uploader: strings.TrimSpace(metadata.Uploader),
		Duration: durationFromSeconds(metadata.Duration),
	}, nil
}

func (y *YTDLP) Stream(ctx context.Context, track Track) (OpusFrameProvider, error) {
	tools, err := y.ensureTools(ctx)
	if err != nil {
		return nil, err
	}
	source := strings.TrimSpace(firstNonEmpty(track.URL, track.Query))
	if source == "" {
		return nil, ErrMissingSong
	}

	streamCtx, cancel := context.WithCancel(ctx)
	ytdlpCmd := exec.CommandContext(streamCtx, tools.YTDLPPath,
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--no-cache-dir",
		"--format", "bestaudio/best",
		"--output", "-",
		source,
	)
	var ytdlpErr limitedBuffer
	ytdlpCmd.Stderr = &ytdlpErr
	ytdlpStdout, err := ytdlpCmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: yt-dlp pipe: %v", ErrTrackStreamFailed, err)
	}

	ffmpegCmd := exec.CommandContext(streamCtx, tools.FFmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-i", "pipe:0",
		"-vn",
		"-c:a", "libopus",
		"-b:a", y.audioBitrate,
		"-application", "audio",
		"-compression_level", "5",
		"-ar", "48000",
		"-ac", "2",
		"-frame_duration", "20",
		"-f", "opus",
		"pipe:1",
	)
	var ffmpegErr limitedBuffer
	ffmpegCmd.Stdin = ytdlpStdout
	ffmpegCmd.Stderr = &ffmpegErr
	ffmpegStdout, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: ffmpeg pipe: %v", ErrTrackStreamFailed, err)
	}

	if err := ytdlpCmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%w: start yt-dlp: %v", ErrTrackStreamFailed, err)
	}
	if err := ffmpegCmd.Start(); err != nil {
		cancel()
		_ = ytdlpCmd.Wait()
		return nil, fmt.Errorf("%w: start ffmpeg: %v", ErrTrackStreamFailed, err)
	}

	return &processOpusProvider{
		reader:    newOggOpusReader(ffmpegStdout),
		cancel:    cancel,
		ytdlp:     ytdlpCmd,
		ffmpeg:    ffmpegCmd,
		ytdlpErr:  &ytdlpErr,
		ffmpegErr: &ffmpegErr,
		logger:    y.logger,
	}, nil
}

type ytdlpMetadata struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	WebpageURL  string  `json:"webpage_url"`
	OriginalURL string  `json:"original_url"`
	URL         string  `json:"url"`
	Uploader    string  `json:"uploader"`
	Duration    float64 `json:"duration"`
}

type processOpusProvider struct {
	reader    *oggOpusReader
	cancel    context.CancelFunc
	ytdlp     *exec.Cmd
	ffmpeg    *exec.Cmd
	ytdlpErr  *limitedBuffer
	ffmpegErr *limitedBuffer
	logger    *slog.Logger
	once      sync.Once
	waitOnce  sync.Once
	waitErr   error
}

func (p *processOpusProvider) ProvideOpusFrame() ([]byte, error) {
	frame, err := p.reader.ProvideOpusFrame()
	if err == nil {
		return frame, nil
	}
	if errors.Is(err, io.EOF) {
		if waitErr := p.wait(); waitErr != nil {
			return nil, waitErr
		}
		return nil, io.EOF
	}
	return nil, err
}

func (p *processOpusProvider) Close() {
	p.once.Do(func() {
		p.cancel()
		if err := p.wait(); err != nil && p.logger != nil {
			p.logger.Debug("music stream process closed with error", slog.Any("err", err))
		}
	})
}

func (p *processOpusProvider) wait() error {
	p.waitOnce.Do(func() {
		ffmpegErr := p.ffmpeg.Wait()
		ytdlpErr := p.ytdlp.Wait()
		if ffmpegErr != nil {
			p.waitErr = fmt.Errorf("%w: ffmpeg: %v %s", ErrTrackStreamFailed, ffmpegErr, p.ffmpegErr.String())
			return
		}
		if ytdlpErr != nil {
			p.waitErr = fmt.Errorf("%w: yt-dlp: %v %s", ErrTrackStreamFailed, ytdlpErr, p.ytdlpErr.String())
		}
	})
	return p.waitErr
}

type limitedBuffer struct {
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	const maxBuffer = 4096
	remaining := maxBuffer - b.buf.Len()
	if remaining > 0 {
		if len(data) > remaining {
			_, _ = b.buf.Write(data[:remaining])
		} else {
			_, _ = b.buf.Write(data)
		}
	}
	return len(data), nil
}

func (b *limitedBuffer) String() string {
	return strings.TrimSpace(b.buf.String())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func durationFromSeconds(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func (y *YTDLP) ensureTools(ctx context.Context) (ToolPaths, error) {
	y.toolsMu.RLock()
	tools := ToolPaths{YTDLPPath: y.ytdlpPath, FFmpegPath: y.ffmpegPath}
	y.toolsMu.RUnlock()
	if toolsAvailable(tools) {
		return tools, nil
	}
	if y.sidecars == nil {
		return ToolPaths{}, fmt.Errorf("%w: server-side audio tools are unavailable", ErrDependencyMissing)
	}
	tools, err := y.sidecars.Ensure(ctx)
	if err != nil {
		return ToolPaths{}, fmt.Errorf("%w: %v", ErrDependencyMissing, err)
	}
	y.toolsMu.Lock()
	y.ytdlpPath = tools.YTDLPPath
	y.ffmpegPath = tools.FFmpegPath
	y.toolsMu.Unlock()
	return tools, nil
}
