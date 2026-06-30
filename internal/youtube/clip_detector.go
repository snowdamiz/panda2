package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/sn0w/panda2/internal/llm"
)

const (
	defaultClipDetectionMaxTokens  = 8192
	minClipDetectionRepairTokens   = 8192
	defaultClipDetectionMinClips   = 2
	defaultClipDetectionMaxClips   = 12
	defaultClipDetectionTimeout    = 2 * time.Minute
	clipDetectionMaxSegments       = 6
	clipDetectionInputMaxBytes     = 260000
	clipDetectionInputTargetBytes  = clipDetectionInputMaxBytes * 4 / 5
	clipDetectionRepairMaxBytes    = 12000
	maxClipDetectionRepairAttempts = 3
	clipDetectionMaxWordsPerUnit   = 18
	clipDetectionUnitPauseSeconds  = 0.45
	clipBoundaryLeadPadSeconds     = 0.18
	clipBoundaryTailPadSeconds     = 0.28
	clipBoundaryMinGapSeconds      = 0.02
	clipBoundarySentenceGap        = 0.45
)

type OpenRouterClipDetector struct {
	client    llm.Client
	model     string
	maxTokens int
	maxClips  int
	timeout   time.Duration
}

type ClipDetectorConfig struct {
	Client    llm.Client
	Model     string
	MaxTokens int
	MaxClips  int
	Timeout   time.Duration
}

func NewOpenRouterClipDetector(config ClipDetectorConfig) *OpenRouterClipDetector {
	maxTokens := config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultClipDetectionMaxTokens
	}
	maxClips := config.MaxClips
	if maxClips <= 0 || maxClips > defaultClipDetectionMaxClips {
		maxClips = defaultClipDetectionMaxClips
	}
	if maxClips < defaultClipDetectionMinClips {
		maxClips = defaultClipDetectionMinClips
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultClipDetectionTimeout
	}
	return &OpenRouterClipDetector{
		client:    config.Client,
		model:     strings.TrimSpace(config.Model),
		maxTokens: maxTokens,
		maxClips:  maxClips,
		timeout:   timeout,
	}
}

func (d *OpenRouterClipDetector) Configured() bool {
	return d != nil && d.client != nil && strings.TrimSpace(d.model) != ""
}

func (d *OpenRouterClipDetector) Detect(ctx context.Context, request ClipDetectionRequest) (ClipDetectionResult, error) {
	if !d.Configured() {
		return ClipDetectionResult{}, fmt.Errorf("youtube clip detection model is not configured")
	}
	if len(request.Segments) == 0 {
		return ClipDetectionResult{}, fmt.Errorf("timestamped transcript segments are required")
	}
	maxClips := request.MaxClips
	if maxClips <= 0 || maxClips > d.maxClips {
		maxClips = d.maxClips
	}
	if maxClips < defaultClipDetectionMinClips {
		maxClips = defaultClipDetectionMinClips
	}
	data, err := clipDetectionRequestData(request, request.Segments, nil, maxClips)
	if err != nil {
		return ClipDetectionResult{}, err
	}
	if len(data) <= clipDetectionInputMaxBytes {
		return d.detectWithData(ctx, request, data, maxClips)
	}
	return d.detectChunked(ctx, request, maxClips)
}

func clipDetectionRequestData(request ClipDetectionRequest, segments []TranscriptSegment, boundarySegments []TranscriptSegment, maxClips int) ([]byte, error) {
	input, err := clipDetectionRequestInput(request, segments, boundarySegments, maxClips)
	if err != nil {
		return nil, err
	}
	return json.Marshal(input)
}

func clipDetectionRequestInput(request ClipDetectionRequest, segments []TranscriptSegment, boundarySegments []TranscriptSegment, maxClips int) (clipDetectionInput, error) {
	transcriptSegments, speechUnits, boundaryOptions, err := clipDetectionTranscriptContextWithBoundarySegments(segments, boundarySegments)
	if err != nil {
		return clipDetectionInput{}, err
	}
	input := clipDetectionInput{
		Video: clipDetectionVideo{
			Title:           strings.TrimSpace(request.Title),
			URL:             strings.TrimSpace(request.URL),
			Uploader:        strings.TrimSpace(request.Uploader),
			DurationSeconds: request.Duration.Seconds(),
		},
		Instructions: normalizedClipInstructions(request.Instructions),
		Constraints: clipDetectionConstraints{
			MinDurationSeconds:        request.MinDurationSeconds,
			MaxDurationSeconds:        request.MaxDurationSeconds,
			TargetMinSeconds:          30,
			TargetMaxSeconds:          45,
			RenderLeadPadSeconds:      clipBoundaryLeadPadSeconds,
			RenderTailPadSeconds:      clipBoundaryTailPadSeconds,
			MinSpliceSourceGapSeconds: clipRequiredSpliceSourceGapSeconds(),
			MinClips:                  defaultClipDetectionMinClips,
			MaxClips:                  maxClips,
		},
		TranscriptSegments: transcriptSegments,
		SpeechUnits:        speechUnits,
		BoundaryOptions:    boundaryOptions,
	}
	return input, nil
}

func (d *OpenRouterClipDetector) detectWithData(ctx context.Context, request ClipDetectionRequest, data []byte, maxClips int) (ClipDetectionResult, error) {
	response, err := d.chat(ctx, d.clipDetectionChatRequest(data, clipDetectionResponseFormat(), "", d.maxTokens))
	if err != nil {
		return ClipDetectionResult{}, err
	}
	result, parseErr := parseAndValidateClipDetection(response.Content, request, maxClips)
	if parseErr == nil {
		return result, nil
	}

	lastResponse := response
	lastErr := parseErr
	for attempt := 1; attempt <= maxClipDetectionRepairAttempts; attempt++ {
		repairResponse, err := d.chat(ctx, d.clipDetectionChatRequest(data, clipDetectionResponseFormat(), clipDetectionRepairMessage(lastErr, lastResponse, attempt), d.repairMaxTokens()))
		if err != nil {
			return ClipDetectionResult{}, err
		}
		repaired, repairErr := parseAndValidateClipDetection(repairResponse.Content, request, maxClips)
		if repairErr == nil {
			return repaired, nil
		}
		lastResponse = repairResponse
		lastErr = repairErr
	}
	if clipStructuredResponseWasTruncated(lastResponse, lastErr) {
		return ClipDetectionResult{}, fmt.Errorf("clip detection response was truncated at max_tokens=%d after %d repair attempts: %w", d.repairMaxTokens(), maxClipDetectionRepairAttempts, lastErr)
	}
	return ClipDetectionResult{}, fmt.Errorf("clip detection response failed validation after %d repair attempts: %w", maxClipDetectionRepairAttempts, lastErr)
}

type clipDetectionWindow struct {
	ID           string
	StartIndex   int
	EndIndex     int
	StartSeconds float64
	EndSeconds   float64
	Segments     []TranscriptSegment
}

type clipDetectionCandidate struct {
	ID                 string
	WindowID           string
	WindowStartSeconds float64
	WindowEndSeconds   float64
	OriginalRank       int
	Decision           ClipDecision
}

func (d *OpenRouterClipDetector) detectChunked(ctx context.Context, request ClipDetectionRequest, maxClips int) (ClipDetectionResult, error) {
	windows, err := clipDetectionWindows(request, maxClips)
	if err != nil {
		return ClipDetectionResult{}, err
	}
	candidates := make([]clipDetectionCandidate, 0, len(windows)*maxClips)
	for windowIndex, window := range windows {
		data, err := clipDetectionRequestData(request, window.Segments, request.Segments, maxClips)
		if err != nil {
			return ClipDetectionResult{}, err
		}
		if len(data) > clipDetectionInputMaxBytes {
			return ClipDetectionResult{}, fmt.Errorf("clip detection window %d/%d is too large after transcript chunking", windowIndex+1, len(windows))
		}
		result, err := d.detectWithData(ctx, request, data, maxClips)
		if err != nil {
			return ClipDetectionResult{}, fmt.Errorf("clip detection window %d/%d failed: %w", windowIndex+1, len(windows), err)
		}
		for _, decision := range result.Clips {
			candidates = append(candidates, clipDetectionCandidate{
				ID:                 fmt.Sprintf("c_%04d", len(candidates)+1),
				WindowID:           window.ID,
				WindowStartSeconds: window.StartSeconds,
				WindowEndSeconds:   window.EndSeconds,
				OriginalRank:       decision.Rank,
				Decision:           decision,
			})
		}
	}
	selected, err := d.selectClipDetectionCandidates(ctx, request, candidates, maxClips)
	if err != nil {
		return ClipDetectionResult{}, err
	}
	result := clipDetectionResultFromCandidates(selected)
	if err := validateClipDetectionResult(result, request.Duration, durationSeconds(request.MinDurationSeconds), durationSeconds(request.MaxDurationSeconds), maxClips); err != nil {
		return ClipDetectionResult{}, fmt.Errorf("selected chunked clip detection result failed validation: %w", err)
	}
	return result, nil
}

func clipDetectionWindows(request ClipDetectionRequest, maxClips int) ([]clipDetectionWindow, error) {
	segments, err := clipDetectionAtomicSegments(request, maxClips)
	if err != nil {
		return nil, err
	}
	baseWindows, err := clipDetectionBaseWindows(request, segments, maxClips)
	if err != nil {
		return nil, err
	}
	if len(baseWindows) <= 1 {
		return baseWindows, nil
	}
	return clipDetectionWindowsWithOverlap(request, segments, baseWindows, maxClips), nil
}

