package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sn0w/panda2/internal/objectstore"
)

const (
	defaultBaseURL          = "https://api.lemonfox.ai/v1"
	defaultLookupTimeout    = 20 * time.Second
	defaultChunkDuration    = 10 * time.Minute
	defaultProcessTimeout   = 30 * time.Minute
	defaultHTTPTimeout      = 2 * time.Minute
	defaultClipMinDuration  = 5 * time.Second
	defaultClipMaxDuration  = 90 * time.Second
	defaultClipMaxBytes     = 100 << 20
	lemonfoxUploadLimit     = 100 << 20
	lemonfoxResponseMaxSize = 4 << 20
	youtubePageMaxSize      = 4 << 20
	youtubeFeedMaxSize      = 1 << 20
)

var (
	ErrNotConfigured = errors.New("lemonfox api key is not configured")
	ErrMissingVideo  = errors.New("missing youtube video query")

	youtubeRSSURLPattern     = regexp.MustCompile(`"rssUrl":"([^"]+)`)
	youtubeExternalIDPattern = regexp.MustCompile(`"externalId":"(UC[0-9A-Za-z_-]+)`)
	youtubeChannelIDPattern  = regexp.MustCompile(`"channelId":"(UC[0-9A-Za-z_-]+)`)
)

type Config struct {
	APIKey              string
	BaseURL             string
	YTDLPPath           string
	FFmpegPath          string
	ToolProvider        ToolProvider
	ClipDetector        ClipDetector
	ClipPlanner         ClipCompositionPlanner
	ClipUploader        ObjectUploader
	HTTPClient          *http.Client
	LookupTimeout       time.Duration
	ChunkDuration       time.Duration
	ProcessTimeout      time.Duration
	ClipMinDuration     time.Duration
	ClipMaxDuration     time.Duration
	ClipMaxBytes        int64
	ThumbnailMaxCount   int
	ThumbnailMaxEdge    int
	VerticalResolution  ClipResolution
	LandscapeResolution ClipResolution
}

type ToolPaths struct {
	YTDLPPath  string
	FFmpegPath string
}

type ToolProvider interface {
	Ensure(ctx context.Context) (ToolPaths, error)
}

type ClipDetector interface {
	Configured() bool
	Detect(ctx context.Context, request ClipDetectionRequest) (ClipDetectionResult, error)
}

type ClipCompositionPlanner interface {
	Configured() bool
	Plan(ctx context.Context, request ClipCompositionRequest) (ClipCompositionResult, error)
}

type ObjectUploader interface {
	Configured() bool
	Upload(ctx context.Context, request objectstore.UploadRequest) (objectstore.UploadResult, error)
}

type Service struct {
	apiKey              string
	baseURL             string
	ytdlpPath           string
	ffmpegPath          string
	toolProvider        ToolProvider
	clipDetector        ClipDetector
	clipPlanner         ClipCompositionPlanner
	clipUploader        ObjectUploader
	client              *http.Client
	lookupTimeout       time.Duration
	chunkDuration       time.Duration
	processTimeout      time.Duration
	clipMinDuration     time.Duration
	clipMaxDuration     time.Duration
	clipMaxBytes        int64
	thumbnailMaxCount   int
	thumbnailMaxEdge    int
	verticalResolution  ClipResolution
	landscapeResolution ClipResolution
	toolsMu             sync.RWMutex
}

type SummaryRequest struct {
	Query    string
	Detail   string
	Language string
}

