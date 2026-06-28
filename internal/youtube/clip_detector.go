package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/llm"
)

const (
	defaultClipDetectionMaxTokens = 8192
	minClipDetectionRepairTokens  = 8192
	defaultClipDetectionMinClips  = 2
	defaultClipDetectionMaxClips  = 12
	defaultClipDetectionTimeout   = 2 * time.Minute
	clipDetectionMaxSegments      = 6
	clipDetectionInputMaxBytes    = 180000
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
	input := clipDetectionInput{
		Video: clipDetectionVideo{
			Title:           strings.TrimSpace(request.Title),
			URL:             strings.TrimSpace(request.URL),
			Uploader:        strings.TrimSpace(request.Uploader),
			DurationSeconds: request.Duration.Seconds(),
		},
		Instructions: normalizedClipInstructions(request.Instructions),
		Constraints: clipDetectionConstraints{
			MinDurationSeconds: request.MinDurationSeconds,
			MaxDurationSeconds: request.MaxDurationSeconds,
			TargetMinSeconds:   30,
			TargetMaxSeconds:   45,
			MinClips:           defaultClipDetectionMinClips,
			MaxClips:           maxClips,
		},
		TranscriptSegments: indexedClipDetectionTranscriptSegments(request.Segments),
	}
	data, err := json.Marshal(input)
	if err != nil {
		return ClipDetectionResult{}, err
	}
	if len(data) > clipDetectionInputMaxBytes {
		return ClipDetectionResult{}, fmt.Errorf("timestamped transcript is too large for clip detection")
	}
	response, err := d.chat(ctx, d.clipDetectionChatRequest(data, clipDetectionResponseFormat(), "", d.maxTokens))
	if err != nil {
		return ClipDetectionResult{}, err
	}
	result, parseErr := parseAndValidateClipDetection(response.Content, request, maxClips)
	if parseErr == nil {
		return result, nil
	}

	repairResponse, err := d.chat(ctx, d.clipDetectionChatRequest(data, clipDetectionResponseFormat(), clipDetectionRepairMessage(parseErr, response), d.repairMaxTokens()))
	if err != nil {
		return ClipDetectionResult{}, err
	}
	repaired, repairErr := parseAndValidateClipDetection(repairResponse.Content, request, maxClips)
	if repairErr != nil {
		if clipStructuredResponseWasTruncated(repairResponse, repairErr) {
			return ClipDetectionResult{}, fmt.Errorf("clip detection response was truncated at max_tokens=%d after repair: %w", d.repairMaxTokens(), repairErr)
		}
		return ClipDetectionResult{}, fmt.Errorf("clip detection response failed validation after repair: %w", repairErr)
	}
	return repaired, nil
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
	var result ClipDetectionResult
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ClipDetectionResult{}, fmt.Errorf("parse clip detection response: %w", err)
	}
	result, err := alignClipDetectionResultToTranscript(result, request.Segments)
	if err != nil {
		return ClipDetectionResult{}, err
	}
	if err := validateClipDetectionResult(result, request.Duration, durationSeconds(request.MinDurationSeconds), durationSeconds(request.MaxDurationSeconds), maxClips); err != nil {
		return ClipDetectionResult{}, err
	}
	return result, nil
}