func clipDetectionAtomicSegments(request ClipDetectionRequest, maxClips int) ([]TranscriptSegment, error) {
	segments := make([]TranscriptSegment, 0, len(request.Segments))
	for _, segment := range request.Segments {
		data, err := clipDetectionRequestData(request, []TranscriptSegment{segment}, request.Segments, maxClips)
		if err != nil {
			return nil, err
		}
		if len(data) <= clipDetectionInputMaxBytes {
			segments = append(segments, segment)
			continue
		}
		split := splitTranscriptSegmentForClipDetection(segment)
		if len(split) <= 1 {
			return nil, fmt.Errorf("transcript segment %s is too large for clip detection chunking", transcriptSegmentID(segment, len(segments)))
		}
		for _, part := range split {
			partData, err := clipDetectionRequestData(request, []TranscriptSegment{part}, request.Segments, maxClips)
			if err != nil {
				return nil, err
			}
			if len(partData) > clipDetectionInputMaxBytes {
				return nil, fmt.Errorf("transcript segment %s remains too large for clip detection chunking", strings.TrimSpace(part.ID))
			}
			segments = append(segments, part)
		}
	}
	return segments, nil
}

func splitTranscriptSegmentForClipDetection(segment TranscriptSegment) []TranscriptSegment {
	if len(segment.Words) <= clipDetectionMaxWordsPerUnit {
		return []TranscriptSegment{segment}
	}
	parts := make([]TranscriptSegment, 0, len(segment.Words)/clipDetectionMaxWordsPerUnit+1)
	baseID := strings.TrimSpace(segment.ID)
	if baseID == "" {
		baseID = "segment"
	}
	for start := 0; start < len(segment.Words); start += clipDetectionMaxWordsPerUnit {
		end := start + clipDetectionMaxWordsPerUnit
		if end > len(segment.Words) {
			end = len(segment.Words)
		}
		words := append([]TranscriptWord(nil), segment.Words[start:end]...)
		refs := make([]transcriptWordRef, 0, len(words))
		for index, word := range words {
			refs = append(refs, transcriptWordRef{Word: word, WordIndex: index, Index: index})
		}
		parts = append(parts, TranscriptSegment{
			StartSeconds: words[0].StartSeconds,
			EndSeconds:   words[len(words)-1].EndSeconds,
			Text:         joinedTranscriptWords(refs),
			ID:           fmt.Sprintf("%s_part_%03d", baseID, len(parts)+1),
			Words:        words,
		})
	}
	return parts
}

func clipDetectionBaseWindows(request ClipDetectionRequest, segments []TranscriptSegment, maxClips int) ([]clipDetectionWindow, error) {
	windows := make([]clipDetectionWindow, 0)
	for start := 0; start < len(segments); {
		end := start
		var selected []TranscriptSegment
		for end < len(segments) {
			candidate := append(append([]TranscriptSegment(nil), selected...), segments[end])
			data, err := clipDetectionRequestData(request, candidate, request.Segments, maxClips)
			if err != nil {
				return nil, err
			}
			if len(data) > clipDetectionInputMaxBytes {
				if len(selected) == 0 {
					return nil, fmt.Errorf("clip detection transcript chunk starting at segment %d is too large", start+1)
				}
				break
			}
			if len(data) > clipDetectionInputTargetBytes && len(selected) > 0 {
				break
			}
			selected = candidate
			end++
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("clip detection transcript chunking made no progress at segment %d", start+1)
		}
		windows = append(windows, newClipDetectionWindow(len(windows)+1, start, end, selected))
		start = end
	}
	return windows, nil
}

func clipDetectionWindowsWithOverlap(request ClipDetectionRequest, segments []TranscriptSegment, windows []clipDetectionWindow, maxClips int) []clipDetectionWindow {
	overlapSeconds := clipDetectionWindowOverlapSeconds(request)
	expanded := make([]clipDetectionWindow, 0, len(windows))
	for index, window := range windows {
		if index == 0 || overlapSeconds <= 0 {
			expanded = append(expanded, window)
			continue
		}
		start := window.StartIndex
		threshold := window.StartSeconds - overlapSeconds
		for start > 0 && segmentEndSeconds(segments[start-1]) >= threshold {
			start--
		}
		for start < window.StartIndex {
			candidateSegments := append([]TranscriptSegment(nil), segments[start:window.EndIndex]...)
			data, err := clipDetectionRequestData(request, candidateSegments, request.Segments, maxClips)
			if err == nil && len(data) <= clipDetectionInputMaxBytes {
				window = newClipDetectionWindow(index+1, start, window.EndIndex, candidateSegments)
				break
			}
			start++
		}
		expanded = append(expanded, window)
	}
	return expanded
}

func clipDetectionWindowOverlapSeconds(request ClipDetectionRequest) float64 {
	overlap := request.MaxDurationSeconds
	if overlap <= 0 {
		overlap = defaultClipMaxDuration.Seconds()
	}
	overlap += clipBoundaryLeadPadSeconds + clipBoundaryTailPadSeconds + clipBoundaryMinGapSeconds
	return overlap
}

func newClipDetectionWindow(index int, start int, end int, segments []TranscriptSegment) clipDetectionWindow {
	return clipDetectionWindow{
		ID:           fmt.Sprintf("w_%04d", index),
		StartIndex:   start,
		EndIndex:     end,
		StartSeconds: segmentStartSeconds(segments[0]),
		EndSeconds:   segmentEndSeconds(segments[len(segments)-1]),
		Segments:     append([]TranscriptSegment(nil), segments...),
	}
}

func segmentStartSeconds(segment TranscriptSegment) float64 {
	if len(segment.Words) > 0 {
		return segment.Words[0].StartSeconds
	}
	return segment.StartSeconds
}

func segmentEndSeconds(segment TranscriptSegment) float64 {
	if len(segment.Words) > 0 {
		return segment.Words[len(segment.Words)-1].EndSeconds
	}
	return segment.EndSeconds
}

func clipDetectionResultFromCandidates(candidates []clipDetectionCandidate) ClipDetectionResult {
	result := ClipDetectionResult{Clips: make([]ClipDecision, 0, len(candidates))}
	for index, candidate := range candidates {
		decision := candidate.Decision
		decision.Rank = index + 1
		result.Clips = append(result.Clips, decision)
	}
	return result
}

func (d *OpenRouterClipDetector) selectClipDetectionCandidates(ctx context.Context, request ClipDetectionRequest, candidates []clipDetectionCandidate, maxClips int) ([]clipDetectionCandidate, error) {
	return d.selectClipDetectionCandidatesRecursive(ctx, request, candidates, maxClips, 0)
}

func (d *OpenRouterClipDetector) selectClipDetectionCandidatesRecursive(ctx context.Context, request ClipDetectionRequest, candidates []clipDetectionCandidate, maxClips int, depth int) ([]clipDetectionCandidate, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("clip detection chunking produced no candidates")
	}
	if depth > 6 {
		return nil, fmt.Errorf("clip detection candidate selection exceeded recursive chunking depth")
	}
	data, err := clipDetectionCandidateSelectionData(request, candidates, maxClips)
	if err != nil {
		return nil, err
	}
	if len(data) <= clipDetectionInputMaxBytes {
		return d.selectClipDetectionCandidateBatch(ctx, request, candidates, data, maxClips)
	}
	batches, err := clipDetectionCandidateSelectionBatches(request, candidates, maxClips)
	if err != nil {
		return nil, err
	}
	if len(batches) <= 1 {
		return nil, fmt.Errorf("clip detection candidate selection input is too large for model request")
	}
	intermediate := make([]clipDetectionCandidate, 0, len(batches)*maxClips)
	batchMaxClips := maxClipSelectionBatchClips(maxClips)
	for index, batch := range batches {
		selected, err := d.selectClipDetectionCandidatesRecursive(ctx, request, batch, batchMaxClips, depth+1)
		if err != nil {
			return nil, fmt.Errorf("clip detection candidate selection batch %d/%d failed: %w", index+1, len(batches), err)
		}
		intermediate = append(intermediate, selected...)
	}
	if len(intermediate) >= len(candidates) {
		return nil, fmt.Errorf("clip detection candidate selection did not reduce an oversized candidate set")
	}
	return d.selectClipDetectionCandidatesRecursive(ctx, request, intermediate, maxClips, depth+1)
}

func maxClipSelectionBatchClips(maxClips int) int {
	if maxClips <= defaultClipDetectionMinClips {
		return defaultClipDetectionMinClips
	}
	limit := maxClips / 2
	if limit < defaultClipDetectionMinClips {
		return defaultClipDetectionMinClips
	}
	return limit
}

func (d *OpenRouterClipDetector) selectClipDetectionCandidateBatch(ctx context.Context, request ClipDetectionRequest, candidates []clipDetectionCandidate, data []byte, maxClips int) ([]clipDetectionCandidate, error) {
	response, err := d.chat(ctx, d.clipDetectionSelectionChatRequest(data, clipDetectionSelectionResponseFormat(), "", d.maxTokens))
	if err != nil {
		return nil, err
	}
	selected, parseErr := parseAndValidateClipDetectionCandidateSelection(response.Content, candidates, maxClips)
	if parseErr == nil {
		return selected, nil
	}
	lastResponse := response
	lastErr := parseErr
	for attempt := 1; attempt <= maxClipDetectionRepairAttempts; attempt++ {
		repairResponse, err := d.chat(ctx, d.clipDetectionSelectionChatRequest(data, clipDetectionSelectionResponseFormat(), clipDetectionSelectionRepairMessage(lastErr, lastResponse, attempt), d.repairMaxTokens()))
		if err != nil {
			return nil, err
		}
		repaired, repairErr := parseAndValidateClipDetectionCandidateSelection(repairResponse.Content, candidates, maxClips)
		if repairErr == nil {
			return repaired, nil
		}
		lastResponse = repairResponse
		lastErr = repairErr
	}
	if clipStructuredResponseWasTruncated(lastResponse, lastErr) {
		return nil, fmt.Errorf("clip detection candidate selection response was truncated at max_tokens=%d after %d repair attempts: %w", d.repairMaxTokens(), maxClipDetectionRepairAttempts, lastErr)
	}
	return nil, fmt.Errorf("clip detection candidate selection failed validation after %d repair attempts: %w", maxClipDetectionRepairAttempts, lastErr)
}

