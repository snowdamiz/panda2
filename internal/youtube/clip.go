package youtube

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/objectstore"
)

const defaultViralClipInstructions = "Find the strongest viral short-form clips in this video. Prioritize hook-first moments with clear emotional spike, humor, surprise, debate, stakes, quotable soundbites, or satisfying setup/payoff. Return ranked continuous or spliced clips only when the transcript supports them; do not fill slots with generic summaries."

const (
	clipProgressSearching    = "Searching"
	clipProgressTranscribing = "Transcribing"
	clipProgressBuilding     = "Building clips"
	clipProgressPlanning     = "Planning layout"
	clipProgressRendering    = "Rendering"
	clipProgressUploading    = "Uploading"
)

func (s *Service) Clip(ctx context.Context, request ClipRequest) (ClipResult, error) {
	if !s.Configured() {
		return ClipResult{}, ErrNotConfigured
	}
	if !s.ClipConfigured() {
		return ClipResult{}, fmt.Errorf("youtube clipping is not configured")
	}
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return ClipResult{}, ErrMissingVideo
	}
	requestedAspect, err := normalizeClipAspectRatio(request.AspectRatio)
	if err != nil {
		return ClipResult{}, err
	}
	captionMode, err := normalizeClipCaptionMode(request.Captions)
	if err != nil {
		return ClipResult{}, err
	}
	availableCaptionFonts := []string(nil)
	if captionMode != clipCaptionRequestOff {
		availableCaptionFonts = s.availableCaptionFontKeys()
		if len(availableCaptionFonts) == 0 {
			return ClipResult{}, fmt.Errorf("youtube clip captions failed: no caption fonts are available; configure youtube_clip_caption_font_path/youtube_clip_caption_font_family")
		}
	}
	instructions := normalizedClipInstructions(request.Instructions)
	reportClipProgress(request, clipProgressSearching)
	tools, err := s.ensureTools(ctx)
	if err != nil {
		return ClipResult{}, err
	}
	metadata, err := s.resolve(ctx, tools, query)
	if err != nil {
		return ClipResult{}, err
	}
	source := strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, metadata.URL, query))
	if source == "" {
		return ClipResult{}, fmt.Errorf("youtube lookup failed: missing video url")
	}
	tempDir, err := os.MkdirTemp("", "panda-youtube-clip-*")
	if err != nil {
		return ClipResult{}, fmt.Errorf("create temporary clip dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	reportClipProgress(request, clipProgressTranscribing)
	chunks, err := s.extractAudioChunks(ctx, tools, source, tempDir)
	if err != nil {
		return ClipResult{}, err
	}
	segments, err := s.transcribeClipSegments(ctx, chunks, request.Language)
	if err != nil {
		return ClipResult{}, err
	}

	duration := durationFromSeconds(metadata.Duration)
	slog.Info("youtube clip detection started",
		slog.String("guild_id", request.GuildID),
		slog.String("request_id", request.RequestID),
		slog.String("video_url", source),
		slog.Int("transcript_segment_count", len(segments)),
	)
	reportClipProgress(request, clipProgressBuilding)
	detection, err := s.clipDetector.Detect(ctx, ClipDetectionRequest{
		Title:              strings.TrimSpace(metadata.Title),
		URL:                strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, source)),
		Uploader:           strings.TrimSpace(metadata.Uploader),
		Duration:           duration,
		Instructions:       instructions,
		MinDurationSeconds: s.clipMinDuration.Seconds(),
		MaxDurationSeconds: s.clipMaxDuration.Seconds(),
		MaxClips:           defaultClipDetectionMaxClips,
		Segments:           segments,
	})
	if err != nil {
		return ClipResult{}, err
	}
	if err := validateClipDetectionResult(detection, duration, s.clipMinDuration, s.clipMaxDuration, defaultClipDetectionMaxClips); err != nil {
		return ClipResult{}, err
	}
	slog.Info("youtube clip detection completed",
		slog.String("guild_id", request.GuildID),
		slog.String("request_id", request.RequestID),
		slog.Int("clip_count", len(detection.Clips)),
	)

	slog.Info("youtube clip source video download started",
		slog.String("guild_id", request.GuildID),
		slog.String("request_id", request.RequestID),
		slog.String("video_url", source),
	)
	reportClipProgress(request, clipProgressRendering)
	sourceVideoPath, err := s.downloadClipSourceVideo(ctx, tools, source, tempDir)
	if err != nil {
		return ClipResult{}, err
	}
	slog.Info("youtube clip source video download completed",
		slog.String("guild_id", request.GuildID),
		slog.String("request_id", request.RequestID),
		slog.String("file", filepath.Base(sourceVideoPath)),
	)

	rendered := make([]RenderedClip, 0, len(detection.Clips))
	candidateFailures := make([]string, 0)
	for index, decision := range detection.Clips {
		reportClipProgress(request, countedClipProgress(clipProgressPlanning, index+1, len(detection.Clips)))
		transcriptTimeline, err := clipCompositionTranscriptTimeline(decision, segments)
		if err != nil {
			if ctx.Err() != nil {
				return ClipResult{}, ctx.Err()
			}
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "timeline", err))
			continue
		}
		thumbnails, err := s.extractClipThumbnails(ctx, tools, sourceVideoPath, decision, transcriptTimeline, filepath.Join(tempDir, fmt.Sprintf("clip-%02d", index+1)))
		if err != nil {
			if ctx.Err() != nil {
				return ClipResult{}, ctx.Err()
			}
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "thumbnail extraction", err))
			continue
		}
		compositionRequest := ClipCompositionRequest{
			Title:                 strings.TrimSpace(metadata.Title),
			URL:                   strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, source)),
			Uploader:              strings.TrimSpace(metadata.Uploader),
			RequestedAspect:       requestedAspect,
			LayoutInstructions:    strings.TrimSpace(request.LayoutInstructions),
			CaptionMode:           captionMode,
			CaptionInstructions:   strings.TrimSpace(request.CaptionInstructions),
			AvailableCaptionFonts: availableCaptionFonts,
			Clip:                  decision,
			TranscriptTimeline:    transcriptTimeline,
			Thumbnails:            thumbnails,
		}
		composition, err := s.clipPlanner.Plan(ctx, compositionRequest)
		if err != nil {
			if ctx.Err() != nil {
				return ClipResult{}, ctx.Err()
			}
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "composition planning", err))
			continue
		}
		validationRequest := compositionRequest
		validationRequest.Thumbnails = clipThumbnailDimensionsOnly(compositionRequest.Thumbnails)
		if err := ValidateClipCompositionResult(composition, validationRequest); err != nil {
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "composition validation", fmt.Errorf("clip composition response failed validation: %w", err)))
			continue
		}
		reportClipProgress(request, countedClipProgress(clipProgressRendering, index+1, len(detection.Clips)))
		outputRank := len(rendered) + 1
		outputPath := filepath.Join(tempDir, fmt.Sprintf("clip-%02d.mp4", outputRank))
		slog.Info("youtube clip render started",
			slog.String("guild_id", request.GuildID),
			slog.String("request_id", request.RequestID),
			slog.Int("rank", outputRank),
			slog.Int("candidate_rank", index+1),
			slog.Int("segment_count", len(decision.Segments)),
			slog.String("aspect_ratio", composition.AspectRatio),
			slog.String("layout_mode", composition.LayoutMode),
		)
		if err := s.renderVideoClip(ctx, tools, sourceVideoPath, decision, composition, transcriptTimeline, tempDir, outputPath); err != nil {
			if ctx.Err() != nil {
				return ClipResult{}, ctx.Err()
			}
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "render", err))
			continue
		}
		info, err := os.Stat(outputPath)
		if err != nil {
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "render", fmt.Errorf("clip render failed: %w", err)))
			continue
		}
		if info.Size() <= 0 {
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "render", fmt.Errorf("clip render produced an empty file")))
			continue
		}
		if info.Size() > s.clipMaxBytes {
			candidateFailures = append(candidateFailures, logClipCandidateSkipped(request, index, decision, "render", fmt.Errorf("clip render exceeded maximum size of %d bytes", s.clipMaxBytes)))
			continue
		}
		slog.Info("youtube clip render completed",
			slog.String("guild_id", request.GuildID),
			slog.String("request_id", request.RequestID),
			slog.Int("rank", outputRank),
			slog.Int("candidate_rank", index+1),
			slog.Int64("size_bytes", info.Size()),
			slog.String("aspect_ratio", composition.AspectRatio),
			slog.String("layout_mode", composition.LayoutMode),
		)
		data, err := os.ReadFile(outputPath)
		if err != nil {
			return ClipResult{}, fmt.Errorf("read rendered clip: %w", err)
		}
		reportClipProgress(request, countedClipProgress(clipProgressUploading, index+1, len(detection.Clips)))
		upload, err := s.clipUploader.Upload(ctx, objectstore.UploadRequest{
			Key:         clipObjectKey(request, outputRank, decision.Title),
			ContentType: "video/mp4",
			Body:        data,
		})
		if err != nil {
			return ClipResult{}, err
		}
		slog.Info("youtube clip upload completed",
			slog.String("guild_id", request.GuildID),
			slog.String("request_id", request.RequestID),
			slog.Int("rank", outputRank),
			slog.Int("candidate_rank", index+1),
			slog.String("object_key", upload.Key),
			slog.Int64("size_bytes", upload.SizeBytes),
		)
		thumbnailUpload, err := s.uploadRenderedClipThumbnail(ctx, tools, outputPath, request, decision, outputRank, filepath.Join(tempDir, fmt.Sprintf("clip-%02d-thumbnail.jpg", outputRank)))
		if err != nil {
			return ClipResult{}, err
		}
		rendered = append(rendered, RenderedClip{
			Rank:                     decision.Rank,
			Title:                    strings.TrimSpace(decision.Title),
			Type:                     strings.TrimSpace(decision.Type),
			WatchURL:                 upload.URL,
			ObjectKey:                upload.Key,
			ThumbnailURL:             thumbnailUpload.URL,
			ThumbnailObjectKey:       thumbnailUpload.Key,
			Duration:                 clipSegmentsDuration(decision.Segments),
			SourceStartSeconds:       clipSourceStartSeconds(decision.Segments),
			SourceEndSeconds:         clipSourceEndSeconds(decision.Segments),
			Segments:                 renderedClipSegments(decision.Segments),
			Reason:                   strings.TrimSpace(decision.Reason),
			Confidence:               decision.Confidence,
			ViralityScore:            decision.ViralityScore,
			HookScore:                decision.HookScore,
			RetentionScore:           decision.RetentionScore,
			ShareabilityScore:        decision.ShareabilityScore,
			DurationPolicy:           strings.TrimSpace(decision.DurationPolicy),
			ExceptionReason:          strings.TrimSpace(decision.ExceptionReason),
			OutputSizeBytes:          upload.SizeBytes,
			AspectRatio:              strings.TrimSpace(composition.AspectRatio),
			LayoutMode:               strings.TrimSpace(composition.LayoutMode),
			CompositionReason:        strings.TrimSpace(composition.Reason),
			CompositionConfidence:    composition.Confidence,
			CaptionRendered:          composition.CaptionPlan != nil && strings.TrimSpace(composition.CaptionPlan.Mode) == clipCaptionPlanModeBurnedIn,
			CaptionMode:              captionPlanMode(composition.CaptionPlan),
			CaptionStylePreset:       captionPlanStylePreset(composition.CaptionPlan),
			CaptionStyleSource:       captionPlanStyleSource(composition.CaptionPlan),
			CaptionAnimation:         captionPlanAnimation(composition.CaptionPlan),
			CaptionTimingQuality:     captionPlanTimingQuality(composition.CaptionPlan),
			CaptionConfidence:        captionPlanConfidence(composition.CaptionPlan),
			CaptionReason:            captionPlanReason(composition.CaptionPlan),
			CaptionFontFamily:        captionPlanFontFamily(composition.CaptionPlan),
			CaptionFontColor:         captionPlanFontColor(composition.CaptionPlan),
			CaptionHighlightColor:    captionPlanHighlightColor(composition.CaptionPlan),
			CaptionBorderColor:       captionPlanBorderColor(composition.CaptionPlan),
			CaptionBorderThickness:   captionPlanBorderThickness(composition.CaptionPlan),
			CaptionBackgroundColor:   captionPlanBackgroundColor(composition.CaptionPlan),
			CaptionBackgroundOpacity: captionPlanBackgroundOpacity(composition.CaptionPlan),
		})
	}
	if len(rendered) == 0 {
		detail := "no candidate failure details were recorded"
		if len(candidateFailures) > 0 {
			detail = strings.Join(candidateFailures, "; ")
		}
		return ClipResult{}, fmt.Errorf("youtube clipping produced no renderable clips after trying %d detected candidate(s): %s", len(detection.Clips), detail)
	}
	return ClipResult{
		Title:                  strings.TrimSpace(metadata.Title),
		URL:                    strings.TrimSpace(firstNonEmpty(metadata.WebpageURL, metadata.OriginalURL, source)),
		Uploader:               strings.TrimSpace(metadata.Uploader),
		Duration:               duration,
		TranscriptSegmentCount: len(segments),
		Clips:                  rendered,
	}, nil
}