type SearchRequest struct {
	Query      string
	Limit      int
	Source     string
	ChannelURL string
	Handle     string
	SortBy     string
	Date       string
	DateAfter  string
	DateBefore string
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

type ClipRequest struct {
	Query              string
	Instructions       string
	Language           string
	AspectRatio        string
	LayoutInstructions string
	GuildID            string
	RequestID          string
	Progress           func(ClipProgress)
}

type ClipProgress struct {
	Status string
}

type ClipResult struct {
	Title                  string
	URL                    string
	Uploader               string
	Duration               time.Duration
	TranscriptSegmentCount int
	Clips                  []RenderedClip
}

type RenderedClip struct {
	Rank                  int
	Title                 string
	Type                  string
	WatchURL              string
	ObjectKey             string
	Duration              time.Duration
	SourceStartSeconds    float64
	SourceEndSeconds      float64
	Segments              []RenderedClipSegment
	Reason                string
	Confidence            float64
	ViralityScore         int
	HookScore             int
	RetentionScore        int
	ShareabilityScore     int
	DurationPolicy        string
	ExceptionReason       string
	OutputSizeBytes       int64
	AspectRatio           string
	LayoutMode            string
	CompositionReason     string
	CompositionConfidence float64
}

type RenderedClipSegment struct {
	StartSeconds float64
	EndSeconds   float64
	Duration     time.Duration
	Transcript   string
}

type TranscriptSegment struct {
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Text         string  `json:"text"`
}

type ClipDetectionRequest struct {
	Title              string
	URL                string
	Uploader           string
	Duration           time.Duration
	Instructions       string
	MinDurationSeconds float64
	MaxDurationSeconds float64
	MaxClips           int
	Segments           []TranscriptSegment
}

type ClipDetectionResult struct {
	Clips []ClipDecision `json:"clips"`
}

type ClipDecision struct {
	Rank              int                   `json:"rank"`
	Title             string                `json:"title"`
	Type              string                `json:"type"`
	Segments          []ClipDecisionSegment `json:"segments"`
	Reason            string                `json:"reason"`
	Confidence        float64               `json:"confidence"`
	ViralityScore     int                   `json:"virality_score"`
	HookScore         int                   `json:"hook_score"`
	RetentionScore    int                   `json:"retention_score"`
	ShareabilityScore int                   `json:"shareability_score"`
	DurationPolicy    string                `json:"duration_policy"`
	ExceptionReason   string                `json:"exception_reason"`
}

type ClipDecisionSegment struct {
	StartSegmentIndex *int    `json:"start_segment_index,omitempty"`
	EndSegmentIndex   *int    `json:"end_segment_index,omitempty"`
	StartSeconds      float64 `json:"start_seconds"`
	EndSeconds        float64 `json:"end_seconds"`
	Transcript        string  `json:"transcript"`
}

type ClipResolution struct {
	Width  int
	Height int
}

type ClipCompositionRequest struct {
	Title              string
	URL                string
	Uploader           string
	RequestedAspect    string
	LayoutInstructions string
	Clip               ClipDecision
	TranscriptTimeline []ClipCompositionTranscriptSegment
	Thumbnails         []ClipThumbnail
}

type ClipThumbnail struct {
	ID                  string
	SourceSeconds       float64
	ClipSegmentIndex    int
	ClipOffsetSeconds   float64
	SampleReason        string
	Width               int
	Height              int
	MIMEType            string
	Data                []byte
	TranscriptNearFrame string
}

type ClipCompositionTranscriptSegment struct {
	ClipSegmentIndex int     `json:"clip_segment_index"`
	StartSeconds     float64 `json:"start_seconds"`
	EndSeconds       float64 `json:"end_seconds"`
	Text             string  `json:"text"`
}

type ClipCompositionResult struct {
	AspectRatio string                `json:"aspect_ratio"`
	LayoutMode  string                `json:"layout_mode"`
	Plans       []ClipFrameRenderPlan `json:"plans"`
	Confidence  float64               `json:"confidence"`
	Reason      string                `json:"reason"`
}

type ClipFrameRenderPlan struct {
	AppliesToSegmentIndex int                `json:"applies_to_segment_index"`
	SourceStartSeconds    float64            `json:"source_start_seconds"`
	SourceEndSeconds      float64            `json:"source_end_seconds"`
	Regions               []ClipRenderRegion `json:"regions"`
}

type ClipRenderRegion struct {
	Role       string   `json:"role"`
	SourceRect ClipRect `json:"source_rect"`
	OutputRect ClipRect `json:"output_rect"`
	Fit        string   `json:"fit"`
	ZIndex     int      `json:"z_index"`
}

type ClipRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type VideoCandidate struct {
	ID           string
	Title        string
	URL          string
	Uploader     string
	ThumbnailURL string
	Duration     time.Duration
	UploadDate   time.Time
}

type videoMetadata struct {
	ID               string          `json:"id"`
	Type             string          `json:"_type"`
	Title            string          `json:"title"`
	WebpageURL       string          `json:"webpage_url"`
	OriginalURL      string          `json:"original_url"`
	URL              string          `json:"url"`
	IEKey            string          `json:"ie_key"`
	Extractor        string          `json:"extractor"`
	ExtractorKey     string          `json:"extractor_key"`
	Uploader         string          `json:"uploader"`
	UploaderID       string          `json:"uploader_id"`
	UploaderURL      string          `json:"uploader_url"`
	Channel          string          `json:"channel"`
	ChannelID        string          `json:"channel_id"`
	ChannelURL       string          `json:"channel_url"`
	Thumbnail        string          `json:"thumbnail"`
	Thumbnails       []thumbnailInfo `json:"thumbnails"`
	Duration         float64         `json:"duration"`
	UploadDate       string          `json:"upload_date"`
	Timestamp        int64           `json:"timestamp"`
	PlaylistUploader string          `json:"playlist_uploader"`
	PlaylistChannel  string          `json:"playlist_channel"`
	Entries          []videoMetadata `json:"entries"`
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
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type chunkTranscription struct {
	Text     string
	Segments []transcriptionSegment
}

type youtubeFeed struct {
	Title   string             `xml:"title"`
	Author  youtubeFeedAuthor  `xml:"author"`
	Entries []youtubeFeedEntry `xml:"entry"`
}

type youtubeFeedAuthor struct {
	Name string `xml:"name"`
}

type youtubeFeedEntry struct {
	VideoID    string            `xml:"videoId"`
	Title      string            `xml:"title"`
	Link       youtubeFeedLink   `xml:"link"`
	Author     youtubeFeedAuthor `xml:"author"`
	Published  string            `xml:"published"`
	MediaGroup youtubeMediaGroup `xml:"group"`
}

type youtubeFeedLink struct {
	Href string `xml:"href,attr"`
}

type youtubeMediaGroup struct {
	Title     string                `xml:"title"`
	Thumbnail youtubeMediaThumbnail `xml:"thumbnail"`
}

type youtubeMediaThumbnail struct {
	URL string `xml:"url,attr"`
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
	clipMinDuration := config.ClipMinDuration
	if clipMinDuration <= 0 {
		clipMinDuration = defaultClipMinDuration
	}
	clipMaxDuration := config.ClipMaxDuration
	if clipMaxDuration <= 0 {
		clipMaxDuration = defaultClipMaxDuration
	}
	clipMaxBytes := config.ClipMaxBytes
	if clipMaxBytes <= 0 {
		clipMaxBytes = defaultClipMaxBytes
	}
	thumbnailMaxCount := config.ThumbnailMaxCount
	if thumbnailMaxCount <= 0 {
		thumbnailMaxCount = 12
	}
	thumbnailMaxEdge := config.ThumbnailMaxEdge
	if thumbnailMaxEdge <= 0 {
		thumbnailMaxEdge = 720
	}
	verticalResolution := config.VerticalResolution
	if verticalResolution.Width <= 0 || verticalResolution.Height <= 0 {
		verticalResolution = ClipResolution{Width: 1080, Height: 1920}
	}
	landscapeResolution := config.LandscapeResolution
	if landscapeResolution.Width <= 0 || landscapeResolution.Height <= 0 {
		landscapeResolution = ClipResolution{Width: 1920, Height: 1080}
	}
	return &Service{
		apiKey:              strings.TrimSpace(config.APIKey),
		baseURL:             baseURL,
		ytdlpPath:           strings.TrimSpace(config.YTDLPPath),
		ffmpegPath:          strings.TrimSpace(config.FFmpegPath),
		toolProvider:        config.ToolProvider,
		clipDetector:        config.ClipDetector,
		clipPlanner:         config.ClipPlanner,
		clipUploader:        config.ClipUploader,
		client:              client,
		lookupTimeout:       lookupTimeout,
		chunkDuration:       chunkDuration,
		processTimeout:      processTimeout,
		clipMinDuration:     clipMinDuration,
		clipMaxDuration:     clipMaxDuration,
		clipMaxBytes:        clipMaxBytes,
		thumbnailMaxCount:   thumbnailMaxCount,
		thumbnailMaxEdge:    thumbnailMaxEdge,
		verticalResolution:  verticalResolution,
		landscapeResolution: landscapeResolution,
	}
}

func (s *Service) Configured() bool {
	return s != nil && strings.TrimSpace(s.apiKey) != ""
}

func (s *Service) ClipConfigured() bool {
	return s != nil &&
		s.Configured() &&
		s.clipDetector != nil &&
		s.clipDetector.Configured() &&
		s.clipPlanner != nil &&
		s.clipPlanner.Configured() &&
		s.clipUploader != nil &&
		s.clipUploader.Configured()
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
	lookupCtx, cancel := context.WithTimeout(ctx, s.lookupTimeout)
	defer cancel()
	if youtubeSearchUsesChannelUploads(request) {
		if candidates, err := s.searchChannelUploadsFeed(lookupCtx, request, limit); err == nil && len(candidates) > 0 {
			return candidates, nil
		}
		tools, err := s.ensureTools(ctx)
		if err != nil {
			return nil, err
		}
		return s.searchChannelUploadsWithYTDLP(lookupCtx, tools, request, limit)
	}
	tools, err := s.ensureTools(ctx)
	if err != nil {
		return nil, err
	}
	searchLimit := youtubeSearchFetchLimit(limit, request)
	args := []string{
		"--dump-json",
		"--flat-playlist",
		"--no-warnings",
		"--no-cache-dir",
		"--skip-download",
	}
	if date := normalizedYTDLPDate(request.Date); date != "" {
		args = append(args, "--date", date)
	}
	if dateAfter := normalizedYTDLPDate(request.DateAfter); dateAfter != "" {
		args = append(args, "--dateafter", dateAfter)
	}
	if dateBefore := normalizedYTDLPDate(request.DateBefore); dateBefore != "" {
		args = append(args, "--datebefore", dateBefore)
	}
	args = append(args, fmt.Sprintf("ytsearch%d:%s", searchLimit, query))
	cmd := exec.CommandContext(lookupCtx, tools.YTDLPPath, args...)
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
	candidates := parseSearchCandidates(output, searchLimit)
	candidates = s.enrichSearchCandidates(lookupCtx, tools, candidates)
	candidates = filterSearchCandidatesByDate(candidates, request)
	if youtubeSearchSortsByUploadDate(request.SortBy) {
		sortSearchCandidatesByUploadDate(candidates)
	}
	return limitSearchCandidates(candidates, limit), nil
}

func youtubeSearchUsesChannelUploads(request SearchRequest) bool {
	switch strings.ToLower(strings.TrimSpace(request.Source)) {
	case "channel_uploads", "channel", "uploads", "latest_uploads":
		return true
	default:
		return false
	}
}

func (s *Service) searchChannelUploadsFeed(ctx context.Context, request SearchRequest, limit int) ([]VideoCandidate, error) {
	feedURLs, err := s.channelFeedURLs(ctx, request)
	if err != nil {
		return nil, err
	}
	fetchLimit := youtubeChannelUploadFetchLimit(limit, request)
	var lastErr error
	for _, feedURL := range feedURLs {
		candidates, err := s.channelFeedCandidates(ctx, feedURL, fetchLimit)
		if err != nil {
			lastErr = err
			continue
		}
		candidates = filterSearchCandidatesByDate(candidates, request)
		sortSearchCandidatesByUploadDate(candidates)
		if len(candidates) > 0 {
			return limitSearchCandidates(candidates, limit), nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrMissingVideo
}

func (s *Service) channelFeedURLs(ctx context.Context, request SearchRequest) ([]string, error) {
	feedURLs := []string{}
	add := func(feedURL string) {
		feedURL = strings.TrimSpace(feedURL)
		if feedURL == "" {
			return
		}
		for _, existing := range feedURLs {
			if existing == feedURL {
				return
			}
		}
		feedURLs = append(feedURLs, feedURL)
	}
	for _, source := range []string{request.ChannelURL, normalizedYouTubeHandle(request.Handle), request.Query} {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		if source != request.Query || youtubeChannelSourceLooksExplicit(source) {
			feedURL, err := s.channelFeedURLFromSource(ctx, source)
			if err == nil {
				add(feedURL)
			}
		}
	}
	if len(feedURLs) > 0 {
		return feedURLs, nil
	}
	feedURL, err := s.resolveChannelFeedURL(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	add(feedURL)
	return feedURLs, nil
}

func (s *Service) channelFeedURLFromSource(ctx context.Context, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", ErrMissingVideo
	}
	if strings.HasPrefix(source, "UC") && !strings.Contains(source, "/") {
		return youtubeChannelFeedURL(source), nil
	}
	if strings.HasPrefix(source, "@") {
		return s.resolveChannelPageFeedURL(ctx, "https://www.youtube.com/"+source+"/videos")
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrMissingVideo
	}
	channelID := youtubeChannelIDFromPath(parsed.Path)
	if channelID != "" {
		return youtubeChannelFeedURL(channelID), nil
	}
	return s.resolveChannelPageFeedURL(ctx, channelUploadsURL(source))
}

func (s *Service) resolveChannelFeedURL(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", ErrMissingVideo
	}
	searchURL := "https://www.youtube.com/results?search_query=" + url.QueryEscape(query)
	body, err := s.fetchYouTubeURL(ctx, searchURL, youtubePageMaxSize)
	if err != nil {
		return "", err
	}
	if feedURL := youtubeFeedURLFromPage(body); feedURL != "" {
		return feedURL, nil
	}
	if channelID := youtubeChannelIDFromPage(body); channelID != "" {
		return youtubeChannelFeedURL(channelID), nil
	}
	return "", fmt.Errorf("youtube channel lookup failed: no channel feed found")
}

func (s *Service) resolveChannelPageFeedURL(ctx context.Context, pageURL string) (string, error) {
	pageURL = strings.TrimSpace(pageURL)
	if pageURL == "" {
		return "", ErrMissingVideo
	}
	body, err := s.fetchYouTubeURL(ctx, pageURL, youtubePageMaxSize)
	if err != nil {
		return "", err
	}
	if feedURL := youtubeFeedURLFromPage(body); feedURL != "" {
		return feedURL, nil
	}
	if channelID := youtubeExternalIDFromPage(body); channelID != "" {
		return youtubeChannelFeedURL(channelID), nil
	}
	if channelID := youtubeChannelIDFromPage(body); channelID != "" {
		return youtubeChannelFeedURL(channelID), nil
	}
	return "", fmt.Errorf("youtube channel lookup failed: no channel feed found")
}

func (s *Service) channelFeedCandidates(ctx context.Context, feedURL string, limit int) ([]VideoCandidate, error) {
	body, err := s.fetchYouTubeURL(ctx, feedURL, youtubeFeedMaxSize)
	if err != nil {
		return nil, err
	}
	var feed youtubeFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("youtube channel feed lookup failed: parse feed: %w", err)
	}
	candidates := make([]VideoCandidate, 0, limit)
	for _, entry := range feed.Entries {
		candidate, ok := videoCandidateFromFeedEntry(entry, feed)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
		if len(candidates) >= limit {
			break
		}
	}
	return candidates, nil
}

func (s *Service) fetchYouTubeURL(ctx context.Context, rawURL string, maxSize int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("youtube lookup failed: %w", err)
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Panda/1.0)")
	response, err := s.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("youtube lookup failed: %s", ctx.Err().Error())
		}
		return nil, fmt.Errorf("youtube lookup failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("youtube lookup failed: http %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("youtube lookup failed: read response: %w", err)
	}
	if int64(len(body)) > maxSize {
		return nil, fmt.Errorf("youtube lookup failed: response too large")
	}
	return body, nil
}