func clipDetectionCandidateSelectionData(request ClipDetectionRequest, candidates []clipDetectionCandidate, maxClips int) ([]byte, error) {
	limit := maxClips
	if limit <= 0 || limit > len(candidates) {
		limit = len(candidates)
	}
	input := clipDetectionCandidateSelectionInput{
		Video: clipDetectionVideo{
			Title:           strings.TrimSpace(request.Title),
			URL:             strings.TrimSpace(request.URL),
			Uploader:        strings.TrimSpace(request.Uploader),
			DurationSeconds: request.Duration.Seconds(),
		},
		Instructions: normalizedClipInstructions(request.Instructions),
		Constraints: clipDetectionCandidateSelectionConstraints{
			MinDurationSeconds: request.MinDurationSeconds,
			MaxDurationSeconds: request.MaxDurationSeconds,
			MinClips:           minClipSelectionClips(len(candidates), limit),
			MaxClips:           limit,
		},
		Candidates: clipDetectionCandidateInputs(candidates),
	}
	return json.Marshal(input)
}

func clipDetectionCandidateSelectionBatches(request ClipDetectionRequest, candidates []clipDetectionCandidate, maxClips int) ([][]clipDetectionCandidate, error) {
	batches := make([][]clipDetectionCandidate, 0)
	for start := 0; start < len(candidates); {
		end := start
		var selected []clipDetectionCandidate
		for end < len(candidates) {
			candidate := append(append([]clipDetectionCandidate(nil), selected...), candidates[end])
			data, err := clipDetectionCandidateSelectionData(request, candidate, maxClips)
			if err != nil {
				return nil, err
			}
			if len(data) > clipDetectionInputMaxBytes {
				if len(selected) == 0 {
					return nil, fmt.Errorf("clip detection candidate %s is too large for selection", strings.TrimSpace(candidates[end].ID))
				}
				break
			}
			if len(data) > clipDetectionInputTargetBytes && len(selected) > 0 {
				break
			}
			selected = candidate
			end++
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("clip detection candidate batching made no progress at candidate %d", start+1)
		}
		batches = append(batches, selected)
		start = end
	}
	return batches, nil
}

func minClipSelectionClips(candidateCount int, maxClips int) int {
	if candidateCount <= 0 || maxClips <= 0 {
		return 0
	}
	limit := defaultClipDetectionMinClips
	if candidateCount < limit {
		limit = candidateCount
	}
	if maxClips < limit {
		limit = maxClips
	}
	return limit
}

func clipDetectionCandidateInputs(candidates []clipDetectionCandidate) []clipDetectionCandidateInput {
	inputs := make([]clipDetectionCandidateInput, 0, len(candidates))
	for _, candidate := range candidates {
		decision := candidate.Decision
		inputs = append(inputs, clipDetectionCandidateInput{
			ID:                 strings.TrimSpace(candidate.ID),
			WindowID:           strings.TrimSpace(candidate.WindowID),
			WindowStartSeconds: candidate.WindowStartSeconds,
			WindowEndSeconds:   candidate.WindowEndSeconds,
			OriginalRank:       candidate.OriginalRank,
			Title:              strings.TrimSpace(decision.Title),
			Type:               strings.TrimSpace(decision.Type),
			Segments:           clipDetectionCandidateSegmentInputs(decision.Segments),
			Reason:             strings.TrimSpace(decision.Reason),
			Confidence:         decision.Confidence,
			ViralityScore:      decision.ViralityScore,
			HookScore:          decision.HookScore,
			RetentionScore:     decision.RetentionScore,
			ShareabilityScore:  decision.ShareabilityScore,
			DurationPolicy:     strings.TrimSpace(decision.DurationPolicy),
			ExceptionReason:    strings.TrimSpace(decision.ExceptionReason),
			DurationSeconds:    clipSegmentsDuration(decision.Segments).Seconds(),
		})
	}
	return inputs
}

func clipDetectionCandidateSegmentInputs(segments []ClipDecisionSegment) []clipDetectionCandidateSegmentInput {
	inputs := make([]clipDetectionCandidateSegmentInput, 0, len(segments))
	for _, segment := range segments {
		inputs = append(inputs, clipDetectionCandidateSegmentInput{
			StartWordID:        strings.TrimSpace(segment.StartWordID),
			EndWordID:          strings.TrimSpace(segment.EndWordID),
			SpeechStartSeconds: clipDecisionSegmentSpeechStartSeconds(segment),
			SpeechEndSeconds:   clipDecisionSegmentSpeechEndSeconds(segment),
			RenderStartSeconds: segment.StartSeconds,
			RenderEndSeconds:   segment.EndSeconds,
			Transcript:         strings.TrimSpace(segment.Transcript),
			BoundaryReason:     strings.TrimSpace(segment.BoundaryReason),
		})
	}
	return inputs
}

func clipDecisionSegmentSpeechStartSeconds(segment ClipDecisionSegment) float64 {
	if segment.SpeechEndSeconds > segment.SpeechStartSeconds {
		return segment.SpeechStartSeconds
	}
	return segment.StartSeconds
}

func clipDecisionSegmentSpeechEndSeconds(segment ClipDecisionSegment) float64 {
	if segment.SpeechEndSeconds > segment.SpeechStartSeconds {
		return segment.SpeechEndSeconds
	}
	return segment.EndSeconds
}

func parseAndValidateClipDetectionCandidateSelection(content string, candidates []clipDetectionCandidate, maxClips int) ([]clipDetectionCandidate, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("clip detection candidate selection model returned an empty response")
	}
	var response clipDetectionCandidateSelectionResponse
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("parse clip detection candidate selection response: %w", err)
	}
	limit := maxClips
	if limit <= 0 || limit > len(candidates) {
		limit = len(candidates)
	}
	if len(response.SelectedClipIDs) > limit {
		return nil, fmt.Errorf("clip detection candidate selection returned %d clips, maximum is %d", len(response.SelectedClipIDs), limit)
	}
	minClips := minClipSelectionClips(len(candidates), limit)
	if len(response.SelectedClipIDs) < minClips {
		return nil, fmt.Errorf("clip detection candidate selection returned %d clips, minimum is %d", len(response.SelectedClipIDs), minClips)
	}
	candidatesByID := make(map[string]clipDetectionCandidate, len(candidates))
	for _, candidate := range candidates {
		candidatesByID[strings.TrimSpace(candidate.ID)] = candidate
	}
	selected := make([]clipDetectionCandidate, 0, len(response.SelectedClipIDs))
	seenIDs := map[string]struct{}{}
	seenSourceSpans := map[string]string{}
	for index, id := range response.SelectedClipIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("selected_clip_ids[%d] is empty", index)
		}
		if _, exists := seenIDs[id]; exists {
			return nil, fmt.Errorf("selected_clip_ids contains duplicate candidate %s", id)
		}
		candidate, ok := candidatesByID[id]
		if !ok {
			return nil, fmt.Errorf("selected_clip_ids contains unknown candidate %s", id)
		}
		signature := clipDecisionSourceSignature(candidate.Decision)
		if previousID, exists := seenSourceSpans[signature]; exists {
			return nil, fmt.Errorf("selected_clip_ids contains duplicate source span %s and %s", previousID, id)
		}
		seenIDs[id] = struct{}{}
		seenSourceSpans[signature] = id
		selected = append(selected, candidate)
	}
	return selected, nil
}

func clipDecisionSourceSignature(decision ClipDecision) string {
	parts := make([]string, 0, len(decision.Segments)+1)
	parts = append(parts, strings.TrimSpace(decision.Type))
	for _, segment := range decision.Segments {
		parts = append(parts, strings.TrimSpace(segment.StartWordID)+"-"+strings.TrimSpace(segment.EndWordID))
	}
	return strings.Join(parts, "|")
}

func (d *OpenRouterClipDetector) repairMaxTokens() int {
	if d.maxTokens >= minClipDetectionRepairTokens {
		return d.maxTokens
	}
	return minClipDetectionRepairTokens
}

func parseAndValidateClipDetection(content string, request ClipDetectionRequest, maxClips int) (ClipDetectionResult, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return ClipDetectionResult{}, fmt.Errorf("clip detection model returned an empty response")
	}
	var response clipDetectionResponse
	decoder := json.NewDecoder(strings.NewReader(content))
	if err := decoder.Decode(&response); err != nil {
		return ClipDetectionResult{}, fmt.Errorf("parse clip detection response: %w", err)
	}
	result, err := alignClipDetectionResponseToTranscript(response, request)
	if err != nil {
		return ClipDetectionResult{}, err
	}
	if err := validateClipDetectionResult(result, request.Duration, durationSeconds(request.MinDurationSeconds), durationSeconds(request.MaxDurationSeconds), maxClips); err != nil {
		return ClipDetectionResult{}, err
	}
	return result, nil
}

