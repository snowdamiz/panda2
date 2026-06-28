package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL          = "https://api.lemonfox.ai/v1"
	defaultLookupTimeout    = 20 * time.Second
	defaultChunkDuration    = 10 * time.Minute
	defaultProcessTimeout   = 30 * time.Minute
	defaultHTTPTimeout      = 2 * time.Minute
	lemonfoxUploadLimit     = 100 << 20
	lemonfoxResponseMaxSize = 4 << 20
)

var (
	ErrNotConfigured = errors.New("lemonfox api key is not configured")
	ErrMissingVideo  = errors.New("missing youtube video query")
)

type Config struct {
	APIKey         string
	BaseURL        string
	YTDLPPath      string
	FFmpegPath     string
	ToolProvider   ToolProvider
	HTTPClient     *http.Client
	LookupTimeout  time.Duration
	ChunkDuration  time.Duration
	ProcessTimeout time.Duration
}

type ToolPaths struct {
	YTDLPPath  string
	FFmpegPath string
}

type ToolProvider interface {
	Ensure(ctx context.Context) (ToolPaths, error)
}

type Service struct {
	apiKey         string
	baseURL        string
	ytdlpPath      string
	ffmpegPath     string
	toolProvider   ToolProvider
	client         *http.Client
	lookupTimeout  time.Duration
	chunkDuration  time.Duration
	processTimeout time.Duration
	toolsMu        sync.RWMutex
}

type SummaryRequest struct {
	Query    string
	Detail   string
	Language string
}

type SearchRequest struct {
	Query string
	Limit int
}

type SummaryResult struct {
	Title               string
	URL                 string
	Uploader            string
	Duration            time.Duration
	ResolvedQuery       string
	Transcript          string
	TranscriptChunkText []string
	ChunkCount          int
}

type VideoCandidate struct {
	ID           string
	Title        string
	URL          string
	Uploader     string
	ThumbnailURL string
	Duration     time.Duration
}

type videoMetadata struct {
	ID          string          `json:"id"`
	Type        string          `json:"_type"`
	Title       string          `json:"title"`
	WebpageURL  string          `json:"webpage_url"`
	OriginalURL string          `json:"original_url"`
	URL         string          `json:"url"`
	Uploader    string          `json:"uploader"`
	Thumbnail   string          `json:"thumbnail"`
	Thumbnails  []thumbnailInfo `json:"thumbnails"`
	Duration    float64         `json:"duration"`
	Entries     []videoMetadata `json:"entries"`
}