func videoCandidateFromFeedEntry(entry youtubeFeedEntry, feed youtubeFeed) (VideoCandidate, bool) {
	id := strings.TrimSpace(entry.VideoID)
	title := strings.TrimSpace(firstNonEmpty(entry.MediaGroup.Title, entry.Title))
	url := strings.TrimSpace(entry.Link.Href)
	if url == "" && id != "" {
		url = "https://www.youtube.com/watch?v=" + id
	}
	if title == "" || url == "" {
		return VideoCandidate{}, false
	}
	return VideoCandidate{
		ID:           id,
		Title:        title,
		URL:          url,
		Uploader:     strings.TrimSpace(firstNonEmpty(entry.Author.Name, feed.Author.Name, feed.Title)),
		ThumbnailURL: strings.TrimSpace(entry.MediaGroup.Thumbnail.URL),
		UploadDate:   parseFeedTime(entry.Published),
	}, true
}

func youtubeChannelIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "channel" && i+1 < len(parts) && strings.HasPrefix(parts[i+1], "UC") {
			return parts[i+1]
		}
	}
	return ""
}

func youtubeFeedURLFromPage(body []byte) string {
	if match := youtubeRSSURLPattern.FindSubmatch(body); len(match) == 2 {
		return decodeYouTubeEscapedString(string(match[1]))
	}
	return ""
}