func logClipCandidateSkipped(request ClipRequest, index int, decision ClipDecision, stage string, err error) string {
	title := strings.TrimSpace(decision.Title)
	if title == "" {
		title = "untitled"
	}
	detail := fmt.Sprintf("candidate %d %q %s failed: %v", index+1, title, stage, err)
	slog.Warn("youtube clip candidate skipped",
		slog.String("guild_id", request.GuildID),
		slog.String("request_id", request.RequestID),
		slog.Int("candidate_rank", index+1),
		slog.String("title", title),
		slog.String("stage", stage),
		slog.String("error", err.Error()),
	)
	return detail
}

func clipCompositionTranscriptTimeline(decision ClipDecision, transcript []TranscriptSegment) ([]ClipCompositionTranscriptSegment, error) {
	timeline := make([]ClipCompositionTranscriptSegment, 0)
	refs, refsByID, err := transcriptWordReferences(transcript)
	if err != nil {
		return nil, err
	}
	for segmentIndex, clipSegment := range decision.Segments {
		startWordID := strings.TrimSpace(clipSegment.StartWordID)
		endWordID := strings.TrimSpace(clipSegment.EndWordID)
		if startWordID == "" || endWordID == "" {
			return nil, fmt.Errorf("clip composition transcript timeline requires word-backed clip segments")
		}
		startRef, startOK := refsByID[startWordID]
		endRef, endOK := refsByID[endWordID]
		if !startOK || !endOK {
			return nil, fmt.Errorf("clip composition transcript timeline references missing word IDs")
		}
		if endRef.Index < startRef.Index {
			return nil, fmt.Errorf("clip composition transcript timeline end word precedes start word")
		}
		for sourceSegmentIndex := startRef.SegmentIndex; sourceSegmentIndex <= endRef.SegmentIndex; sourceSegmentIndex++ {
			segmentWords := make([]transcriptWordRef, 0)
			for _, ref := range refs[startRef.Index : endRef.Index+1] {
				if ref.SegmentIndex != sourceSegmentIndex {
					continue
				}
				segmentWords = append(segmentWords, ref)
			}
			if len(segmentWords) == 0 {
				continue
			}
			text := joinedTranscriptWords(segmentWords)
			if text == "" {
				continue
			}
			sourceSegment := transcript[sourceSegmentIndex]
			words := make([]TranscriptWord, 0, len(segmentWords))
			for _, word := range segmentWords {
				words = append(words, word.Word)
			}
			timeline = append(timeline, ClipCompositionTranscriptSegment{
				ClipSegmentIndex: segmentIndex,
				ID:               transcriptSegmentID(sourceSegment, sourceSegmentIndex),
				StartSeconds:     segmentWords[0].Word.StartSeconds,
				EndSeconds:       segmentWords[len(segmentWords)-1].Word.EndSeconds,
				Text:             text,
				Words:            words,
			})
		}
	}
	return timeline, nil
}

