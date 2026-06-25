package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
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
	Sidecars      *SidecarManager
}

type YTDLP struct {
	ytdlpPath     string
	ffmpegPath    string
	lookupTimeout time.Duration
	audioBitrate  string
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
	return &YTDLP{
		ytdlpPath:     ytdlpPath,
		ffmpegPath:    ffmpegPath,
		lookupTimeout: lookupTimeout,
		audioBitrate:  audioBitrate,
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
		detail := strings.TrimSpace(stderr.String())
		if lookupCtx.Err() != nil {
			detail = lookupCtx.Err().Error()
		} else if detail == "" {
			detail = err.Error()
		}
		return Track{}, fmt.Errorf("%w: %s", ErrTrackLookupFailed, detail)
	}
	var metadata ytdlpMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return Track{}, fmt.Errorf("%w: parse metadata: %v", ErrTrackLookupFailed, err)
	}
	streamURL := strings.TrimSpace(metadata.URL)
	url := strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, streamURL))
	title := strings.TrimSpace(metadata.Title)
	if url == "" || title == "" {
		return Track{}, fmt.Errorf("%w: missing title or url", ErrTrackLookupFailed)
	}
	return Track{
		ID:            strings.TrimSpace(metadata.ID),
		Query:         query,
		Title:         title,
		URL:           url,
		StreamURL:     streamURL,
		StreamHeaders: cleanHTTPHeaders(metadata.HTTPHeaders),
		Uploader:      strings.TrimSpace(metadata.Uploader),
		Duration:      durationFromSeconds(metadata.Duration),
	}, nil
}

func (y *YTDLP) Suggestions(ctx context.Context, query string, limit int) ([]Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrMissingSong
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	tools, err := y.ensureTools(ctx)
	if err != nil {
		return nil, err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, y.lookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(lookupCtx, tools.YTDLPPath,
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--no-cache-dir",
		"--skip-download",
		fmt.Sprintf("ytsearch%d:%s", limit, query),
	)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if lookupCtx.Err() != nil {
			detail = lookupCtx.Err().Error()
		} else if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("%w: %s", ErrTrackLookupFailed, detail)
	}
	return parseYTDLPSuggestions(output, query, limit)
}

func (y *YTDLP) Stream(ctx context.Context, track Track) (OpusFrameProvider, error) {
	tools, err := y.ensureTools(ctx)
	if err != nil {
		return nil, err
	}
	directSource := strings.TrimSpace(track.StreamURL)
	source := strings.TrimSpace(firstNonEmpty(track.URL, track.Query, directSource))
	if source == "" {
		return nil, ErrMissingSong
	}
	if shouldStreamDirect(track) {
		return y.streamDirect(ctx, tools, directSource, track.StreamHeaders)
	}
	return y.streamViaYTDLP(ctx, tools, source)
}

func (y *YTDLP) streamViaYTDLP(ctx context.Context, tools ToolPaths, source string) (OpusFrameProvider, error) {
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

	ffmpegCmd := exec.CommandContext(streamCtx, tools.FFmpegPath, ffmpegOpusArgs("pipe:0", y.audioBitrate, nil, false)...)
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
	}, nil
}

func shouldStreamDirect(track Track) bool {
	directSource := strings.TrimSpace(track.StreamURL)
	if directSource == "" {
		return false
	}
	playbackLookupSource := strings.TrimSpace(firstNonEmpty(track.URL, track.Query))
	return playbackLookupSource == "" || playbackLookupSource == directSource
}

func (y *YTDLP) streamDirect(ctx context.Context, tools ToolPaths, source string, headers map[string]string) (OpusFrameProvider, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	ffmpegCmd := exec.CommandContext(streamCtx, tools.FFmpegPath, ffmpegOpusArgs(source, y.audioBitrate, headers, true)...)
	var ffmpegErr limitedBuffer
	ffmpegCmd.Stderr = &ffmpegErr
	ffmpegStdout, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: ffmpeg pipe: %v", ErrTrackStreamFailed, err)
	}
	if err := ffmpegCmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%w: start ffmpeg: %v", ErrTrackStreamFailed, err)
	}
	return &processOpusProvider{
		reader:    newOggOpusReader(ffmpegStdout),
		cancel:    cancel,
		ffmpeg:    ffmpegCmd,
		ffmpegErr: &ffmpegErr,
	}, nil
}