func youtubeExternalIDFromPage(body []byte) string {
	if match := youtubeExternalIDPattern.FindSubmatch(body); len(match) == 2 {
		return string(match[1])
	}
	return ""
}

func youtubeChannelIDFromPage(body []byte) string {
	if match := youtubeChannelIDPattern.FindSubmatch(body); len(match) == 2 {
		return string(match[1])
	}
	return ""
}

func youtubeChannelFeedURL(channelID string) string {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ""
	}
	return "https://www.youtube.com/feeds/videos.xml?channel_id=" + url.QueryEscape(channelID)
}

func decodeYouTubeEscapedString(value string) string {
	if decoded, err := strconv.Unquote(`"` + value + `"`); err == nil {
		return decoded
	}
	value = strings.ReplaceAll(value, `\/`, `/`)
	value = strings.ReplaceAll(value, `\u0026`, "&")
	return value
}

func parseFeedTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func (s *Service) searchChannelUploadsWithYTDLP(ctx context.Context, tools ToolPaths, request SearchRequest, limit int) ([]VideoCandidate, error) {
	searchLimit := youtubeChannelUploadFetchLimit(limit, request)
	sources := channelUploadSources(request)
	var lastErr error
	if len(sources) == 0 {
		resolved, err := s.resolveChannelUploadSources(ctx, tools, request.Query)
		if err != nil {
			lastErr = err
		}
		sources = resolved
	}
	for _, source := range sources {
		candidates, err := s.channelUploadCandidates(ctx, tools, source, searchLimit, request)
		if err != nil {
			lastErr = err
			continue
		}
		candidates = filterSearchCandidatesByDate(candidates, request)
		sortSearchCandidatesByUploadDate(candidates)
		if len(candidates) > 0 {
			return limitSearchCandidates(candidates, limit), nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

func (s *Service) channelUploadCandidates(ctx context.Context, tools ToolPaths, source string, limit int, request SearchRequest) ([]VideoCandidate, error) {
	args := []string{
		"--dump-json",
		"--flat-playlist",
		"--extractor-args", "youtubetab:approximate_date",
		"--playlist-end", strconv.Itoa(limit),
		"--no-warnings",
		"--no-cache-dir",
		"--skip-download",
	}
	if date := normalizedYTDLPDate(request.Date); date != "" {
		args = append(args, "--date", date)
	}
	if dateAfter := normalizedYTDLPDate(request.DateAfter); dateAfter != "" {
		args = append(args, "--dateafter", dateAfter)
	}
	if dateBefore := normalizedYTDLPDate(request.DateBefore); dateBefore != "" {
		args = append(args, "--datebefore", dateBefore)
	}
	args = append(args, source)
	cmd := exec.CommandContext(ctx, tools.YTDLPPath, args...)
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
		return nil, fmt.Errorf("youtube channel uploads lookup failed: %s", detail)
	}
	return parseSearchCandidates(output, limit), nil
}

func channelUploadSources(request SearchRequest) []string {
	sources := []string{}
	add := func(source string) {
		source = strings.TrimSpace(source)
		if source == "" {
			return
		}
		source = channelUploadsURL(source)
		if source == "" {
			return
		}
		for _, existing := range sources {
			if existing == source {
				return
			}
		}
		sources = append(sources, source)
	}
	addExplicit := func(source string) {
		if youtubeChannelSourceLooksExplicit(source) {
			add(source)
		}
	}
	addHandle := func(handle string) {
		handle = normalizedYouTubeHandle(handle)
		if handle == "" {
			return
		}
		add(handle)
	}
	addExplicit(request.ChannelURL)
	addHandle(request.Handle)
	if youtubeChannelSourceLooksExplicit(request.Query) {
		add(request.Query)
	}
	return sources
}

func channelUploadsURL(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if strings.HasPrefix(source, "@") {
		return "https://www.youtube.com/" + source + "/videos"
	}
	if strings.HasPrefix(source, "UC") && !strings.Contains(source, "/") {
		return "https://www.youtube.com/channel/" + source + "/videos"
	}
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return source
	}
	trimmed := strings.TrimRight(source, "/")
	for _, suffix := range []string{"/videos", "/shorts", "/streams", "/featured", "/community"} {
		if strings.HasSuffix(trimmed, suffix) {
			trimmed = strings.TrimSuffix(trimmed, suffix)
			break
		}
	}
	return trimmed + "/videos"
}

func normalizedYouTubeHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" ||
		strings.HasPrefix(handle, "@") ||
		strings.HasPrefix(handle, "http://") ||
		strings.HasPrefix(handle, "https://") {
		return handle
	}
	return "@" + handle
}