func reportClipProgress(request ClipRequest, status string) {
	status = strings.TrimSpace(status)
	if request.Progress == nil || status == "" {
		return
	}
	request.Progress(ClipProgress{Status: status})
}

func countedClipProgress(status string, current int, total int) string {
	status = strings.TrimSpace(status)
	if total <= 1 || current <= 0 {
		return status
	}
	return fmt.Sprintf("%s %d/%d", status, current, total)
}

func normalizedClipInstructions(instructions string) string {
	instructions = strings.TrimSpace(instructions)
	if instructions != "" {
		return instructions
	}
	return defaultViralClipInstructions
}

func (s *Service) transcribeClipSegments(ctx context.Context, chunks []string, language string) ([]TranscriptSegment, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("youtube audio extraction produced no chunks")
	}
	segments := make([]TranscriptSegment, 0, len(chunks)*16)
	wordIndex := 1
	for index, chunk := range chunks {
		transcription, err := s.transcribeChunkDetailed(ctx, chunk, language)
		if err != nil {
			return nil, err
		}
		chunkOffset := float64(index) * s.chunkDuration.Seconds()
		for _, segment := range transcription.Segments {
			text := cleanTranscriptText(segment.Text)
			if text == "" {
				continue
			}
			if segment.End <= segment.Start {
				continue
			}
			words := transcriptionWordsForSegment(segment, transcription.Words)
			transcriptWords := make([]TranscriptWord, 0, len(words))
			for _, word := range words {
				wordText := cleanTranscriptText(word.captionText())
				if wordText == "" || word.End <= word.Start {
					continue
				}
				transcriptWords = append(transcriptWords, TranscriptWord{
					ID:           fmt.Sprintf("w_%04d", wordIndex),
					StartSeconds: chunkOffset + word.Start,
					EndSeconds:   chunkOffset + word.End,
					Text:         wordText,
				})
				wordIndex++
			}
			segments = append(segments, TranscriptSegment{
				StartSeconds: chunkOffset + segment.Start,
				EndSeconds:   chunkOffset + segment.End,
				Text:         text,
				ID:           fmt.Sprintf("s_%04d", len(segments)+1),
				Words:        transcriptWords,
			})
		}
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("lemonfox returned no timestamped transcript segments")
	}
	return segments, nil
}