func clipDetectionRepairMessage(validationErr error, response llm.ChatResponse, attempt int) string {
	message := strings.TrimSpace(validationErr.Error())
	if clipStructuredResponseWasTruncated(response, validationErr) {
		return "The previous structured JSON response was incomplete or truncated before the object ended. Regenerate one complete JSON object from scratch, keep every field concise, and ensure every opened object and array is closed. Validation error: " + message
	}
	parts := []string{
		fmt.Sprintf("The previous structured JSON response failed validation on repair attempt %d of %d. Return one complete corrected JSON object using the same transcript.", attempt, maxClipDetectionRepairAttempts),
		"Preserve the same semantic clip choices, rank order, titles, reasons, and segment roles unless the validation error proves that a candidate cannot be rendered from the supplied transcript word IDs.",
		"Repair missing, unknown, reversed, or unrenderable word IDs before replacing a candidate. Choose nearby start_word_id and end_word_id values that keep the same spoken moment, complete thought, and user-requested target.",
		"boundary_options are formatting constraints and quality hints only; they must not become the reason for switching to a generic clip.",
		"Prefer start_word_id values listed in boundary_options.clean_start_word_ids and end_word_id values listed in boundary_options.clean_end_word_ids when that preserves the selected moment.",
		"speech_units are transcript context windows. If the best boundary for the selected moment is a supplied speech_unit edge outside boundary_options, use that supplied word ID instead of moving the clip away from the target.",
		"When a nearby boundary_options.clean_start_word_ids item has reason after_weak_boundary_starter, prefer that option when it preserves the selected moment.",
		"Commas and transcript segment changes are weak standalone boundaries unless the transcript context makes them the best fit for the selected moment.",
		"If a boundary validation error says a clip cannot be rendered, choose a wider or narrower word span that preserves the same selected moment. If no coherent rendered span exists, replace that candidate with a different transcript-backed clip that directly matches the user instructions.",
		"Panda normalizes clip type from segment count, but returning type=continuous for one segment and type=spliced for 2-6 segments makes the response easier to inspect.",
		"Validation error: " + message,
	}
	if content := clippedClipDetectionRepairContent(response.Content); content != "" {
		parts = append(parts, "Previous invalid JSON:\n"+content)
	}
	return strings.Join(parts, "\n\n")
}

func clipDetectionSelectionRepairMessage(validationErr error, response llm.ChatResponse, attempt int) string {
	message := strings.TrimSpace(validationErr.Error())
	if clipStructuredResponseWasTruncated(response, validationErr) {
		return "The previous candidate selection JSON was incomplete or truncated before the object ended. Regenerate one complete JSON object from scratch with selected_clip_ids only. Validation error: " + message
	}
	parts := []string{
		fmt.Sprintf("The previous candidate selection response failed validation on repair attempt %d of %d.", attempt, maxClipDetectionRepairAttempts),
		"Return one complete corrected JSON object with selected_clip_ids only.",
		"Use only candidate IDs present in the supplied candidates list.",
		"Do not include duplicate candidate IDs or duplicate source spans.",
		"Keep the IDs in final rank order.",
		"Validation error: " + message,
	}
	if content := clippedClipDetectionRepairContent(response.Content); content != "" {
		parts = append(parts, "Previous invalid JSON:\n"+content)
	}
	return strings.Join(parts, "\n\n")
}

func clippedClipDetectionRepairContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if len(content) <= clipDetectionRepairMaxBytes {
		return content
	}
	return content[:clipDetectionRepairMaxBytes] + "\n... [truncated previous invalid JSON]"
}

type clipDetectionChatResult struct {
	response llm.ChatResponse
	err      error
}

func (d *OpenRouterClipDetector) chat(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	if d.timeout <= 0 {
		return d.client.Chat(ctx, request)
	}
	chatCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	resultC := make(chan clipDetectionChatResult, 1)
	go func() {
		response, err := d.client.Chat(chatCtx, request)
		resultC <- clipDetectionChatResult{response: response, err: err}
	}()
	select {
	case result := <-resultC:
		return result.response, result.err
	case <-chatCtx.Done():
		if chatCtx.Err() == context.DeadlineExceeded {
			return llm.ChatResponse{}, fmt.Errorf("clip detection model timed out after %s", d.timeout)
		}
		return llm.ChatResponse{}, chatCtx.Err()
	}
}

func (d *OpenRouterClipDetector) clipDetectionChatRequest(data []byte, responseFormat *llm.ResponseFormat, repairInstruction string, maxTokens int) llm.ChatRequest {
	systemPrompt := clipDetectionSystemPrompt()
	if responseFormat != nil && responseFormat.Type == "json_schema" {
		systemPrompt += " The response must be a single JSON object matching the supplied strict JSON schema."
	}
	if repairInstruction = strings.TrimSpace(repairInstruction); repairInstruction != "" {
		systemPrompt += " " + repairInstruction
	}
	return llm.ChatRequest{
		Model: d.model,
		Messages: []llm.Message{
			{
				Role:    "system",
				Content: systemPrompt,
			},
			{
				Role:    "user",
				Content: string(data),
			},
		},
		ResponseFormat: responseFormat,
		Temperature:    0.4,
		MaxTokens:      maxTokens,
	}
}

func (d *OpenRouterClipDetector) clipDetectionSelectionChatRequest(data []byte, responseFormat *llm.ResponseFormat, repairInstruction string, maxTokens int) llm.ChatRequest {
	systemPrompt := clipDetectionSelectionSystemPrompt()
	if responseFormat != nil && responseFormat.Type == "json_schema" {
		systemPrompt += " The response must be a single JSON object matching the supplied strict JSON schema."
	}
	if repairInstruction = strings.TrimSpace(repairInstruction); repairInstruction != "" {
		systemPrompt += " " + repairInstruction
	}
	return llm.ChatRequest{
		Model: d.model,
		Messages: []llm.Message{
			{
				Role:    "system",
				Content: systemPrompt,
			},
			{
				Role:    "user",
				Content: string(data),
			},
		},
		ResponseFormat: responseFormat,
		Temperature:    0.3,
		MaxTokens:      maxTokens,
	}
}

type clipDetectionInput struct {
	Video              clipDetectionVideo                `json:"video"`
	Instructions       string                            `json:"instructions"`
	Constraints        clipDetectionConstraints          `json:"constraints"`
	TranscriptSegments []clipDetectionTranscriptSegment  `json:"transcript_segments"`
	SpeechUnits        []clipDetectionSpeechUnit         `json:"speech_units"`
	BoundaryOptions    clipDetectionBoundaryOptionsInput `json:"boundary_options"`
}