func youtubeChannelSourceLooksExplicit(source string) bool {
	source = strings.TrimSpace(source)
	return strings.HasPrefix(source, "@") ||
		(strings.HasPrefix(source, "UC") && !strings.Contains(source, "/")) ||
		strings.Contains(source, "youtube.com/@") ||
		strings.Contains(source, "youtube.com/channel/") ||
		strings.Contains(source, "youtube.com/c/") ||
		strings.Contains(source, "youtube.com/user/")
}

func (s *Service) resolveChannelUploadSources(ctx context.Context, tools ToolPaths, query string) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrMissingVideo
	}
	cmd := exec.CommandContext(ctx, tools.YTDLPPath,
		"--dump-json",
		"--flat-playlist",
		"--playlist-end", "5",
		"--no-warnings",
		"--no-cache-dir",
		"--skip-download",
		fmt.Sprintf("ytsearch%d:%s", 5, query),
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
		return nil, fmt.Errorf("youtube channel lookup failed: %s", detail)
	}
	return parseChannelUploadSources(output), nil
}

func parseChannelUploadSources(output []byte) []string {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil
	}
	sources := []string{}
	add := func(source string) {
		source = channelUploadsURL(source)
		if source == "" {
			return
		}
		for _, existing := range sources {
			if existing == source {
				return
			}
		}
		sources = append(sources, source)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var metadata videoMetadata
		if err := decoder.Decode(&metadata); err != nil {
			break
		}
		addChannelUploadSourcesFromMetadata(metadata, add)
		for _, entry := range metadata.Entries {
			addChannelUploadSourcesFromMetadata(entry, add)
		}
	}
	return sources
}