func transcriptionWordsForSegment(segment transcriptionSegment, topLevelWords []transcriptionWord) []transcriptionWord {
	if len(segment.Words) > 0 {
		return segment.Words
	}
	if len(segment.WholeWordTimestamps) > 0 {
		return segment.WholeWordTimestamps
	}
	if len(topLevelWords) == 0 {
		return nil
	}
	words := make([]transcriptionWord, 0)
	for _, word := range topLevelWords {
		if word.End <= word.Start {
			continue
		}
		if word.End < segment.Start-0.05 || word.Start > segment.End+0.05 {
			continue
		}
		words = append(words, word)
	}
	return words
}

func (word transcriptionWord) captionText() string {
	if text := strings.TrimSpace(word.PunctuatedWord); text != "" {
		return text
	}
	if text := strings.TrimSpace(word.Word); text != "" {
		return text
	}
	return strings.TrimSpace(word.Text)
}

func validateClipDetectionResult(result ClipDetectionResult, videoDuration time.Duration, minDuration time.Duration, maxDuration time.Duration, maxClips int) error {
	if len(result.Clips) == 0 {
		return fmt.Errorf("clip detection response contained no clips")
	}
	if maxClips > 0 && len(result.Clips) > maxClips {
		return fmt.Errorf("clip detection response returned %d clips, maximum is %d", len(result.Clips), maxClips)
	}
	seenRanks := map[int]struct{}{}
	for index, decision := range result.Clips {
		if err := validateClipDecision(decision, videoDuration, minDuration, maxDuration); err != nil {
			return fmt.Errorf("clip %d: %w", index+1, err)
		}
		if decision.Rank != index+1 {
			return fmt.Errorf("clip %d rank must be %d", index+1, index+1)
		}
		if _, exists := seenRanks[decision.Rank]; exists {
			return fmt.Errorf("clip rank %d is duplicated", decision.Rank)
		}
		seenRanks[decision.Rank] = struct{}{}
	}
	return nil
}