type clipDetectionTranscriptSegment struct {
	Index        int     `json:"index"`
	ID           string  `json:"id"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Text         string  `json:"text"`
	WordCount    int     `json:"word_count"`
}

type clipDetectionSpeechUnit struct {
	ID                          string  `json:"id"`
	StartTranscriptSegmentIndex int     `json:"start_transcript_segment_index"`
	EndTranscriptSegmentIndex   int     `json:"end_transcript_segment_index"`
	StartWordID                 string  `json:"start_word_id"`
	EndWordID                   string  `json:"end_word_id"`
	StartBoundaryClean          bool    `json:"start_boundary_clean"`
	EndBoundaryClean            bool    `json:"end_boundary_clean"`
	StartBoundaryReason         string  `json:"start_boundary_reason"`
	EndBoundaryReason           string  `json:"end_boundary_reason"`
	StartSeconds                float64 `json:"start_seconds"`
	EndSeconds                  float64 `json:"end_seconds"`
	Text                        string  `json:"text"`
	PreviousWord                string  `json:"previous_word,omitempty"`
	NextWord                    string  `json:"next_word,omitempty"`
	PauseBefore                 float64 `json:"pause_before_seconds,omitempty"`
	PauseAfter                  float64 `json:"pause_after_seconds,omitempty"`
}

type clipDetectionBoundaryOptionsInput struct {
	CleanStartWordIDs []clipDetectionBoundaryOptionInput `json:"clean_start_word_ids"`
	CleanEndWordIDs   []clipDetectionBoundaryOptionInput `json:"clean_end_word_ids"`
}

type clipDetectionBoundaryOptionInput struct {
	WordID       string  `json:"word_id"`
	SpeechUnitID string  `json:"speech_unit_id"`
	TimeSeconds  float64 `json:"time_seconds"`
	Text         string  `json:"text"`
	Reason       string  `json:"reason"`
}

type clipDetectionVideo struct {
	Title           string  `json:"title"`
	URL             string  `json:"url"`
	Uploader        string  `json:"uploader"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type clipDetectionConstraints struct {
	MinDurationSeconds        float64 `json:"min_duration_seconds"`
	MaxDurationSeconds        float64 `json:"max_duration_seconds"`
	TargetMinSeconds          float64 `json:"target_min_seconds"`
	TargetMaxSeconds          float64 `json:"target_max_seconds"`
	RenderLeadPadSeconds      float64 `json:"render_lead_pad_seconds"`
	RenderTailPadSeconds      float64 `json:"render_tail_pad_seconds"`
	MinSpliceSourceGapSeconds float64 `json:"min_splice_source_gap_seconds"`
	MinClips                  int     `json:"min_clips"`
	MaxClips                  int     `json:"max_clips"`
}

type clipDetectionCandidateSelectionInput struct {
	Video        clipDetectionVideo                         `json:"video"`
	Instructions string                                     `json:"instructions"`
	Constraints  clipDetectionCandidateSelectionConstraints `json:"constraints"`
	Candidates   []clipDetectionCandidateInput              `json:"candidates"`
}

type clipDetectionCandidateSelectionConstraints struct {
	MinDurationSeconds float64 `json:"min_duration_seconds"`
	MaxDurationSeconds float64 `json:"max_duration_seconds"`
	MinClips           int     `json:"min_clips"`
	MaxClips           int     `json:"max_clips"`
}

type clipDetectionCandidateInput struct {
	ID                 string                               `json:"id"`
	WindowID           string                               `json:"window_id"`
	WindowStartSeconds float64                              `json:"window_start_seconds"`
	WindowEndSeconds   float64                              `json:"window_end_seconds"`
	OriginalRank       int                                  `json:"original_rank"`
	Title              string                               `json:"title"`
	Type               string                               `json:"type"`
	Segments           []clipDetectionCandidateSegmentInput `json:"segments"`
	Reason             string                               `json:"reason"`
	Confidence         float64                              `json:"confidence"`
	ViralityScore      int                                  `json:"virality_score"`
	HookScore          int                                  `json:"hook_score"`
	RetentionScore     int                                  `json:"retention_score"`
	ShareabilityScore  int                                  `json:"shareability_score"`
	DurationPolicy     string                               `json:"duration_policy"`
	ExceptionReason    string                               `json:"exception_reason"`
	DurationSeconds    float64                              `json:"duration_seconds"`
}

type clipDetectionCandidateSegmentInput struct {
	StartWordID        string  `json:"start_word_id"`
	EndWordID          string  `json:"end_word_id"`
	SpeechStartSeconds float64 `json:"speech_start_seconds"`
	SpeechEndSeconds   float64 `json:"speech_end_seconds"`
	RenderStartSeconds float64 `json:"render_start_seconds"`
	RenderEndSeconds   float64 `json:"render_end_seconds"`
	Transcript         string  `json:"transcript"`
	BoundaryReason     string  `json:"boundary_reason"`
}

type clipDetectionResponse struct {
	Clips []clipDetectionResponseClip `json:"clips"`
}

type clipDetectionCandidateSelectionResponse struct {
	SelectedClipIDs []string `json:"selected_clip_ids"`
}

type clipDetectionResponseClip struct {
	Rank              int                            `json:"rank"`
	Title             string                         `json:"title"`
	Type              string                         `json:"type"`
	Segments          []clipDetectionResponseSegment `json:"segments"`
	Reason            string                         `json:"reason"`
	Confidence        float64                        `json:"confidence"`
	ViralityScore     int                            `json:"virality_score"`
	HookScore         int                            `json:"hook_score"`
	RetentionScore    int                            `json:"retention_score"`
	ShareabilityScore int                            `json:"shareability_score"`
	DurationPolicy    string                         `json:"duration_policy"`
	ExceptionReason   string                         `json:"exception_reason"`
}

type clipDetectionResponseSegment struct {
	StartWordID    string `json:"start_word_id"`
	EndWordID      string `json:"end_word_id"`
	Transcript     string `json:"transcript"`
	BoundaryReason string `json:"boundary_reason"`
}

func clipDetectionSystemPrompt() string {
	return strings.Join([]string{
		"You are Panda's dedicated short-form clip director for YouTube transcripts.",
		"Your job is to return a ranked set of transcript-backed clips with genuine stop-scroll potential, not a summary.",
		"Use the user's instructions as the targeting lens. If the instructions name a topic, person, claim, moment, emotion, or style, return only clips that satisfy that target; do not wander to generic best moments. If the instructions are broad/default, find the strongest clips across the video.",
		"Return every distinct viable clip the transcript supports, from constraints.min_clips up to the provided max_clips. Broad/default requests should be expansive: scan the whole transcript and surface all strong standalone candidates, not just the first few. Do not cap the default viral preset at 3 clips. A quick soundbite and a fuller story version may both be valid if they serve different posting goals. Never add generic or confusing clips; the quality bar applies to every clip.",
		"Return clips in rank order with rank values 1..N and no gaps.",
		"Select in this order: first identify standalone moments with clear meaning, user relevance, hook, and payoff; then choose word-backed boundaries that preserve those exact moments.",
		"For each candidate, explicitly know the subject before returning it: who or what is being discussed, what claim/action/reaction happens, why it matters, and what payoff or tension lands. If that cannot be answered from the returned transcript span, extend to include the missing setup or skip the candidate.",
		"The clip title and reason must name the concrete subject, claim, conflict, question, or payoff. Avoid vague titles or reasons such as \"Interesting Point\", \"Great Reaction\", \"They explain it\", or \"This is important\" unless the named subject is also present.",
		"Use speech_units to understand the transcript flow. Boundary options are formatting constraints and quality hints, not a discovery or ranking signal; prefer clean_start_word_ids and clean_end_word_ids only after selecting the clip moment. If the best boundary for the selected moment is a supplied speech_unit edge outside boundary_options, use that supplied word ID instead of moving the clip away from the target.",
		"Do not return start_seconds, end_seconds, start_segment_index, end_segment_index, or any model-owned timing fields. Panda derives all timing from the selected transcript word IDs.",
		"Every returned segment must be an inclusive word-backed span using supplied word IDs. The transcript text should exactly match that span.",
		"Prefer clean standalone starts and endings; avoid filler, weak connective words, dangling prepositions, contextless pronouns, mid-sentence starts, and unresolved endings when a nearby boundary preserves the chosen moment.",
		"Do not start a clip with orphaned pronouns or references like he, she, they, it, this, that, those, these, here, there, the guy, the thing, or the problem unless the returned span quickly establishes the antecedent. Do not return a reaction, answer, quote, or punchline without the setup needed to understand it.",
		"For spliced clips, obey constraints.min_splice_source_gap_seconds between source spans. If two spans are too close, return one continuous segment covering the complete thought.",
		"Set transcript to the exact source text covered by the inclusive word span, lightly joined with spaces only when the range spans multiple transcript segments.",
		"Set boundary_reason to a concise explanation of why the chosen first and last words preserve the selected moment.",
		"Each clip may be type=continuous with one segment or type=spliced with 2-6 ordered segments. Use spliced clips when cutting dead air, repeated filler, meandering setup, or distant setup/payoff moments creates a more cohesive short-form story.",
		"Clip type should match segment count: continuous means exactly one returned segment; spliced means two to six returned segments.",
		"Spliced segments must preserve narrative logic. Do not create deceptive edits that reverse meaning. Do not splice unrelated moments just because each moment is individually interesting.",
		"For spliced clips, each segment must have a clear role: hook, setup, escalation, punchline, reveal, reaction, or payoff. Keep only the smallest segments needed for flow.",
		"Keep title, reason, exception_reason, and segment transcript snippets concise so up to 12 clips can fit in one JSON response.",
		"Choose hook-first boundaries: the first 0.5-2 seconds should contain the strongest hook, emotional spike, bold claim, question, contradiction, consequence, funny reaction, or open loop.",
		"Prefer ending after the thought, punchline, reveal, or exchange lands. Avoid ending mid-sentence, mid-word, or on weak connective words, prepositions, setup-only clauses, or unresolved references when a nearby boundary preserves the chosen moment.",
		"If the best moment starts or ends inside an ongoing idea, extend to neighboring speech units when that improves standalone clarity without losing the target.",
		"Every clip must make sense when watched alone. Include the minimum setup needed for pronouns, stakes, contrast, punchline, or payoff; skip candidates that only work with missing context.",
		"Reject subjectless fragments, setup-only fragments, answer-only fragments, generic commentary, list items without the list topic, examples without the rule they demonstrate, and transitions between ideas. A clip needs a complete mini-arc: setup, tension or insight, and payoff.",
		"Duration policy: target 30-45 seconds by default; use shorter clips only for extreme standalone reactions, memes, quotable soundbites, or user-requested brevity; use longer clips only when setup and payoff truly require it. Always obey the min/max constraints.",
		"Score honestly. No hook caps virality. High-arousal emotion, shareability, retention, debate potential, humor, surprise, charisma, stakes, or useful novelty should drive high scores.",
		"Reject calm explanations, slow context, filler-heavy starts, low-energy moments, and clips that require a long explanation to be interesting.",
		"When uncertain between a shorter confusing clip and a slightly longer clear clip, choose the clear clip. When uncertain whether a moment has enough subject/context, do not return it just to fill constraints.",
		"Use only timestamps supported by the transcript and video duration. Do not invent visual-only details.",
		"Do not include derived timing fields such as total duration; Panda computes clip duration from the returned segments.",
		"Return only strict JSON matching the schema. Do not include Markdown or commentary.",
	}, " ")
}

func clipDetectionSelectionSystemPrompt() string {
	return strings.Join([]string{
		"You are Panda's final short-form clip selector.",
		"You receive model-detected, word-backed candidate clips from multiple transcript windows of the same YouTube video.",
		"Your job is to choose the strongest final ranked set by candidate ID only. Do not create new clips, new boundaries, new titles, or new timing.",
		"Use the user's instructions as the targeting lens. If the instructions name a topic, person, claim, moment, emotion, or style, select only candidates that satisfy that target.",
		"Pick from constraints.min_clips up to constraints.max_clips candidates when the candidate set supports it. Quality beats filling every slot.",
		"Rank for stop-scroll potential, standalone clarity, hook strength, payoff, shareability, retention, and fit to the request.",
		"Prefer candidates whose transcript, title, and reason establish a concrete subject and mini-arc. Reject candidates with missing subject, contextless pronouns, setup-only fragments, answer-only fragments, or payoff without setup, even if their scores are high.",
		"Avoid near-duplicates and duplicate source spans, especially candidates repeated because transcript windows overlap. Prefer the clearest, most complete version.",
		"Prefer candidates with better narrative completeness over a slightly higher score when the scores are close.",
		"Return selected_clip_ids in final rank order. Use only IDs present in candidates.",
		"Return only strict JSON matching the schema. Do not include Markdown or commentary.",
	}, " ")
}

func clipDetectionSchema() json.RawMessage {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"clips": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"rank": map[string]any{
							"type": "integer",
						},
						"title": map[string]any{
							"type": "string",
						},
						"type": map[string]any{
							"type": "string",
							"enum": []string{"continuous", "spliced"},
						},
						"segments": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": false,
								"properties": map[string]any{
									"start_word_id": map[string]any{"type": "string"},
									"end_word_id":   map[string]any{"type": "string"},
									"transcript": map[string]any{
										"type": "string",
									},
									"boundary_reason": map[string]any{"type": "string"},
								},
								"required": []string{
									"start_word_id",
									"end_word_id",
									"transcript",
									"boundary_reason",
								},
							},
						},
						"reason": map[string]any{
							"type": "string",
						},
						"confidence": map[string]any{
							"type": "number",
						},
						"virality_score": map[string]any{
							"type": "integer",
						},
						"hook_score": map[string]any{
							"type": "integer",
						},
						"retention_score": map[string]any{
							"type": "integer",
						},
						"shareability_score": map[string]any{
							"type": "integer",
						},
						"duration_policy": map[string]any{
							"type": "string",
							"enum": []string{
								"target_30_45",
								"short_exception",
								"long_context_exception",
								"requested_duration",
								"other",
							},
						},
						"exception_reason": map[string]any{
							"type": "string",
						},
					},
					"required": []string{
						"rank",
						"title",
						"type",
						"segments",
						"reason",
						"confidence",
						"virality_score",
						"hook_score",
						"retention_score",
						"shareability_score",
						"duration_policy",
						"exception_reason",
					},
				},
			},
		},
		"required": []string{"clips"},
	})
	return json.RawMessage(schema)
}