func addChannelUploadSourcesFromMetadata(metadata videoMetadata, add func(string)) {
	add(metadata.ChannelURL)
	add(metadata.UploaderURL)
	if strings.HasPrefix(strings.TrimSpace(metadata.UploaderID), "@") {
		add(metadata.UploaderID)
	}
	if strings.HasPrefix(strings.TrimSpace(metadata.ChannelID), "UC") {
		add(metadata.ChannelID)
	}
	if metadataIsYouTubeTab(metadata) {
		add(metadata.WebpageURL)
		add(metadata.OriginalURL)
		add(metadata.URL)
	}
}

func metadataIsYouTubeTab(metadata videoMetadata) bool {
	for _, value := range []string{metadata.IEKey, metadata.Extractor, metadata.ExtractorKey} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "youtubetab" || value == "youtube:tab" {
			return true
		}
	}
	return false
}

func youtubeSearchFetchLimit(limit int, request SearchRequest) int {
	if !youtubeSearchSortsByUploadDate(request.SortBy) &&
		normalizedYTDLPDate(request.Date) == "" &&
		normalizedYTDLPDate(request.DateAfter) == "" &&
		normalizedYTDLPDate(request.DateBefore) == "" {
		return limit
	}
	if limit < 10 {
		return 10
	}
	return limit
}