func validateClipDecision(decision ClipDecision, videoDuration time.Duration, minDuration time.Duration, maxDuration time.Duration) error {
	if decision.Rank <= 0 {
		return fmt.Errorf("clip detection response missing rank")
	}
	if strings.TrimSpace(decision.Title) == "" {
		return fmt.Errorf("clip detection response missing title")
	}
	if strings.TrimSpace(decision.Reason) == "" {
		return fmt.Errorf("clip detection response missing reason")
	}
	clipType := strings.TrimSpace(decision.Type)
	switch clipType {
	case "continuous":
		if len(decision.Segments) != 1 {
			return fmt.Errorf("continuous clip must contain exactly one segment")
		}
	case "spliced":
		if len(decision.Segments) < 2 {
			return fmt.Errorf("spliced clip must contain at least two segments")
		}
	default:
		return fmt.Errorf("clip type is invalid")
	}
	if len(decision.Segments) == 0 {
		return fmt.Errorf("clip detection response missing segments")
	}
	if len(decision.Segments) > clipDetectionMaxSegments {
		return fmt.Errorf("clip has %d segments, maximum is %d", len(decision.Segments), clipDetectionMaxSegments)
	}
	totalSeconds := 0.0
	previousEnd := -1.0
	for index, segment := range decision.Segments {
		if err := validateClipDecisionSegment(segment, videoDuration); err != nil {
			return fmt.Errorf("segment %d: %w", index+1, err)
		}
		if previousEnd >= 0 && segment.StartSeconds < previousEnd {
			return fmt.Errorf("clip segments must be ordered by source time and non-overlapping")
		}
		previousEnd = segment.EndSeconds
		totalSeconds += segment.EndSeconds - segment.StartSeconds
	}
	if decision.Confidence < 0 || decision.Confidence > 1 || math.IsNaN(decision.Confidence) || math.IsInf(decision.Confidence, 0) {
		return fmt.Errorf("clip confidence must be between 0 and 1")
	}
	if err := validateScore("virality_score", decision.ViralityScore); err != nil {
		return err
	}
	if err := validateScore("hook_score", decision.HookScore); err != nil {
		return err
	}
	if err := validateScore("retention_score", decision.RetentionScore); err != nil {
		return err
	}
	if err := validateScore("shareability_score", decision.ShareabilityScore); err != nil {
		return err
	}
	switch strings.TrimSpace(decision.DurationPolicy) {
	case "target_30_45", "short_exception", "long_context_exception", "requested_duration", "other":
	default:
		return fmt.Errorf("clip duration_policy is invalid")
	}
	clipDuration := time.Duration(totalSeconds * float64(time.Second))
	if minDuration > 0 && clipDuration < minDuration {
		return fmt.Errorf("clip duration %.1fs is shorter than minimum %.1fs", clipDuration.Seconds(), minDuration.Seconds())
	}
	if maxDuration > 0 && clipDuration > maxDuration {
		return fmt.Errorf("clip duration %.1fs exceeds maximum %.1fs", clipDuration.Seconds(), maxDuration.Seconds())
	}
	return nil
}

func validateClipDecisionSegment(segment ClipDecisionSegment, videoDuration time.Duration) error {
	if strings.TrimSpace(segment.Transcript) == "" {
		return fmt.Errorf("clip segment missing transcript")
	}
	if invalidSeconds(segment.StartSeconds) || invalidSeconds(segment.EndSeconds) {
		return fmt.Errorf("clip segment contains invalid timestamps")
	}
	if segment.StartSeconds < 0 {
		return fmt.Errorf("clip segment start must not be negative")
	}
	if segment.EndSeconds <= segment.StartSeconds {
		return fmt.Errorf("clip segment end must be after clip segment start")
	}
	if videoDuration > 0 && time.Duration(segment.EndSeconds*float64(time.Second)) > videoDuration+time.Second {
		return fmt.Errorf("clip segment end %.1fs exceeds video duration %.1fs", segment.EndSeconds, videoDuration.Seconds())
	}
	return nil
}

func validateScore(name string, score int) error {
	if score < 0 || score > 100 {
		return fmt.Errorf("clip %s must be between 0 and 100", name)
	}
	return nil
}

func invalidSeconds(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0)
}