func clipDetectionSelectionSchema() json.RawMessage {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"selected_clip_ids": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
		},
		"required": []string{"selected_clip_ids"},
	})
	return json.RawMessage(schema)
}

func clipDetectionResponseFormat() *llm.ResponseFormat {
	return &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.ResponseFormatSchema{
			Name:   "youtube_clip_detection",
			Strict: true,
			Schema: clipDetectionSchema(),
		},
	}
}

func clipDetectionSelectionResponseFormat() *llm.ResponseFormat {
	return &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.ResponseFormatSchema{
			Name:   "youtube_clip_detection_selection",
			Strict: true,
			Schema: clipDetectionSelectionSchema(),
		},
	}
}

func clipDetectionTranscriptContext(segments []TranscriptSegment) ([]clipDetectionTranscriptSegment, []clipDetectionSpeechUnit, clipDetectionBoundaryOptionsInput, error) {
	return clipDetectionTranscriptContextWithBoundarySegments(segments, nil)
}

func clipDetectionTranscriptContextWithBoundarySegments(segments []TranscriptSegment, boundarySegments []TranscriptSegment) ([]clipDetectionTranscriptSegment, []clipDetectionSpeechUnit, clipDetectionBoundaryOptionsInput, error) {
	indexed := indexedClipDetectionTranscriptSegments(segments)
	refs, _, err := transcriptWordReferences(segments)
	if err != nil {
		return nil, nil, clipDetectionBoundaryOptionsInput{}, err
	}
	boundaryRefs := refs
	if len(boundarySegments) > 0 {
		var boundaryErr error
		boundaryRefs, _, boundaryErr = transcriptWordReferences(boundarySegments)
		if boundaryErr != nil {
			return nil, nil, clipDetectionBoundaryOptionsInput{}, boundaryErr
		}
	}
	units := clipDetectionSpeechUnitsWithBoundaryRefs(refs, boundaryRefs)
	if len(units) == 0 {
		return nil, nil, clipDetectionBoundaryOptionsInput{}, fmt.Errorf("word-backed speech units are required for clip detection")
	}
	return indexed, units, clipDetectionBoundaryOptionsInputForRefs(refs, units), nil
}

func indexedClipDetectionTranscriptSegments(segments []TranscriptSegment) []clipDetectionTranscriptSegment {
	indexed := make([]clipDetectionTranscriptSegment, 0, len(segments))
	for index, segment := range segments {
		indexed = append(indexed, clipDetectionTranscriptSegment{
			Index:        index,
			ID:           transcriptSegmentID(segment, index),
			StartSeconds: segment.StartSeconds,
			EndSeconds:   segment.EndSeconds,
			Text:         segment.Text,
			WordCount:    len(segment.Words),
		})
	}
	return indexed
}

type transcriptWordRef struct {
	Word         TranscriptWord
	Segment      TranscriptSegment
	SegmentIndex int
	WordIndex    int
	Index        int
}

func transcriptWordReferences(segments []TranscriptSegment) ([]transcriptWordRef, map[string]transcriptWordRef, error) {
	refs := make([]transcriptWordRef, 0)
	byID := map[string]transcriptWordRef{}
	for segmentIndex, segment := range segments {
		for wordIndex, word := range segment.Words {
			wordID := strings.TrimSpace(word.ID)
			if wordID == "" {
				return nil, nil, fmt.Errorf("word timing is missing a word ID in transcript segment %d", segmentIndex)
			}
			if word.EndSeconds <= word.StartSeconds {
				return nil, nil, fmt.Errorf("word timing for %s has invalid timestamps", wordID)
			}
			if _, exists := byID[wordID]; exists {
				return nil, nil, fmt.Errorf("word timing contains duplicate word ID %s", wordID)
			}
			ref := transcriptWordRef{
				Word:         word,
				Segment:      segment,
				SegmentIndex: segmentIndex,
				WordIndex:    wordIndex,
				Index:        len(refs),
			}
			refs = append(refs, ref)
			byID[wordID] = ref
		}
	}
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("word timing is required for clip detection")
	}
	return refs, byID, nil
}

func transcriptSegmentID(segment TranscriptSegment, index int) string {
	if id := strings.TrimSpace(segment.ID); id != "" {
		return id
	}
	return fmt.Sprintf("s_%04d", index+1)
}

func clipDetectionSpeechUnits(refs []transcriptWordRef) []clipDetectionSpeechUnit {
	return clipDetectionSpeechUnitsWithBoundaryRefs(refs, refs)
}

func clipDetectionSpeechUnitsWithBoundaryRefs(refs []transcriptWordRef, boundaryRefs []transcriptWordRef) []clipDetectionSpeechUnit {
	if len(refs) == 0 {
		return nil
	}
	if len(boundaryRefs) == 0 {
		boundaryRefs = refs
	}
	boundaryIndexes := clipDetectionBoundaryIndexByWordID(boundaryRefs)
	units := make([]clipDetectionSpeechUnit, 0, len(refs)/8+1)
	start := 0
	for index := range refs {
		wordCount := index - start + 1
		split := wordCount >= clipDetectionMaxWordsPerUnit || clipWordEndsCleanBoundary(refs[index].Word.Text)
		if index+1 < len(refs) {
			next := refs[index+1]
			pause := next.Word.StartSeconds - refs[index].Word.EndSeconds
			if clipWordIsSkippableWeakStarter(boundaryRefs, boundaryIndexForLocalRef(boundaryIndexes, refs, index)) {
				split = true
			}
			if pause >= clipDetectionUnitPauseSeconds {
				split = true
			}
		}
		if !split {
			continue
		}
		units = append(units, clipDetectionSpeechUnitFromRefs(len(units)+1, refs, boundaryRefs, boundaryIndexes, start, index))
		start = index + 1
	}
	if start < len(refs) {
		units = append(units, clipDetectionSpeechUnitFromRefs(len(units)+1, refs, boundaryRefs, boundaryIndexes, start, len(refs)-1))
	}
	return units
}

func clipDetectionBoundaryIndexByWordID(refs []transcriptWordRef) map[string]int {
	indexes := make(map[string]int, len(refs))
	for index, ref := range refs {
		if wordID := strings.TrimSpace(ref.Word.ID); wordID != "" {
			indexes[wordID] = index
		}
	}
	return indexes
}

func boundaryIndexForLocalRef(boundaryIndexes map[string]int, refs []transcriptWordRef, localIndex int) int {
	if localIndex < 0 || localIndex >= len(refs) {
		return localIndex
	}
	if index, ok := boundaryIndexes[strings.TrimSpace(refs[localIndex].Word.ID)]; ok {
		return index
	}
	return localIndex
}