func clipDetectionRepairMessage(validationErr error, response llm.ChatResponse) string {
	message := strings.TrimSpace(validationErr.Error())
	if clipStructuredResponseWasTruncated(response, validationErr) {
		return "The previous structured JSON response was incomplete or truncated before the object ended. Regenerate one complete JSON object from scratch, keep every field concise, and ensure every opened object and array is closed. Validation error: " + message
	}
	return "The previous structured JSON response failed validation. Regenerate one complete JSON object from scratch using the same transcript. Do not locally reinterpret the prior answer. A clip with one segment must use type=continuous; a clip with 2-6 segments must use type=spliced; never return type=continuous with multiple segments. Validation error: " + message
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

type clipDetectionInput struct {
	Video              clipDetectionVideo               `json:"video"`
	Instructions       string                           `json:"instructions"`
	Constraints        clipDetectionConstraints         `json:"constraints"`
	TranscriptSegments []clipDetectionTranscriptSegment `json:"transcript_segments"`
}

type clipDetectionTranscriptSegment struct {
	Index        int     `json:"index"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Text         string  `json:"text"`
}

type clipDetectionVideo struct {
	Title           string  `json:"title"`
	URL             string  `json:"url"`
	Uploader        string  `json:"uploader"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type clipDetectionConstraints struct {
	MinDurationSeconds float64 `json:"min_duration_seconds"`
	MaxDurationSeconds float64 `json:"max_duration_seconds"`
	TargetMinSeconds   float64 `json:"target_min_seconds"`
	TargetMaxSeconds   float64 `json:"target_max_seconds"`
	MinClips           int     `json:"min_clips"`
	MaxClips           int     `json:"max_clips"`
}

func clipDetectionSystemPrompt() string {
	return strings.Join([]string{
		"You are Panda's dedicated short-form clip director for YouTube transcripts.",
		"Your job is to return a ranked set of transcript-backed clips with genuine stop-scroll potential, not a summary.",
		"Use the user's instructions as the targeting lens, but include stronger adjacent viral moments when they are clearly better and still relevant.",
		"Return at least 2 distinct viable clips from a normal-length video, and up to the provided max_clips when the transcript supports more strong candidates. Do not cap the default viral preset at 3 clips. A quick soundbite and a fuller story version may both be valid if they serve different posting goals.",
		"Return clips in rank order with rank values 1..N and no gaps.",
		"Transcript segments are atomic cut units. Every returned segment must choose a contiguous inclusive transcript segment range using start_segment_index and end_segment_index.",
		"Set start_seconds exactly to transcript_segments[start_segment_index].start_seconds and end_seconds exactly to transcript_segments[end_segment_index].end_seconds. Do not invent interior timestamps.",
		"Set transcript to the exact source text covered by that inclusive range, lightly joined with spaces only when the range spans multiple transcript segments.",
		"Each clip may be type=continuous with one segment or type=spliced with 2-6 ordered segments. Use spliced clips when cutting dead air, repeated filler, meandering setup, or distant setup/payoff moments creates a more cohesive short-form story.",
		"Clip type must match segment count exactly: continuous means exactly one returned segment; spliced means two to six returned segments. Never label a multi-segment clip as continuous.",
		"Spliced segments must preserve narrative logic. Do not create deceptive edits that reverse meaning. Do not splice unrelated moments just because each moment is individually interesting.",
		"For spliced clips, each segment must have a clear role: hook, setup, escalation, punchline, reveal, reaction, or payoff. Keep only the smallest segments needed for flow.",
		"Keep title, reason, exception_reason, and segment transcript snippets concise so up to 12 clips can fit in one JSON response.",
		"Choose hook-first boundaries: the first 0.5-2 seconds should contain the strongest hook, emotional spike, bold claim, question, contradiction, consequence, funny reaction, or open loop.",
		"Do not start on filler, weak connective words, dangling prepositions, or contextless pronouns. Move the start_segment_index to a clean sentence, clause, quote, question, or reaction start.",
		"End after the thought, punchline, reveal, or exchange lands. Do not end mid-sentence, mid-word, or on weak connective words, prepositions, setup-only clauses, or unresolved references.",
		"Every clip must make sense when watched alone. Include the minimum setup needed for pronouns, stakes, contrast, punchline, or payoff; skip candidates that only work with missing context.",
		"If a transcript segment boundary forces an awkward opening or ending, choose the neighboring segment boundary that makes the phrase complete, or skip that candidate.",
		"Duration policy: target 30-45 seconds by default; use shorter clips only for extreme standalone reactions, memes, quotable soundbites, or user-requested brevity; use longer clips only when setup and payoff truly require it. Always obey the min/max constraints.",
		"Score honestly. No hook caps virality. High-arousal emotion, shareability, retention, debate potential, humor, surprise, charisma, stakes, or useful novelty should drive high scores.",
		"Reject calm explanations, slow context, filler-heavy starts, low-energy moments, and clips that require a long explanation to be interesting.",
		"Use only timestamps supported by the transcript and video duration. Do not invent visual-only details.",
		"Do not include derived timing fields such as total duration; Panda computes clip duration from the returned segments.",
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
									"start_segment_index": map[string]any{
										"type": "integer",
									},
									"end_segment_index": map[string]any{
										"type": "integer",
									},
									"start_seconds": map[string]any{
										"type": "number",
									},
									"end_seconds": map[string]any{
										"type": "number",
									},
									"transcript": map[string]any{
										"type": "string",
									},
								},
								"required": []string{
									"start_segment_index",
									"end_segment_index",
									"start_seconds",
									"end_seconds",
									"transcript",
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

func indexedClipDetectionTranscriptSegments(segments []TranscriptSegment) []clipDetectionTranscriptSegment {
	indexed := make([]clipDetectionTranscriptSegment, 0, len(segments))
	for index, segment := range segments {
		indexed = append(indexed, clipDetectionTranscriptSegment{
			Index:        index,
			StartSeconds: segment.StartSeconds,
			EndSeconds:   segment.EndSeconds,
			Text:         segment.Text,
		})
	}
	return indexed
}

func alignClipDetectionResultToTranscript(result ClipDetectionResult, transcript []TranscriptSegment) (ClipDetectionResult, error) {
	if len(transcript) == 0 {
		return result, fmt.Errorf("timestamped transcript segments are required")
	}
	aligned := ClipDetectionResult{Clips: make([]ClipDecision, 0, len(result.Clips))}
	for clipIndex, clip := range result.Clips {
		alignedClip := clip
		alignedClip.Segments = make([]ClipDecisionSegment, 0, len(clip.Segments))
		for segmentIndex, segment := range clip.Segments {
			alignedSegment, err := alignClipDecisionSegmentToTranscript(segment, transcript)
			if err != nil {
				return ClipDetectionResult{}, fmt.Errorf("clip %d segment %d: %w", clipIndex+1, segmentIndex+1, err)
			}
			alignedClip.Segments = append(alignedClip.Segments, alignedSegment)
		}
		aligned.Clips = append(aligned.Clips, alignedClip)
	}
	return aligned, nil
}

func alignClipDecisionSegmentToTranscript(segment ClipDecisionSegment, transcript []TranscriptSegment) (ClipDecisionSegment, error) {
	if segment.StartSegmentIndex == nil || segment.EndSegmentIndex == nil {
		return ClipDecisionSegment{}, fmt.Errorf("missing transcript segment indexes")
	}
	startIndex := *segment.StartSegmentIndex
	endIndex := *segment.EndSegmentIndex
	if startIndex < 0 || startIndex >= len(transcript) || endIndex < 0 || endIndex >= len(transcript) {
		return ClipDecisionSegment{}, fmt.Errorf("transcript segment indexes are out of range")
	}
	if endIndex < startIndex {
		return ClipDecisionSegment{}, fmt.Errorf("end_segment_index must be greater than or equal to start_segment_index")
	}
	aligned := segment
	aligned.StartSeconds = transcript[startIndex].StartSeconds
	aligned.EndSeconds = transcript[endIndex].EndSeconds
	aligned.Transcript = joinedTranscriptRange(transcript[startIndex : endIndex+1])
	return aligned, nil
}

func joinedTranscriptRange(segments []TranscriptSegment) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if text := cleanTranscriptText(segment.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

func durationSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