func (s *Service) downloadClipSourceVideo(ctx context.Context, tools ToolPaths, source string, tempDir string) (string, error) {
	processCtx, cancel := context.WithTimeout(ctx, s.processTimeout)
	defer cancel()
	outputTemplate := filepath.Join(tempDir, "source.%(ext)s")
	args := youtubeYTDLPDownloadArgs(
		"--format", "best[ext=mp4]/best",
		"--merge-output-format", "mp4",
		"--output", outputTemplate,
		source,
	)
	cmd := exec.CommandContext(processCtx, tools.YTDLPPath, args...)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if processCtx.Err() != nil {
			return "", fmt.Errorf("youtube clip source download failed: %w", processCtx.Err())
		}
		return "", fmt.Errorf("youtube clip source download failed: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	matches, err := filepath.Glob(filepath.Join(tempDir, "source.*"))
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
	return "", fmt.Errorf("youtube clip source download produced no video file")
}

func (s *Service) renderVideoClip(ctx context.Context, tools ToolPaths, sourcePath string, decision ClipDecision, composition ClipCompositionResult, transcriptTimeline []ClipCompositionTranscriptSegment, tempDir string, outputPath string) error {
	if len(decision.Segments) == 0 {
		return fmt.Errorf("youtube clip render failed: no segments to render")
	}
	plans := orderedClipRenderPlans(composition, len(decision.Segments))
	if len(plans) == 0 {
		return fmt.Errorf("youtube clip render failed: composition returned no plans")
	}
	target := s.clipResolutionForAspect(composition.AspectRatio)
	captionRefs := newClipCaptionReferences(transcriptTimeline)
	var captionStyle *clipCaptionASSStyle
	if composition.CaptionPlan != nil && strings.TrimSpace(composition.CaptionPlan.Mode) == clipCaptionPlanModeBurnedIn {
		style, err := s.buildClipCaptionASSStyle(*composition.CaptionPlan, target)
		if err != nil {
			return err
		}
		captionStyle = &style
		if err := validateCaptionRenderer(ctx, tools); err != nil {
			return err
		}
	}
	if len(plans) == 1 {
		return s.renderVideoPlanSegment(ctx, tools, sourcePath, composition.LayoutMode, plans[0], target, composition.CaptionPlan, captionRefs, captionStyle, outputPath)
	}
	segmentDir := filepath.Join(tempDir, strings.TrimSuffix(filepath.Base(outputPath), filepath.Ext(outputPath))+"-segments")
	if err := os.MkdirAll(segmentDir, 0o700); err != nil {
		return fmt.Errorf("create spliced clip segment dir: %w", err)
	}
	paths := make([]string, 0, len(plans))
	for index, plan := range plans {
		segmentPath := filepath.Join(segmentDir, fmt.Sprintf("segment-%02d.mp4", index+1))
		if err := s.renderVideoPlanSegment(ctx, tools, sourcePath, composition.LayoutMode, plan, target, composition.CaptionPlan, captionRefs, captionStyle, segmentPath); err != nil {
			return err
		}
		paths = append(paths, segmentPath)
	}
	return s.concatVideoSegments(ctx, tools, paths, filepath.Join(segmentDir, "concat.txt"), outputPath)
}

func orderedClipRenderPlans(composition ClipCompositionResult, segmentCount int) []ClipFrameRenderPlan {
	plansBySegment := make(map[int][]ClipFrameRenderPlan, segmentCount)
	for _, plan := range composition.Plans {
		plansBySegment[plan.AppliesToSegmentIndex] = append(plansBySegment[plan.AppliesToSegmentIndex], plan)
	}
	ordered := make([]ClipFrameRenderPlan, 0, len(composition.Plans))
	for segmentIndex := 0; segmentIndex < segmentCount; segmentIndex++ {
		plans := plansBySegment[segmentIndex]
		sort.Slice(plans, func(i, j int) bool {
			return plans[i].SourceStartSeconds < plans[j].SourceStartSeconds
		})
		ordered = append(ordered, plans...)
	}
	return ordered
}

func clipThumbnailDimensionsOnly(thumbnails []ClipThumbnail) []ClipThumbnail {
	if len(thumbnails) == 0 {
		return nil
	}
	copied := make([]ClipThumbnail, len(thumbnails))
	for index, thumbnail := range thumbnails {
		copied[index] = ClipThumbnail{
			Width:  thumbnail.Width,
			Height: thumbnail.Height,
		}
	}
	return copied
}

func (s *Service) clipResolutionForAspect(aspect string) ClipResolution {
	switch strings.TrimSpace(aspect) {
	case clipAspectVertical:
		return s.verticalResolution
	default:
		return s.landscapeResolution
	}
}

func (s *Service) renderVideoPlanSegment(ctx context.Context, tools ToolPaths, sourcePath string, layout string, plan ClipFrameRenderPlan, target ClipResolution, captionPlan *ClipCaptionPlan, captionRefs clipCaptionReferences, captionStyle *clipCaptionASSStyle, outputPath string) error {
	processCtx, cancel := context.WithTimeout(ctx, s.processTimeout)
	defer cancel()
	durationSeconds := plan.SourceEndSeconds - plan.SourceStartSeconds
	if durationSeconds <= 0 {
		return fmt.Errorf("youtube clip render failed: invalid plan duration")
	}
	filterGraph, err := buildClipRenderFilterGraph(layout, plan.Regions, target, durationSeconds)
	if err != nil {
		return err
	}
	videoOutputLabel := "vout"
	if captionPlan != nil && strings.TrimSpace(captionPlan.Mode) == clipCaptionPlanModeBurnedIn {
		if captionStyle == nil {
			return fmt.Errorf("youtube clip captions failed: caption style was not resolved")
		}
		ass, err := buildClipASSSubtitle(*captionPlan, plan, target, captionRefs, *captionStyle)
		if err != nil {
			return err
		}
		assPath := outputPath + ".ass"
		if err := os.WriteFile(assPath, []byte(ass), 0o600); err != nil {
			return fmt.Errorf("write youtube clip captions: %w", err)
		}
		filterGraph = appendClipCaptionFilterGraph(filterGraph, assPath, captionStyle.Font.Path)
		videoOutputLabel = "vcaption"
	}
	ffmpegCmd := exec.CommandContext(processCtx, tools.FFmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-ss", formatClipSeconds(plan.SourceStartSeconds),
		"-i", sourcePath,
		"-t", formatClipSeconds(durationSeconds),
		"-filter_complex", filterGraph,
		"-map", "["+videoOutputLabel+"]",
		"-map", "0:a:0?",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		"-f", "mp4",
		outputPath,
	)
	var ffmpegErr limitedBuffer
	ffmpegCmd.Stderr = &ffmpegErr

	if err := ffmpegCmd.Run(); err != nil {
		if processCtx.Err() != nil {
			return fmt.Errorf("youtube clip render failed: %w", processCtx.Err())
		}
		return fmt.Errorf("youtube clip render failed: ffmpeg: %v %s", err, strings.TrimSpace(ffmpegErr.String()))
	}
	return nil
}

func buildClipRenderFilterGraph(layout string, regions []ClipRenderRegion, target ClipResolution, durationSeconds float64) (string, error) {
	if target.Width <= 0 || target.Height <= 0 {
		return "", fmt.Errorf("youtube clip render failed: invalid target resolution")
	}
	if len(regions) == 0 {
		return "", fmt.Errorf("youtube clip render failed: no render regions")
	}
	switch layout {
	case clipLayoutFullFrame:
		return fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,format=yuv420p[vout]", target.Width, target.Height, target.Width, target.Height), nil
	case clipLayoutSingleCrop:
		if len(regions) != 1 {
			return buildCompositeClipRenderFilterGraph(regions, target, durationSeconds), nil
		}
		return fmt.Sprintf("[0:v]%s,setsar=1,format=yuv420p[vout]", clipRegionVideoFilter(regions[0], target)), nil
	case clipLayoutStacked, clipLayoutFaceOverlay:
		return buildCompositeClipRenderFilterGraph(regions, target, durationSeconds), nil
	default:
		return "", fmt.Errorf("youtube clip render failed: unsupported layout mode %q", layout)
	}
}

func buildCompositeClipRenderFilterGraph(regions []ClipRenderRegion, target ClipResolution, durationSeconds float64) string {
	sorted := append([]ClipRenderRegion(nil), regions...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].ZIndex == sorted[j].ZIndex {
			return sorted[i].OutputRect.Y < sorted[j].OutputRect.Y
		}
		return sorted[i].ZIndex < sorted[j].ZIndex
	})
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("color=c=black:s=%dx%d:d=%s[base];", target.Width, target.Height, formatClipSeconds(durationSeconds)))
	builder.WriteString("[0:v]")
	builder.WriteString(fmt.Sprintf("split=%d", len(sorted)))
	for index := range sorted {
		builder.WriteString(fmt.Sprintf("[v%d]", index))
	}
	builder.WriteString(";")
	for index, region := range sorted {
		builder.WriteString(fmt.Sprintf("[v%d]%s[r%d];", index, clipRegionVideoFilter(region, target), index))
	}
	base := "base"
	for index, region := range sorted {
		rect := outputPixelRect(region.OutputRect, target)
		out := "composed"
		if index < len(sorted)-1 {
			out = fmt.Sprintf("tmp%d", index)
		}
		builder.WriteString(fmt.Sprintf("[%s][r%d]overlay=x=%d:y=%d:shortest=1[%s]", base, index, rect.X, rect.Y, out))
		if index < len(sorted)-1 {
			builder.WriteString(";")
			base = out
		}
	}
	builder.WriteString(";[composed]setsar=1,format=yuv420p[vout]")
	return builder.String()
}