func youtubeChannelUploadFetchLimit(limit int, request SearchRequest) int {
	if normalizedYTDLPDate(request.Date) == "" &&
		normalizedYTDLPDate(request.DateAfter) == "" &&
		normalizedYTDLPDate(request.DateBefore) == "" {
		return limit
	}
	if limit < 10 {
		return 10
	}
	return limit
}

func youtubeSearchSortsByUploadDate(sortBy string) bool {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "upload_date", "date", "latest", "newest", "recent", "recently_uploaded":
		return true
	default:
		return false
	}
}

func normalizedYTDLPDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == len("2006-01-02") && value[4] == '-' && value[7] == '-' {
		return strings.ReplaceAll(value, "-", "")
	}
	return value
}

func filterSearchCandidatesByDate(candidates []VideoCandidate, request SearchRequest) []VideoCandidate {
	exact := parseUploadDate(request.Date)
	after := parseUploadDate(request.DateAfter)
	before := parseUploadDate(request.DateBefore)
	if exact.IsZero() && after.IsZero() && before.IsZero() {
		return candidates
	}
	filtered := make([]VideoCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.UploadDate.IsZero() {
			continue
		}
		uploadDate := uploadDateDay(candidate.UploadDate)
		if !exact.IsZero() && !uploadDate.Equal(exact) {
			continue
		}
		if !after.IsZero() && uploadDate.Before(after) {
			continue
		}
		if !before.IsZero() && uploadDate.After(before) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func sortSearchCandidatesByUploadDate(candidates []VideoCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i].UploadDate
		right := candidates[j].UploadDate
		if left.IsZero() {
			return false
		}
		if right.IsZero() {
			return true
		}
		return left.After(right)
	})
}

func limitSearchCandidates(candidates []VideoCandidate, limit int) []VideoCandidate {
	if limit <= 0 || len(candidates) <= limit {
		return candidates
	}
	return candidates[:limit]
}

func uploadDateDay(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	year, month, day := value.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
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
		Uploader:     strings.TrimSpace(firstNonEmpty(metadata.Uploader, firstNonEmpty(metadata.Channel, firstNonEmpty(metadata.PlaylistUploader, metadata.PlaylistChannel)))),
		ThumbnailURL: bestThumbnailURL(metadata.Thumbnail, metadata.Thumbnails),
		Duration:     durationFromSeconds(metadata.Duration),
		UploadDate:   uploadDateFromMetadata(metadata),
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
		candidate.Duration == 0 ||
		candidate.UploadDate.IsZero()
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
	if base.UploadDate.IsZero() {
		base.UploadDate = extra.UploadDate
	}
	return base
}

func uploadDateFromMetadata(metadata videoMetadata) time.Time {
	if uploadDate := parseUploadDate(metadata.UploadDate); !uploadDate.IsZero() {
		return uploadDate
	}
	if metadata.Timestamp <= 0 {
		return time.Time{}
	}
	return time.Unix(metadata.Timestamp, 0).UTC()
}