func clipDetectionSpeechUnitFromRefs(unitIndex int, refs []transcriptWordRef, boundaryRefs []transcriptWordRef, boundaryIndexes map[string]int, start int, end int) clipDetectionSpeechUnit {
	startRef := refs[start]
	endRef := refs[end]
	startBoundaryIndex := boundaryIndexForLocalRef(boundaryIndexes, refs, start)
	endBoundaryIndex := boundaryIndexForLocalRef(boundaryIndexes, refs, end)
	startBoundaryClean, startBoundaryReason := clipStartBoundaryQuality(boundaryRefs, startBoundaryIndex)
	endBoundaryClean, endBoundaryReason := clipEndBoundaryQuality(boundaryRefs, endBoundaryIndex)
	unit := clipDetectionSpeechUnit{
		ID:                          fmt.Sprintf("u_%04d", unitIndex),
		StartTranscriptSegmentIndex: startRef.SegmentIndex,
		EndTranscriptSegmentIndex:   endRef.SegmentIndex,
		StartWordID:                 strings.TrimSpace(startRef.Word.ID),
		EndWordID:                   strings.TrimSpace(endRef.Word.ID),
		StartBoundaryClean:          startBoundaryClean,
		EndBoundaryClean:            endBoundaryClean,
		StartBoundaryReason:         startBoundaryReason,
		EndBoundaryReason:           endBoundaryReason,
		StartSeconds:                startRef.Word.StartSeconds,
		EndSeconds:                  endRef.Word.EndSeconds,
		Text:                        joinedTranscriptWords(refs[start : end+1]),
	}
	if previous, ok := previousBoundaryWord(boundaryRefs, startBoundaryIndex, refs, start); ok {
		unit.PreviousWord = cleanTranscriptText(previous.Text)
		if pause := startRef.Word.StartSeconds - previous.EndSeconds; pause > 0 {
			unit.PauseBefore = pause
		}
	}
	if next, ok := nextBoundaryWord(boundaryRefs, endBoundaryIndex, refs, end); ok {
		unit.NextWord = cleanTranscriptText(next.Text)
		if pause := next.StartSeconds - endRef.Word.EndSeconds; pause > 0 {
			unit.PauseAfter = pause
		}
	}
	return unit
}

func previousBoundaryWord(boundaryRefs []transcriptWordRef, boundaryIndex int, refs []transcriptWordRef, localIndex int) (TranscriptWord, bool) {
	if boundaryIndex > 0 && boundaryIndex <= len(boundaryRefs) {
		return boundaryRefs[boundaryIndex-1].Word, true
	}
	if localIndex > 0 && localIndex <= len(refs) {
		return refs[localIndex-1].Word, true
	}
	return TranscriptWord{}, false
}

func nextBoundaryWord(boundaryRefs []transcriptWordRef, boundaryIndex int, refs []transcriptWordRef, localIndex int) (TranscriptWord, bool) {
	if boundaryIndex >= 0 && boundaryIndex+1 < len(boundaryRefs) {
		return boundaryRefs[boundaryIndex+1].Word, true
	}
	if localIndex >= 0 && localIndex+1 < len(refs) {
		return refs[localIndex+1].Word, true
	}
	return TranscriptWord{}, false
}

func alignClipDetectionResponseToTranscript(response clipDetectionResponse, request ClipDetectionRequest) (ClipDetectionResult, error) {
	refs, refsByID, err := transcriptWordReferences(request.Segments)
	if err != nil {
		return ClipDetectionResult{}, err
	}
	aligned := ClipDetectionResult{Clips: make([]ClipDecision, 0, len(response.Clips))}
	alignmentErrors := make([]string, 0)
	for clipIndex, clip := range response.Clips {
		alignedClip := ClipDecision{
			Rank:              len(aligned.Clips) + 1,
			Title:             normalizedClipTitle(clip.Title, clipIndex+1),
			Reason:            normalizedClipReason(clip.Reason),
			Confidence:        normalizedClipConfidence(clip.Confidence),
			ViralityScore:     normalizedClipScore(clip.ViralityScore),
			HookScore:         normalizedClipScore(clip.HookScore),
			RetentionScore:    normalizedClipScore(clip.RetentionScore),
			ShareabilityScore: normalizedClipScore(clip.ShareabilityScore),
			DurationPolicy:    normalizedClipDurationPolicy(clip.DurationPolicy),
			ExceptionReason:   strings.TrimSpace(clip.ExceptionReason),
		}
		alignedClip.Segments = make([]ClipDecisionSegment, 0, len(clip.Segments))
		clipValid := true
		for segmentIndex, segment := range clip.Segments {
			alignedSegment, err := alignClipDecisionSegmentToTranscript(segment, refs, refsByID)
			if err != nil {
				alignmentErrors = append(alignmentErrors, fmt.Sprintf("clip %d segment %d: %v", clipIndex+1, segmentIndex+1, err))
				clipValid = false
				break
			}
			alignedClip.Segments = append(alignedClip.Segments, alignedSegment)
		}
		if !clipValid {
			continue
		}
		if len(alignedClip.Segments) == 0 {
			alignmentErrors = append(alignmentErrors, fmt.Sprintf("clip %d: missing segments", clipIndex+1))
			continue
		}
		normalized, changed, err := normalizeClipDecisionSegmentsForRender(alignedClip.Segments, refs, refsByID)
		if err != nil {
			alignmentErrors = append(alignmentErrors, fmt.Sprintf("clip %d: %v", clipIndex+1, err))
			continue
		}
		if changed {
			alignedClip.Segments = normalized
		}
		alignedClip.Type = clipTypeForSegmentCount(len(alignedClip.Segments))
		padded, err := padClipDecisionSegmentBoundaries(alignedClip.Segments, request.Duration, durationSeconds(request.MaxDurationSeconds))
		if err != nil {
			alignmentErrors = append(alignmentErrors, fmt.Sprintf("clip %d: %v", clipIndex+1, err))
			continue
		}
		alignedClip.Segments = padded
		aligned.Clips = append(aligned.Clips, alignedClip)
	}
	if len(aligned.Clips) == 0 && len(alignmentErrors) > 0 {
		return ClipDetectionResult{}, fmt.Errorf("clip detection response contained no renderable clips after alignment: %s", strings.Join(alignmentErrors, "; "))
	}
	return aligned, nil
}

func normalizedClipTitle(title string, rank int) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	return fmt.Sprintf("Clip %d", rank)
}

func normalizedClipReason(reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" {
		return reason
	}
	return "Selected by the clip detection model."
}

func normalizedClipConfidence(confidence float64) float64 {
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func normalizedClipScore(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func normalizedClipDurationPolicy(policy string) string {
	switch policy = strings.TrimSpace(policy); policy {
	case "target_30_45", "short_exception", "long_context_exception", "requested_duration", "other":
		return policy
	default:
		return "other"
	}
}

func clipTypeForSegmentCount(count int) string {
	if count == 1 {
		return "continuous"
	}
	return "spliced"
}

func normalizeClipDecisionSegmentsForRender(segments []ClipDecisionSegment, refs []transcriptWordRef, refsByID map[string]transcriptWordRef) ([]ClipDecisionSegment, bool, error) {
	if len(segments) < 2 {
		return segments, false, nil
	}
	normalized := make([]ClipDecisionSegment, 0, len(segments))
	current := segments[0]
	changed := false
	for index := 1; index < len(segments); index++ {
		next := segments[index]
		if !clipDecisionSegmentsNeedContinuousRender(current, next) {
			normalized = append(normalized, current)
			current = next
			continue
		}
		merged, err := mergeClipDecisionSegments(current, next, refs, refsByID)
		if err != nil {
			return nil, false, err
		}
		current = merged
		changed = true
	}
	normalized = append(normalized, current)
	return normalized, changed, nil
}

func clipDecisionSegmentsNeedContinuousRender(previous ClipDecisionSegment, next ClipDecisionSegment) bool {
	return next.SpeechStartSeconds-previous.SpeechEndSeconds < clipRequiredSpliceSourceGapSeconds()-0.000001
}

func clipRequiredSpliceSourceGapSeconds() float64 {
	return clipBoundaryLeadPadSeconds + clipBoundaryTailPadSeconds + clipBoundaryMinGapSeconds
}

func mergeClipDecisionSegments(previous ClipDecisionSegment, next ClipDecisionSegment, refs []transcriptWordRef, refsByID map[string]transcriptWordRef) (ClipDecisionSegment, error) {
	startWordID := strings.TrimSpace(previous.StartWordID)
	endWordID := strings.TrimSpace(next.EndWordID)
	startRef, startOK := refsByID[startWordID]
	endRef, endOK := refsByID[endWordID]
	if !startOK || !endOK {
		return ClipDecisionSegment{}, fmt.Errorf("cannot merge render-adjacent clip segments with missing word IDs")
	}
	if endRef.Index < startRef.Index {
		return ClipDecisionSegment{}, fmt.Errorf("cannot merge render-adjacent clip segments because the end word precedes the start word")
	}
	words := refs[startRef.Index : endRef.Index+1]
	return ClipDecisionSegment{
		StartWordID:        startWordID,
		EndWordID:          endWordID,
		StartSeconds:       startRef.Word.StartSeconds,
		EndSeconds:         endRef.Word.EndSeconds,
		SpeechStartSeconds: startRef.Word.StartSeconds,
		SpeechEndSeconds:   endRef.Word.EndSeconds,
		Transcript:         joinedTranscriptWords(words),
		BoundaryReason:     joinedClipBoundaryReasons(previous.BoundaryReason, next.BoundaryReason),
	}, nil
}

func joinedClipBoundaryReasons(reasons ...string) string {
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			parts = append(parts, reason)
		}
	}
	return strings.Join(parts, " ")
}