func clipRegionVideoFilter(region ClipRenderRegion, target ClipResolution) string {
	rect := outputPixelRect(region.OutputRect, target)
	filter := clipSourceCropFilter(region.SourceRect)
	switch region.Fit {
	case "contain":
		filter += fmt.Sprintf(",scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2", rect.W, rect.H, rect.W, rect.H)
	default:
		filter += fmt.Sprintf(",scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", rect.W, rect.H, rect.W, rect.H)
	}
	return filter
}

func clipSourceCropFilter(rect ClipRect) string {
	return fmt.Sprintf(
		"crop=w=floor(iw*%d/1000/2)*2:h=floor(ih*%d/1000/2)*2:x=floor(iw*%d/1000/2)*2:y=floor(ih*%d/1000/2)*2",
		rect.W,
		rect.H,
		rect.X,
		rect.Y,
	)
}

type clipPixelRect struct {
	X int
	Y int
	W int
	H int
}

func outputPixelRect(rect ClipRect, target ClipResolution) clipPixelRect {
	x1 := int(math.Round(float64(target.Width) * float64(rect.X) / 1000))
	y1 := int(math.Round(float64(target.Height) * float64(rect.Y) / 1000))
	x2 := int(math.Round(float64(target.Width) * float64(rect.X+rect.W) / 1000))
	y2 := int(math.Round(float64(target.Height) * float64(rect.Y+rect.H) / 1000))
	if rect.X+rect.W == 1000 {
		x2 = target.Width
	}
	if rect.Y+rect.H == 1000 {
		y2 = target.Height
	}
	return clipPixelRect{
		X: x1,
		Y: y1,
		W: x2 - x1,
		H: y2 - y1,
	}
}