func parseUploadDate(value string) time.Time {
	value = normalizedYTDLPDate(value)
	if len(value) != len("20060102") {
		return time.Time{}
	}
	parsed, err := time.Parse("20060102", value)
	if err != nil {
		return time.Time{}
	}
	return parsed
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
	audioPath, err := s.downloadAudioSource(processCtx, tools, source, dir)
	if err != nil {
		return nil, err
	}
	return s.segmentAudioSource(processCtx, tools, audioPath, dir)
}

func (s *Service) downloadAudioSource(ctx context.Context, tools ToolPaths, source string, dir string) (string, error) {
	outputTemplate := filepath.Join(dir, "audio.%(ext)s")
	ytdlpCmd := exec.CommandContext(ctx, tools.YTDLPPath,
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--no-cache-dir",
		"--format", "bestaudio[ext=m4a]/bestaudio/best",
		"--output", outputTemplate,
		source,
	)
	var ytdlpErr limitedBuffer
	ytdlpCmd.Stderr = &ytdlpErr
	if err := ytdlpCmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("youtube audio extraction failed: %w", ctx.Err())
		}
		return "", fmt.Errorf("youtube audio extraction failed: yt-dlp: %v %s", err, strings.TrimSpace(ytdlpErr.String()))
	}
	matches, err := filepath.Glob(filepath.Join(dir, "audio.*"))
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	for _, path := range matches {
		if strings.HasSuffix(path, ".part") || strings.HasSuffix(path, ".ytdl") {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("youtube audio extraction failed: yt-dlp produced no audio file")
}

func (s *Service) segmentAudioSource(ctx context.Context, tools ToolPaths, audioPath string, dir string) ([]string, error) {
	pattern := filepath.Join(dir, "chunk-%05d.wav")
	ffmpegCmd := exec.CommandContext(ctx, tools.FFmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-i", audioPath,
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
	ffmpegCmd.Stderr = &ffmpegErr
	if err := ffmpegCmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("youtube audio extraction failed: %w", ctx.Err())
		}
		return nil, fmt.Errorf("youtube audio extraction failed: ffmpeg: %v %s", err, strings.TrimSpace(ffmpegErr.String()))
	}

	chunks, err := filepath.Glob(filepath.Join(dir, "chunk-*.wav"))
	if err != nil {
		return nil, err
	}
	sort.Strings(chunks)
	return chunks, nil
}

func (s *Service) transcribeChunk(ctx context.Context, path string, language string) (string, error) {
	result, err := s.transcribeChunkDetailed(ctx, path, language)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func (s *Service) transcribeChunkDetailed(ctx context.Context, path string, language string) (chunkTranscription, error) {
	info, err := os.Stat(path)
	if err != nil {
		return chunkTranscription{}, err
	}
	if info.Size() <= 0 {
		return chunkTranscription{}, fmt.Errorf("audio chunk %s is empty", filepath.Base(path))
	}
	if info.Size() > lemonfoxUploadLimit {
		return chunkTranscription{}, fmt.Errorf("audio chunk %s exceeds Lemonfox upload limit", filepath.Base(path))
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := os.Open(path)
	if err != nil {
		return chunkTranscription{}, err
	}
	defer file.Close()
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return chunkTranscription{}, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return chunkTranscription{}, err
	}
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		return chunkTranscription{}, err
	}
	if value := strings.TrimSpace(language); value != "" {
		if err := writer.WriteField("language", value); err != nil {
			return chunkTranscription{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return chunkTranscription{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return chunkTranscription{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		return chunkTranscription{}, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, lemonfoxResponseMaxSize+1))
	if readErr != nil {
		return chunkTranscription{}, readErr
	}
	if len(data) > lemonfoxResponseMaxSize {
		return chunkTranscription{}, fmt.Errorf("lemonfox transcription response exceeded %d bytes", lemonfoxResponseMaxSize)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return chunkTranscription{}, fmt.Errorf("lemonfox transcription failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var decoded transcriptionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return chunkTranscription{}, fmt.Errorf("parse lemonfox transcription response: %w", err)
	}
	if text := strings.TrimSpace(decoded.Text); text != "" {
		return chunkTranscription{Text: text, Segments: decoded.Segments}, nil
	}
	segments := make([]string, 0, len(decoded.Segments))
	for _, segment := range decoded.Segments {
		if text := strings.TrimSpace(segment.Text); text != "" {
			segments = append(segments, text)
		}
	}
	return chunkTranscription{Text: strings.Join(segments, " "), Segments: decoded.Segments}, nil
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