func padClipDecisionSegmentBoundaries(segments []ClipDecisionSegment, videoDuration time.Duration, maxDuration time.Duration) ([]ClipDecisionSegment, error) {
	if len(segments) == 0 {
		return nil, nil
	}
	padded := paddedClipDecisionSegments(segments, videoDuration, 1)
	if maxDuration > 0 {
		total := clipDecisionSegmentsTotalSeconds(padded)
		if total > maxDuration.Seconds()+0.001 {
			speechTotal := clipDecisionSegmentsTotalSeconds(segments)
			if speechTotal > maxDuration.Seconds()+0.001 {
				return nil, fmt.Errorf("clip duration %.1fs exceeds maximum %.1fs after required render padding", total, maxDuration.Seconds())
			}
			paddingTotal := total - speechTotal
			paddingBudget := maxDuration.Seconds() - speechTotal
			paddingScale := 0.0
			if paddingTotal > 0 && paddingBudget > 0 {
				paddingScale = paddingBudget / paddingTotal
			}
			padded = paddedClipDecisionSegments(segments, videoDuration, paddingScale)
		}
	}
	for index := 1; index < len(padded); index++ {
		if padded[index-1].EndSeconds <= padded[index].StartSeconds-clipBoundaryMinGapSeconds {
			continue
		}
		return nil, fmt.Errorf("segments %d and %d are too close for render padding; choose wider word boundaries or a continuous clip", index, index+1)
	}
	return padded, nil
}

func paddedClipDecisionSegments(segments []ClipDecisionSegment, videoDuration time.Duration, paddingScale float64) []ClipDecisionSegment {
	padded := make([]ClipDecisionSegment, len(segments))
	videoEnd := videoDuration.Seconds()
	if paddingScale < 0 {
		paddingScale = 0
	}
	if paddingScale > 1 {
		paddingScale = 1
	}
	for index, segment := range segments {
		padded[index] = segment
		padded[index].StartSeconds = segment.StartSeconds - clipBoundaryLeadPadSeconds*paddingScale
		if padded[index].StartSeconds < 0 {
			padded[index].StartSeconds = 0
		}
		padded[index].EndSeconds = segment.EndSeconds + clipBoundaryTailPadSeconds*paddingScale
		if videoEnd > 0 && padded[index].EndSeconds > videoEnd {
			padded[index].EndSeconds = videoEnd
		}
	}
	return padded
}

func clipDecisionSegmentsTotalSeconds(segments []ClipDecisionSegment) float64 {
	total := 0.0
	for _, segment := range segments {
		total += segment.EndSeconds - segment.StartSeconds
	}
	return total
}

func clipDetectionBoundaryOptionsInputForRefs(refs []transcriptWordRef, units []clipDetectionSpeechUnit) clipDetectionBoundaryOptionsInput {
	refsByID := make(map[string]transcriptWordRef, len(refs))
	for _, ref := range refs {
		refsByID[strings.TrimSpace(ref.Word.ID)] = ref
	}
	options := clipDetectionBoundaryOptionsInput{
		CleanStartWordIDs: make([]clipDetectionBoundaryOptionInput, 0, len(units)),
		CleanEndWordIDs:   make([]clipDetectionBoundaryOptionInput, 0, len(units)),
	}
	seenStarts := map[string]struct{}{}
	seenEnds := map[string]struct{}{}
	for _, unit := range units {
		if unit.StartBoundaryClean {
			wordID := strings.TrimSpace(unit.StartWordID)
			if _, seen := seenStarts[wordID]; wordID != "" && !seen {
				seenStarts[wordID] = struct{}{}
				options.CleanStartWordIDs = append(options.CleanStartWordIDs, clipDetectionBoundaryOptionInput{
					WordID:       wordID,
					SpeechUnitID: strings.TrimSpace(unit.ID),
					TimeSeconds:  unit.StartSeconds,
					Text:         clipDetectionBoundaryWordText(refsByID, wordID),
					Reason:       unit.StartBoundaryReason,
				})
			}
		}
		if unit.EndBoundaryClean {
			wordID := strings.TrimSpace(unit.EndWordID)
			if _, seen := seenEnds[wordID]; wordID != "" && !seen {
				seenEnds[wordID] = struct{}{}
				options.CleanEndWordIDs = append(options.CleanEndWordIDs, clipDetectionBoundaryOptionInput{
					WordID:       wordID,
					SpeechUnitID: strings.TrimSpace(unit.ID),
					TimeSeconds:  unit.EndSeconds,
					Text:         clipDetectionBoundaryWordText(refsByID, wordID),
					Reason:       unit.EndBoundaryReason,
				})
			}
		}
	}
	return options
}

func clipDetectionBoundaryWordText(refsByID map[string]transcriptWordRef, wordID string) string {
	ref, ok := refsByID[wordID]
	if !ok {
		return ""
	}
	return cleanTranscriptText(ref.Word.Text)
}

func alignClipDecisionSegmentToTranscript(segment clipDetectionResponseSegment, refs []transcriptWordRef, refsByID map[string]transcriptWordRef) (ClipDecisionSegment, error) {
	startWordID := strings.TrimSpace(segment.StartWordID)
	endWordID := strings.TrimSpace(segment.EndWordID)
	if startWordID == "" || endWordID == "" {
		return ClipDecisionSegment{}, fmt.Errorf("missing start_word_id or end_word_id")
	}
	startRef, startOK := refsByID[startWordID]
	endRef, endOK := refsByID[endWordID]
	if !startOK || !endOK {
		return ClipDecisionSegment{}, fmt.Errorf("word IDs must reference supplied transcript words")
	}
	if endRef.Index < startRef.Index {
		return ClipDecisionSegment{}, fmt.Errorf("end_word_id must not precede start_word_id")
	}
	words := refs[startRef.Index : endRef.Index+1]
	return ClipDecisionSegment{
		StartWordID:        startWordID,
		EndWordID:          endWordID,
		StartSeconds:       startRef.Word.StartSeconds,
		EndSeconds:         endRef.Word.EndSeconds,
		SpeechStartSeconds: startRef.Word.StartSeconds,
		SpeechEndSeconds:   endRef.Word.EndSeconds,
		Transcript:         joinedTranscriptWords(words),
		BoundaryReason:     normalizedClipBoundaryReason(segment.BoundaryReason),
	}, nil
}

func normalizedClipBoundaryReason(reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" {
		return reason
	}
	return "Selected by the clip detection model."
}

func clipStartBoundaryQuality(refs []transcriptWordRef, index int) (bool, string) {
	if index < 0 || index >= len(refs) {
		return true, "transcript_start"
	}
	if _, weak := weakClipStartWords[normalizedBoundaryWord(refs[index].Word.Text)]; weak {
		return false, "starts_on_weak_boundary_word"
	}
	if index == 0 {
		return true, "transcript_start"
	}
	if clipPreviousWordIsSkippableWeakStarter(refs, index) {
		return true, "after_weak_boundary_starter"
	}
	previous := refs[index-1].Word
	pause := refs[index].Word.StartSeconds - previous.EndSeconds
	if clipWordEndsCleanBoundary(previous.Text) {
		return true, "previous_word_ends_clean_boundary"
	}
	if pause >= clipBoundarySentenceGap {
		return true, fmt.Sprintf("pause_before_%.2fs", pause)
	}
	return false, "continues_previous_sentence"
}

func clipEndBoundaryQuality(refs []transcriptWordRef, index int) (bool, string) {
	if index < 0 || index+1 >= len(refs) {
		return true, "transcript_end"
	}
	word := refs[index].Word
	next := refs[index+1].Word
	pause := next.StartSeconds - word.EndSeconds
	if clipWordEndsCleanBoundary(word.Text) {
		return true, "word_ends_clean_boundary"
	}
	if pause >= clipBoundarySentenceGap {
		return true, fmt.Sprintf("pause_after_%.2fs", pause)
	}
	return false, "continues_next_sentence"
}

func clipWordIsSkippableWeakStarter(refs []transcriptWordRef, index int) bool {
	if index < 0 || index+1 >= len(refs) {
		return false
	}
	if _, weak := weakClipStartWords[normalizedBoundaryWord(refs[index].Word.Text)]; !weak {
		return false
	}
	if index == 0 {
		return true
	}
	previous := refs[index-1].Word
	pause := refs[index].Word.StartSeconds - previous.EndSeconds
	return clipWordEndsCleanBoundary(previous.Text) || pause >= clipBoundarySentenceGap
}

func clipPreviousWordIsSkippableWeakStarter(refs []transcriptWordRef, index int) bool {
	if index <= 0 || index > len(refs) {
		return false
	}
	return clipWordIsSkippableWeakStarter(refs, index-1)
}

func joinedTranscriptWords(words []transcriptWordRef) string {
	parts := make([]string, 0, len(words))
	for _, word := range words {
		if text := cleanTranscriptText(word.Word.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

func normalizedBoundaryWord(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	return value
}

func clipWordEndsCleanBoundary(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	last := lastMeaningfulRune(value)
	switch last {
	case '.', '?', '!', ';', ':':
		return true
	default:
		return false
	}
}

func lastMeaningfulRune(value string) rune {
	runes := []rune(strings.TrimSpace(value))
	for index := len(runes) - 1; index >= 0; index-- {
		r := runes[index]
		if unicode.IsSpace(r) || r == '"' || r == '\'' || r == ')' || r == ']' || r == '}' {
			continue
		}
		return r
	}
	return 0
}

var weakClipStartWords = map[string]struct{}{
	"actually":  {},
	"also":      {},
	"and":       {},
	"anyway":    {},
	"anyways":   {},
	"basically": {},
	"because":   {},
	"but":       {},
	"cause":     {},
	"just":      {},
	"like":      {},
	"literally": {},
	"or":        {},
	"so":        {},
	"then":      {},
	"uh":        {},
	"um":        {},
	"well":      {},
}

func durationSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