func (s *Service) concatVideoSegments(ctx context.Context, tools ToolPaths, segmentPaths []string, listPath string, outputPath string) error {
	if len(segmentPaths) < 2 {
		return fmt.Errorf("youtube clip concat failed: at least two segments are required")
	}
	var builder strings.Builder
	for _, path := range segmentPaths {
		builder.WriteString("file '")
		builder.WriteString(escapeFFmpegConcatPath(path))
		builder.WriteString("'\n")
	}
	if err := os.WriteFile(listPath, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("write spliced clip concat list: %w", err)
	}
	processCtx, cancel := context.WithTimeout(ctx, s.processTimeout)
	defer cancel()
	ffmpegCmd := exec.CommandContext(processCtx, tools.FFmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		"-f", "mp4",
		outputPath,
	)
	var ffmpegErr limitedBuffer
	ffmpegCmd.Stderr = &ffmpegErr
	if err := ffmpegCmd.Run(); err != nil {
		if processCtx.Err() != nil {
			return fmt.Errorf("youtube clip concat failed: %w", processCtx.Err())
		}
		return fmt.Errorf("youtube clip concat failed: %v %s", err, strings.TrimSpace(ffmpegErr.String()))
	}
	return nil
}

func escapeFFmpegConcatPath(path string) string {
	return strings.ReplaceAll(path, "'", "'\\''")
}

func (s *Service) uploadRenderedClipThumbnail(ctx context.Context, tools ToolPaths, renderedPath string, request ClipRequest, decision ClipDecision, rank int, thumbnailPath string) (objectstore.UploadResult, error) {
	if err := s.extractClipThumbnail(ctx, tools, renderedPath, renderedClipThumbnailOffset(decision), thumbnailPath); err != nil {
		return objectstore.UploadResult{}, err
	}
	data, err := os.ReadFile(thumbnailPath)
	if err != nil {
		return objectstore.UploadResult{}, fmt.Errorf("read rendered clip thumbnail: %w", err)
	}
	upload, err := s.clipUploader.Upload(ctx, objectstore.UploadRequest{
		Key:         clipThumbnailObjectKey(request, rank, decision.Title),
		ContentType: "image/jpeg",
		Body:        data,
	})
	if err != nil {
		return objectstore.UploadResult{}, err
	}
	slog.Info("youtube clip thumbnail upload completed",
		slog.String("guild_id", request.GuildID),
		slog.String("request_id", request.RequestID),
		slog.Int("rank", rank),
		slog.String("object_key", upload.Key),
		slog.Int64("size_bytes", upload.SizeBytes),
	)
	return upload, nil
}

func renderedClipThumbnailOffset(decision ClipDecision) float64 {
	duration := clipSegmentsDuration(decision.Segments).Seconds()
	if duration <= 1 {
		return 0.05
	}
	if duration < 2 {
		return duration / 2
	}
	return 1
}

func clipSegmentsDuration(segments []ClipDecisionSegment) time.Duration {
	total := 0.0
	for _, segment := range segments {
		total += segment.EndSeconds - segment.StartSeconds
	}
	return time.Duration(total * float64(time.Second))
}

func renderedClipSegments(segments []ClipDecisionSegment) []RenderedClipSegment {
	rendered := make([]RenderedClipSegment, 0, len(segments))
	for _, segment := range segments {
		rendered = append(rendered, RenderedClipSegment{
			StartSeconds: segment.StartSeconds,
			EndSeconds:   segment.EndSeconds,
			Duration:     time.Duration((segment.EndSeconds - segment.StartSeconds) * float64(time.Second)),
			Transcript:   strings.TrimSpace(segment.Transcript),
		})
	}
	return rendered
}

func clipSourceStartSeconds(segments []ClipDecisionSegment) float64 {
	if len(segments) == 0 {
		return 0
	}
	return segments[0].StartSeconds
}

func clipSourceEndSeconds(segments []ClipDecisionSegment) float64 {
	if len(segments) == 0 {
		return 0
	}
	return segments[len(segments)-1].EndSeconds
}

func formatClipSeconds(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func clipObjectKey(request ClipRequest, rank int, title string) string {
	return safeObjectPathSegment(firstNonEmpty(request.GuildID, "unknown-guild")) + "/" +
		safeObjectPathSegment(firstNonEmpty(request.RequestID, "unknown-request")) + "/" +
		fmt.Sprintf("%02d-%s", rank, safeClipFilename(title))
}

func clipThumbnailObjectKey(request ClipRequest, rank int, title string) string {
	key := clipObjectKey(request, rank, title)
	extension := filepath.Ext(key)
	if extension == "" {
		return key + ".jpg"
	}
	return strings.TrimSuffix(key, extension) + ".jpg"
}

func safeClipFilename(title string) string {
	slug := safeObjectPathSegment(title)
	if slug == "" {
		slug = "youtube-clip"
	}
	return slug + ".mp4"
}

func safeObjectPathSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastHyphen := false
	for _, char := range value {
		keep := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if keep {
			builder.WriteRune(char)
			lastHyphen = false
			continue
		}
		if !lastHyphen && builder.Len() > 0 {
			builder.WriteByte('-')
			lastHyphen = true
		}
		if builder.Len() >= 80 {
			break
		}
	}
	return strings.Trim(builder.String(), "-")
}