type thumbnailInfo struct {
	URL        string `json:"url"`
	Preference int    `json:"preference"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

type transcriptionResponse struct {
	Text     string                 `json:"text"`
	Segments []transcriptionSegment `json:"segments"`
}

type transcriptionSegment struct {
	Text string `json:"text"`
}

func NewService(config Config) *Service {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	lookupTimeout := config.LookupTimeout
	if lookupTimeout <= 0 {
		lookupTimeout = defaultLookupTimeout
	}
	chunkDuration := config.ChunkDuration
	if chunkDuration <= 0 {
		chunkDuration = defaultChunkDuration
	}
	processTimeout := config.ProcessTimeout
	if processTimeout <= 0 {
		processTimeout = defaultProcessTimeout
	}
	return &Service{
		apiKey:         strings.TrimSpace(config.APIKey),
		baseURL:        baseURL,
		ytdlpPath:      strings.TrimSpace(config.YTDLPPath),
		ffmpegPath:     strings.TrimSpace(config.FFmpegPath),
		toolProvider:   config.ToolProvider,
		client:         client,
		lookupTimeout:  lookupTimeout,
		chunkDuration:  chunkDuration,
		processTimeout: processTimeout,
	}
}

func (s *Service) Configured() bool {
	return s != nil && strings.TrimSpace(s.apiKey) != ""
}

func (s *Service) Summarize(ctx context.Context, request SummaryRequest) (SummaryResult, error) {
	if !s.Configured() {
		return SummaryResult{}, ErrNotConfigured
	}
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return SummaryResult{}, ErrMissingVideo
	}
	tools, err := s.ensureTools(ctx)
	if err != nil {
		return SummaryResult{}, err
	}
	metadata, err := s.resolve(ctx, tools, query)
	if err != nil {
		return SummaryResult{}, err
	}
	source := strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, metadata.URL, query))
	if source == "" {
		return SummaryResult{}, fmt.Errorf("youtube lookup failed: missing video url")
	}
	tempDir, err := os.MkdirTemp("", "panda-youtube-audio-*")
	if err != nil {
		return SummaryResult{}, fmt.Errorf("create temporary audio dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	chunks, err := s.extractAudioChunks(ctx, tools, source, tempDir)
	if err != nil {
		return SummaryResult{}, err
	}
	if len(chunks) == 0 {
		return SummaryResult{}, fmt.Errorf("youtube audio extraction produced no chunks")
	}

	chunkTexts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		text, err := s.transcribeChunk(ctx, chunk, request.Language)
		if err != nil {
			return SummaryResult{}, err
		}
		text = cleanTranscriptText(text)
		if text != "" {
			chunkTexts = append(chunkTexts, text)
		}
	}
	if len(chunkTexts) == 0 {
		return SummaryResult{}, fmt.Errorf("lemonfox returned an empty transcript")
	}

	return SummaryResult{
		Title:               strings.TrimSpace(metadata.Title),
		URL:                 strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, source)),
		Uploader:            strings.TrimSpace(metadata.Uploader),
		Duration:            durationFromSeconds(metadata.Duration),
		ResolvedQuery:       query,
		Transcript:          strings.Join(chunkTexts, "\n\n"),
		TranscriptChunkText: append([]string(nil), chunkTexts...),
		ChunkCount:          len(chunks),
	}, nil
}

func (s *Service) Search(ctx context.Context, request SearchRequest) ([]VideoCandidate, error) {
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return nil, ErrMissingVideo
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 3
	}
	if limit > 10 {
		limit = 10
	}
	tools, err := s.ensureTools(ctx)
	if err != nil {
		return nil, err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, s.lookupTimeout)
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
		return nil, fmt.Errorf("youtube search failed: %s", detail)
	}
	candidates := parseSearchCandidates(output, limit)
	return s.enrichSearchCandidates(lookupCtx, tools, candidates), nil
}

func (s *Service) resolve(ctx context.Context, tools ToolPaths, query string) (videoMetadata, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, s.lookupTimeout)
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
		return videoMetadata{}, fmt.Errorf("youtube lookup failed: %s", detail)
	}
	metadata, err := parseMetadata(output)
	if err != nil {
		return videoMetadata{}, err
	}
	if strings.TrimSpace(metadata.Title) == "" {
		return videoMetadata{}, fmt.Errorf("youtube lookup failed: missing title")
	}
	return metadata, nil
}

func parseMetadata(output []byte) (videoMetadata, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return videoMetadata{}, fmt.Errorf("youtube lookup failed: empty metadata")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var metadata videoMetadata
		if err := decoder.Decode(&metadata); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return videoMetadata{}, fmt.Errorf("youtube lookup failed: parse metadata: %w", err)
		}
		if len(metadata.Entries) > 0 {
			for _, entry := range metadata.Entries {
				if strings.TrimSpace(entry.Title) != "" {
					return entry, nil
				}
			}
		}
		if strings.TrimSpace(metadata.Title) != "" {
			return metadata, nil
		}
	}
	return videoMetadata{}, fmt.Errorf("youtube lookup failed: no video metadata")
}

func parseSearchCandidates(output []byte, limit int) []VideoCandidate {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	candidates := make([]VideoCandidate, 0, limit)
	for {
		var metadata videoMetadata
		if err := decoder.Decode(&metadata); err != nil {
			break
		}
		if len(metadata.Entries) > 0 {
			for _, entry := range metadata.Entries {
				if candidate, ok := videoCandidateFromMetadata(entry); ok {
					candidates = append(candidates, candidate)
				}
				if len(candidates) >= limit {
					return candidates
				}
			}
			continue
		}
		if candidate, ok := videoCandidateFromMetadata(metadata); ok {
			candidates = append(candidates, candidate)
		}
		if len(candidates) >= limit {
			return candidates
		}
	}
	return candidates
}

func videoCandidateFromMetadata(metadata videoMetadata) (VideoCandidate, bool) {
	title := strings.TrimSpace(metadata.Title)
	if title == "" {
		return VideoCandidate{}, false
	}
	id := strings.TrimSpace(metadata.ID)
	url := strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, metadata.URL))
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		if id != "" {
			url = "https://www.youtube.com/watch?v=" + id
		} else {
			url = ""
		}
	}
	if url == "" {
		return VideoCandidate{}, false
	}
	return VideoCandidate{
		ID:           id,
		Title:        title,
		URL:          url,
		Uploader:     strings.TrimSpace(metadata.Uploader),
		ThumbnailURL: bestThumbnailURL(metadata.Thumbnail, metadata.Thumbnails),
		Duration:     durationFromSeconds(metadata.Duration),
	}, true
}

func (s *Service) enrichSearchCandidates(ctx context.Context, tools ToolPaths, candidates []VideoCandidate) []VideoCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	enriched := append([]VideoCandidate(nil), candidates...)
	for i, candidate := range enriched {
		if !needsVideoCandidateEnrichment(candidate) {
			continue
		}
		metadata, err := s.lookupSearchCandidateMetadata(ctx, tools, candidate.URL)
		if err != nil {
			continue
		}
		if resolved, ok := videoCandidateFromMetadata(metadata); ok {
			enriched[i] = mergeVideoCandidate(candidate, resolved)
		}
	}
	return enriched
}

func needsVideoCandidateEnrichment(candidate VideoCandidate) bool {
	return strings.TrimSpace(candidate.ThumbnailURL) == "" ||
		strings.TrimSpace(candidate.Uploader) == "" ||
		candidate.Duration == 0
}

func (s *Service) lookupSearchCandidateMetadata(ctx context.Context, tools ToolPaths, source string) (videoMetadata, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return videoMetadata{}, ErrMissingVideo
	}
	cmd := exec.CommandContext(ctx, tools.YTDLPPath,
		"--dump-json",
		"--no-playlist",
		"--no-warnings",
		"--no-cache-dir",
		"--skip-download",
		source,
	)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			detail = ctx.Err().Error()
		} else if detail == "" {
			detail = err.Error()
		}
		return videoMetadata{}, fmt.Errorf("youtube metadata lookup failed: %s", detail)
	}
	return parseMetadata(output)
}

func mergeVideoCandidate(base VideoCandidate, extra VideoCandidate) VideoCandidate {
	if strings.TrimSpace(base.ID) == "" {
		base.ID = extra.ID
	}
	if strings.TrimSpace(base.Title) == "" {
		base.Title = extra.Title
	}
	if strings.TrimSpace(base.URL) == "" {
		base.URL = extra.URL
	}
	if strings.TrimSpace(base.Uploader) == "" {
		base.Uploader = extra.Uploader
	}
	if strings.TrimSpace(base.ThumbnailURL) == "" {
		base.ThumbnailURL = extra.ThumbnailURL
	}
	if base.Duration == 0 {
		base.Duration = extra.Duration
	}
	return base
}

func bestThumbnailURL(primary string, thumbnails []thumbnailInfo) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	best := ""
	bestPreference := -1 << 30
	bestArea := -1
	bestIndex := -1
	for i, thumbnail := range thumbnails {
		url := strings.TrimSpace(thumbnail.URL)
		if url == "" {
			continue
		}
		area := thumbnail.Width * thumbnail.Height
		if thumbnail.Preference > bestPreference ||
			(thumbnail.Preference == bestPreference && area > bestArea) ||
			(thumbnail.Preference == bestPreference && area == bestArea && i > bestIndex) {
			best = url
			bestPreference = thumbnail.Preference
			bestArea = area
			bestIndex = i
		}
	}
	return best
}

func (s *Service) extractAudioChunks(ctx context.Context, tools ToolPaths, source string, dir string) ([]string, error) {
	processCtx, cancel := context.WithTimeout(ctx, s.processTimeout)
	defer cancel()
	ytdlpCmd := exec.CommandContext(processCtx, tools.YTDLPPath,
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
		return nil, fmt.Errorf("youtube audio extraction failed: yt-dlp pipe: %w", err)
	}

	pattern := filepath.Join(dir, "chunk-%05d.wav")
	ffmpegCmd := exec.CommandContext(processCtx, tools.FFmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-i", "pipe:0",
		"-vn",
		"-sn",
		"-dn",
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		"-f", "segment",
		"-segment_time", strconv.Itoa(int(s.chunkDuration.Seconds())),
		"-reset_timestamps", "1",
		"-segment_format", "wav",
		pattern,
	)
	var ffmpegErr limitedBuffer
	ffmpegCmd.Stdin = ytdlpStdout
	ffmpegCmd.Stderr = &ffmpegErr

	if err := ytdlpCmd.Start(); err != nil {
		return nil, fmt.Errorf("youtube audio extraction failed: start yt-dlp: %w", err)
	}
	if err := ffmpegCmd.Start(); err != nil {
		_ = ytdlpCmd.Wait()
		return nil, fmt.Errorf("youtube audio extraction failed: start ffmpeg: %w", err)
	}

	ffmpegWaitErr := ffmpegCmd.Wait()
	ytdlpWaitErr := ytdlpCmd.Wait()
	if processCtx.Err() != nil {
		return nil, fmt.Errorf("youtube audio extraction failed: %w", processCtx.Err())
	}
	var failures []string
	if ffmpegWaitErr != nil {
		failures = append(failures, strings.TrimSpace(fmt.Sprintf("ffmpeg: %v %s", ffmpegWaitErr, ffmpegErr.String())))
	}
	if ytdlpWaitErr != nil {
		failures = append(failures, strings.TrimSpace(fmt.Sprintf("yt-dlp: %v %s", ytdlpWaitErr, ytdlpErr.String())))
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("youtube audio extraction failed: %s", strings.Join(failures, "; "))
	}

	chunks, err := filepath.Glob(filepath.Join(dir, "chunk-*.wav"))
	if err != nil {
		return nil, err
	}
	sort.Strings(chunks)
	return chunks, nil
}

func (s *Service) transcribeChunk(ctx context.Context, path string, language string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() <= 0 {
		return "", fmt.Errorf("audio chunk %s is empty", filepath.Base(path))
	}
	if info.Size() > lemonfoxUploadLimit {
		return "", fmt.Errorf("audio chunk %s exceeds Lemonfox upload limit", filepath.Base(path))
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		return "", err
	}
	if value := strings.TrimSpace(language); value != "" {
		if err := writer.WriteField("language", value); err != nil {
			return "", err
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, lemonfoxResponseMaxSize+1))
	if readErr != nil {
		return "", readErr
	}
	if len(data) > lemonfoxResponseMaxSize {
		return "", fmt.Errorf("lemonfox transcription response exceeded %d bytes", lemonfoxResponseMaxSize)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("lemonfox transcription failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var decoded transcriptionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", fmt.Errorf("parse lemonfox transcription response: %w", err)
	}
	if text := strings.TrimSpace(decoded.Text); text != "" {
		return text, nil
	}
	segments := make([]string, 0, len(decoded.Segments))
	for _, segment := range decoded.Segments {
		if text := strings.TrimSpace(segment.Text); text != "" {
			segments = append(segments, text)
		}
	}
	return strings.Join(segments, " "), nil
}

func (s *Service) ensureTools(ctx context.Context) (ToolPaths, error) {
	s.toolsMu.RLock()
	tools := ToolPaths{YTDLPPath: s.ytdlpPath, FFmpegPath: s.ffmpegPath}
	s.toolsMu.RUnlock()
	if toolsAvailable(tools) {
		return tools, nil
	}
	if s.toolProvider == nil {
		return ToolPaths{}, fmt.Errorf("server-side youtube audio tools are unavailable")
	}
	tools, err := s.toolProvider.Ensure(ctx)
	if err != nil {
		return ToolPaths{}, fmt.Errorf("server-side youtube audio tools are unavailable: %w", err)
	}
	s.toolsMu.Lock()
	s.ytdlpPath = tools.YTDLPPath
	s.ffmpegPath = tools.FFmpegPath
	s.toolsMu.Unlock()
	return tools, nil
}

func toolsAvailable(paths ToolPaths) bool {
	return executable(paths.YTDLPPath) && executable(paths.FFmpegPath)
}

func executable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	_, err := exec.LookPath(path)
	return err == nil
}

func cleanTranscriptText(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	return strings.Join(fields, " ")
}

func durationFromSeconds(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