func ffmpegOpusArgs(input string, bitrate string, headers map[string]string, directHTTP bool) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if directHTTP {
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_on_network_error", "1",
			"-reconnect_delay_max", "5",
		)
		args = append(args, ffmpegHTTPHeaderArgs(headers)...)
	}
	return append(args,
		"-i", input,
		"-vn",
		"-sn",
		"-dn",
		"-map", "0:a:0",
		"-c:a", "libopus",
		"-b:a", bitrate,
		"-application", "audio",
		"-compression_level", "1",
		"-ar", "48000",
		"-ac", "2",
		"-frame_duration", "20",
		"-f", "opus",
		"pipe:1",
	)
}

func ffmpegHTTPHeaderArgs(headers map[string]string) []string {
	headers = cleanHTTPHeaders(headers)
	if len(headers) == 0 {
		return nil
	}
	args := []string{}
	if userAgent := headerValue(headers, "user-agent"); userAgent != "" {
		args = append(args, "-user_agent", userAgent)
	}
	if referer := headerValue(headers, "referer"); referer != "" {
		args = append(args, "-referer", referer)
	}
	var lines []string
	for name, value := range headers {
		switch strings.ToLower(name) {
		case "user-agent", "referer":
			continue
		}
		lines = append(lines, name+": "+value+"\r\n")
	}
	sort.Strings(lines)
	if len(lines) > 0 {
		args = append(args, "-headers", strings.Join(lines, ""))
	}
	return args
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func cleanHTTPHeaders(headers map[string]string) map[string]string {
	cleaned := map[string]string{}
	for name, value := range headers {
		name = cleanHeaderPart(name)
		value = cleanHeaderPart(value)
		if name == "" || value == "" {
			continue
		}
		cleaned[name] = value
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func cleanHeaderPart(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return value
}

type ytdlpMetadata struct {
	ID          string            `json:"id"`
	Type        string            `json:"_type"`
	Title       string            `json:"title"`
	WebpageURL  string            `json:"webpage_url"`
	OriginalURL string            `json:"original_url"`
	URL         string            `json:"url"`
	HTTPHeaders map[string]string `json:"http_headers"`
	Uploader    string            `json:"uploader"`
	Duration    float64           `json:"duration"`
	Entries     []ytdlpMetadata   `json:"entries"`
}

func parseYTDLPSuggestions(output []byte, query string, limit int) ([]Track, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	tracks := make([]Track, 0, limit)
	for {
		var metadata ytdlpMetadata
		if err := decoder.Decode(&metadata); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%w: parse suggestions: %v", ErrTrackLookupFailed, err)
		}
		if len(metadata.Entries) > 0 {
			for _, entry := range metadata.Entries {
				if track, ok := suggestionTrack(entry, query); ok {
					tracks = append(tracks, track)
				}
				if len(tracks) >= limit {
					return tracks, nil
				}
			}
			continue
		}
		if track, ok := suggestionTrack(metadata, query); ok {
			tracks = append(tracks, track)
		}
		if len(tracks) >= limit {
			return tracks, nil
		}
	}
	return tracks, nil
}

func suggestionTrack(metadata ytdlpMetadata, query string) (Track, bool) {
	title := strings.TrimSpace(metadata.Title)
	if title == "" {
		return Track{}, false
	}
	url := strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, metadata.URL))
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = ""
	}
	return Track{
		ID:       strings.TrimSpace(metadata.ID),
		Query:    query,
		Title:    title,
		URL:      url,
		Uploader: strings.TrimSpace(metadata.Uploader),
		Duration: durationFromSeconds(metadata.Duration),
	}, true
}

type processOpusProvider struct {
	reader    *oggOpusReader
	cancel    context.CancelFunc
	ytdlp     *exec.Cmd
	ffmpeg    *exec.Cmd
	ytdlpErr  *limitedBuffer
	ffmpegErr *limitedBuffer
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
		_ = p.wait()
	})
}

func (p *processOpusProvider) wait() error {
	p.waitOnce.Do(func() {
		var failures []string
		if p.ffmpeg != nil {
			ffmpegErr := p.ffmpeg.Wait()
			if ffmpegErr != nil {
				failures = append(failures, strings.TrimSpace(fmt.Sprintf("ffmpeg: %v %s", ffmpegErr, p.ffmpegErr.String())))
			}
		}
		if p.ytdlp != nil {
			ytdlpErr := p.ytdlp.Wait()
			if ytdlpErr != nil {
				failures = append(failures, strings.TrimSpace(fmt.Sprintf("yt-dlp: %v %s", ytdlpErr, p.ytdlpErr.String())))
			}
		}
		if len(failures) > 0 {
			p.waitErr = fmt.Errorf("%w: %s", ErrTrackStreamFailed, strings.Join(failures, "; "))
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
