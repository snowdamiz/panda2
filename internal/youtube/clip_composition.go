package youtube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/llm"
)

const (
	defaultClipCompositionMaxTokens = 8192
	defaultClipCompositionTimeout   = 2 * time.Minute
	minClipCompositionRepairTokens  = 8192

	clipAspectAuto      = "auto"
	clipAspectLandscape = "16:9"
	clipAspectVertical  = "9:16"

	clipLayoutFullFrame   = "full_frame"
	clipLayoutSingleCrop  = "single_crop"
	clipLayoutStacked     = "stacked_regions"
	clipLayoutFaceOverlay = "content_with_face_overlay"
)

type OpenRouterClipCompositionPlanner struct {
	client    llm.Client
	model     string
	maxTokens int
	timeout   time.Duration
}

type ClipCompositionPlannerConfig struct {
	Client    llm.Client
	Model     string
	MaxTokens int
	Timeout   time.Duration
}

type clipCompositionChatResult struct {
	response llm.ChatResponse
	err      error
}

func NewOpenRouterClipCompositionPlanner(config ClipCompositionPlannerConfig) *OpenRouterClipCompositionPlanner {
	maxTokens := config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultClipCompositionMaxTokens
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultClipCompositionTimeout
	}
	return &OpenRouterClipCompositionPlanner{
		client:    config.Client,
		model:     strings.TrimSpace(config.Model),
		maxTokens: maxTokens,
		timeout:   timeout,
	}
}

func (p *OpenRouterClipCompositionPlanner) Configured() bool {
	return p != nil && p.client != nil && strings.TrimSpace(p.model) != ""
}

func (p *OpenRouterClipCompositionPlanner) Plan(ctx context.Context, request ClipCompositionRequest) (ClipCompositionResult, error) {
	if !p.Configured() {
		return ClipCompositionResult{}, fmt.Errorf("youtube clip composition model is not configured")
	}
	if len(request.Clip.Segments) == 0 {
		return ClipCompositionResult{}, fmt.Errorf("clip composition requires clip segments")
	}
	if len(request.Thumbnails) == 0 {
		return ClipCompositionResult{}, fmt.Errorf("clip composition requires sampled thumbnails")
	}
	response, err := p.chat(ctx, p.clipCompositionChatRequest(request, "", p.maxTokens))
	if err != nil {
		return ClipCompositionResult{}, err
	}
	result, validationErr := parseAndValidateClipComposition(response.Content, request)
	if validationErr == nil {
		return result, nil
	}

	repairResponse, err := p.chat(ctx, p.clipCompositionChatRequest(request, clipCompositionRepairMessage(validationErr, response), p.repairMaxTokens()))
	if err != nil {
		return ClipCompositionResult{}, err
	}
	repaired, repairErr := parseAndValidateClipComposition(repairResponse.Content, request)
	if repairErr != nil {
		if clipStructuredResponseWasTruncated(repairResponse, repairErr) {
			return ClipCompositionResult{}, fmt.Errorf("clip composition response was truncated at max_tokens=%d after repair: %w", p.repairMaxTokens(), repairErr)
		}
		return ClipCompositionResult{}, fmt.Errorf("clip composition response failed validation after repair: %w", repairErr)
	}
	return repaired, nil
}

func (p *OpenRouterClipCompositionPlanner) repairMaxTokens() int {
	if p.maxTokens >= minClipCompositionRepairTokens {
		return p.maxTokens
	}
	return minClipCompositionRepairTokens
}

func (p *OpenRouterClipCompositionPlanner) chat(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	if p.timeout <= 0 {
		return p.client.Chat(ctx, request)
	}
	chatCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	resultC := make(chan clipCompositionChatResult, 1)
	go func() {
		response, err := p.client.Chat(chatCtx, request)
		resultC <- clipCompositionChatResult{response: response, err: err}
	}()
	select {
	case result := <-resultC:
		return result.response, result.err
	case <-chatCtx.Done():
		if chatCtx.Err() == context.DeadlineExceeded {
			return llm.ChatResponse{}, fmt.Errorf("clip composition model timed out after %s", p.timeout)
		}
		return llm.ChatResponse{}, chatCtx.Err()
	}
}

func (p *OpenRouterClipCompositionPlanner) clipCompositionChatRequest(request ClipCompositionRequest, validationError string, maxTokens int) llm.ChatRequest {
	parts := []llm.ContentPart{{Type: "text", Text: clipCompositionUserPrompt(request, validationError)}}
	for _, thumbnail := range request.Thumbnails {
		if len(thumbnail.Data) == 0 || strings.TrimSpace(thumbnail.MIMEType) == "" {
			continue
		}
		parts = append(parts, llm.ContentPart{
			Type: "image_url",
			ImageURL: &llm.ImageURLPart{
				URL: "data:" + thumbnail.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(thumbnail.Data),
			},
		})
	}
	return llm.ChatRequest{
		Model: p.model,
		Messages: []llm.Message{
			{Role: "system", Content: clipCompositionSystemPrompt()},
			{Role: "user", ContentParts: parts},
		},
		ResponseFormat: clipCompositionResponseFormat(),
		Temperature:    0.2,
		MaxTokens:      maxTokens,
	}
}

func clipCompositionRepairMessage(validationErr error, response llm.ChatResponse) string {
	message := strings.TrimSpace(validationErr.Error())
	if clipStructuredResponseWasTruncated(response, validationErr) {
		return "The previous structured JSON response was incomplete or truncated before the object ended. Regenerate one complete JSON object from scratch, keep it concise, and ensure every opened object and array is closed. Validation error: " + message
	}
	return message
}

func clipCompositionSystemPrompt() string {
	return strings.Join([]string{
		"You are Panda's visual composition planner for short-form YouTube clips.",
		"Use only the supplied thumbnails, transcript snippets, and metadata.",
		"Return strict JSON matching the schema. Do not include Markdown or commentary.",
		"Choose watchable render composition, not a summary.",
		"For 9:16, identify whether primary content, facecam, speaker, captions, or full-frame source should be cropped, stacked, or overlaid.",
		"For streamer layouts, preserve both primary_content and facecam when that is more watchable than a single crop.",
		"Use normalized coordinates from 0 to 1000. source_rect crops the original video frame; output_rect places that crop on the final canvas.",
		"For stacked_regions, output rectangles must tile the whole 1000x1000 output coordinate space with no gaps or overlaps.",
		"Preserve important UI, captions, speaker faces, and context needed to understand the clip.",
		"Do not invent regions that are not visible in the thumbnails.",
		"For split-screen interviews, panels, debates, podcasts, and reaction layouts, identify which visible person or region should be emphasized for each time range.",
		"When the active speaker or most important visual focus switches mid-clip, return multiple plans for the same clip segment with contiguous source_start_seconds/source_end_seconds ranges and different source_rect choices.",
		"Use thumbnails marked possible_speaker_switch_before and possible_speaker_switch_after to decide whether the crop should switch at that transcript boundary.",
		"If the visible layout changes by source segment or time range, return separate plans covering those exact ranges.",
		"Use fit=cover when the crop should fill its output rectangle, and fit=contain when preserving the whole crop is more important than filling.",
		"Never return ffmpeg filters.",
	}, " ")
}

func clipCompositionUserPrompt(request ClipCompositionRequest, validationError string) string {
	type thumbnailPrompt struct {
		ID                  string  `json:"id"`
		SourceSeconds       float64 `json:"source_seconds"`
		ClipSegmentIndex    int     `json:"clip_segment_index"`
		ClipOffsetSeconds   float64 `json:"clip_offset_seconds"`
		SampleReason        string  `json:"sample_reason"`
		Width               int     `json:"width"`
		Height              int     `json:"height"`
		TranscriptNearFrame string  `json:"transcript_near_frame"`
	}
	thumbnails := make([]thumbnailPrompt, 0, len(request.Thumbnails))
	for _, thumbnail := range request.Thumbnails {
		thumbnails = append(thumbnails, thumbnailPrompt{
			ID:                  thumbnail.ID,
			SourceSeconds:       thumbnail.SourceSeconds,
			ClipSegmentIndex:    thumbnail.ClipSegmentIndex,
			ClipOffsetSeconds:   thumbnail.ClipOffsetSeconds,
			SampleReason:        thumbnail.SampleReason,
			Width:               thumbnail.Width,
			Height:              thumbnail.Height,
			TranscriptNearFrame: thumbnail.TranscriptNearFrame,
		})
	}
	payload := map[string]any{
		"video": map[string]any{
			"title":    strings.TrimSpace(request.Title),
			"url":      strings.TrimSpace(request.URL),
			"uploader": strings.TrimSpace(request.Uploader),
		},
		"requested_aspect_ratio": strings.TrimSpace(request.RequestedAspect),
		"layout_instructions":    strings.TrimSpace(request.LayoutInstructions),
		"clip":                   request.Clip,
		"transcript_timeline":    request.TranscriptTimeline,
		"thumbnails":             thumbnails,
		"coordinate_system":      "normalized 0..1000; output_rect maps to the final 16:9 or 9:16 canvas",
		"dynamic_regions":        "Return multiple contiguous plans inside the same clip segment when the crop/layout should switch mid-clip, such as speaker A on the left then speaker B on the right.",
	}
	data, _ := json.Marshal(payload)
	prompt := "Plan the visual render composition for this clip. Return one complete JSON object only.\n\n" + string(data)
	if strings.TrimSpace(validationError) != "" {
		prompt += "\n\nYour previous response failed validation. Repair the JSON without changing the source-backed clip timing unless required by the error. Validation error: " + validationError
	}
	return prompt
}

func clipCompositionSchema() json.RawMessage {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"aspect_ratio": map[string]any{
				"type": "string",
				"enum": []string{clipAspectLandscape, clipAspectVertical},
			},
			"layout_mode": map[string]any{
				"type": "string",
				"enum": []string{clipLayoutFullFrame, clipLayoutSingleCrop, clipLayoutStacked, clipLayoutFaceOverlay},
			},
			"plans": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"applies_to_segment_index": map[string]any{"type": "integer"},
						"source_start_seconds":     map[string]any{"type": "number"},
						"source_end_seconds":       map[string]any{"type": "number"},
						"regions": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": false,
								"properties": map[string]any{
									"role":        map[string]any{"type": "string", "enum": []string{"primary_content", "facecam", "speaker", "captions", "full_frame"}},
									"source_rect": clipRectSchema(),
									"output_rect": clipRectSchema(),
									"fit":         map[string]any{"type": "string", "enum": []string{"cover", "contain"}},
									"z_index":     map[string]any{"type": "integer"},
								},
								"required": []string{"role", "source_rect", "output_rect", "fit", "z_index"},
							},
						},
					},
					"required": []string{"applies_to_segment_index", "source_start_seconds", "source_end_seconds", "regions"},
				},
			},
			"confidence": map[string]any{"type": "number"},
			"reason":     map[string]any{"type": "string"},
		},
		"required": []string{"aspect_ratio", "layout_mode", "plans", "confidence", "reason"},
	})
	return json.RawMessage(schema)
}

func clipRectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"x": map[string]any{"type": "integer"},
			"y": map[string]any{"type": "integer"},
			"w": map[string]any{"type": "integer"},
			"h": map[string]any{"type": "integer"},
		},
		"required": []string{"x", "y", "w", "h"},
	}
}

func clipCompositionResponseFormat() *llm.ResponseFormat {
	return &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.ResponseFormatSchema{
			Name:   "youtube_clip_composition",
			Strict: true,
			Schema: clipCompositionSchema(),
		},
	}
}

func parseAndValidateClipComposition(content string, request ClipCompositionRequest) (ClipCompositionResult, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return ClipCompositionResult{}, fmt.Errorf("clip composition model returned an empty response")
	}
	var result ClipCompositionResult
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ClipCompositionResult{}, fmt.Errorf("parse clip composition response: %w", err)
	}
	if err := ValidateClipCompositionResult(result, request); err != nil {
		return ClipCompositionResult{}, err
	}
	return result, nil
}

func ValidateClipCompositionResult(result ClipCompositionResult, request ClipCompositionRequest) error {
	requestedAspect, err := normalizeClipAspectRatio(request.RequestedAspect)
	if err != nil {
		return err
	}
	aspect := strings.TrimSpace(result.AspectRatio)
	if aspect != clipAspectLandscape && aspect != clipAspectVertical {
		return fmt.Errorf("clip composition aspect_ratio must be 16:9 or 9:16")
	}
	if requestedAspect != clipAspectAuto && aspect != requestedAspect {
		return fmt.Errorf("clip composition aspect_ratio %s does not match requested %s", aspect, requestedAspect)
	}
	layout := strings.TrimSpace(result.LayoutMode)
	switch layout {
	case clipLayoutFullFrame, clipLayoutSingleCrop, clipLayoutStacked, clipLayoutFaceOverlay:
	default:
		return fmt.Errorf("clip composition layout_mode %q is not supported", result.LayoutMode)
	}
	if result.Confidence < 0 || result.Confidence > 1 || math.IsNaN(result.Confidence) || math.IsInf(result.Confidence, 0) {
		return fmt.Errorf("clip composition confidence must be between 0 and 1")
	}
	if strings.TrimSpace(result.Reason) == "" {
		return fmt.Errorf("clip composition reason is required")
	}
	if len(result.Plans) == 0 {
		return fmt.Errorf("clip composition returned no render plans")
	}
	return validateClipCompositionCoverage(result, request)
}

func validateClipCompositionCoverage(result ClipCompositionResult, request ClipCompositionRequest) error {
	plansBySegment := map[int][]ClipFrameRenderPlan{}
	for _, plan := range result.Plans {
		if plan.AppliesToSegmentIndex < 0 || plan.AppliesToSegmentIndex >= len(request.Clip.Segments) {
			return fmt.Errorf("clip composition plan references missing segment index %d", plan.AppliesToSegmentIndex)
		}
		if plan.SourceEndSeconds <= plan.SourceStartSeconds {
			return fmt.Errorf("clip composition plan end must be after start")
		}
		segment := request.Clip.Segments[plan.AppliesToSegmentIndex]
		if plan.SourceStartSeconds < segment.StartSeconds-0.05 || plan.SourceEndSeconds > segment.EndSeconds+0.05 {
			return fmt.Errorf("clip composition plan %.3f-%.3f is outside segment %d %.3f-%.3f", plan.SourceStartSeconds, plan.SourceEndSeconds, plan.AppliesToSegmentIndex, segment.StartSeconds, segment.EndSeconds)
		}
		if err := validateClipCompositionRegions(result.LayoutMode, plan.Regions); err != nil {
			return fmt.Errorf("clip composition segment %d: %w", plan.AppliesToSegmentIndex, err)
		}
		plansBySegment[plan.AppliesToSegmentIndex] = append(plansBySegment[plan.AppliesToSegmentIndex], plan)
	}
	for index, segment := range request.Clip.Segments {
		plans := plansBySegment[index]
		if len(plans) == 0 {
			return fmt.Errorf("clip composition missing render plan for segment %d", index)
		}
		sort.Slice(plans, func(i, j int) bool {
			return plans[i].SourceStartSeconds < plans[j].SourceStartSeconds
		})
		cursor := segment.StartSeconds
		for _, plan := range plans {
			if math.Abs(plan.SourceStartSeconds-cursor) > 0.05 {
				return fmt.Errorf("clip composition plans for segment %d leave a gap near %.3f", index, cursor)
			}
			cursor = plan.SourceEndSeconds
		}
		if math.Abs(cursor-segment.EndSeconds) > 0.05 {
			return fmt.Errorf("clip composition plans for segment %d do not cover the segment end %.3f", index, segment.EndSeconds)
		}
	}
	return nil
}

func validateClipCompositionRegions(layout string, regions []ClipRenderRegion) error {
	if len(regions) == 0 {
		return fmt.Errorf("render plan has no regions")
	}
	if len(regions) > 4 {
		return fmt.Errorf("render plan has too many regions")
	}
	for _, region := range regions {
		if !validClipRegionRole(region.Role) {
			return fmt.Errorf("region role %q is not supported", region.Role)
		}
		if region.Fit != "cover" && region.Fit != "contain" {
			return fmt.Errorf("region fit %q is not supported", region.Fit)
		}
		if err := validateClipRect("source_rect", region.SourceRect); err != nil {
			return err
		}
		if err := validateClipRect("output_rect", region.OutputRect); err != nil {
			return err
		}
	}
	switch layout {
	case clipLayoutFullFrame:
		if len(regions) != 1 || regions[0].Role != "full_frame" || !clipRectIsFull(regions[0].SourceRect) || !clipRectIsFull(regions[0].OutputRect) {
			return fmt.Errorf("full_frame layout requires one full_frame region covering source and output")
		}
	case clipLayoutSingleCrop:
		if len(regions) != 1 || !clipRectIsFull(regions[0].OutputRect) {
			return fmt.Errorf("single_crop layout requires one region filling the output")
		}
	case clipLayoutStacked:
		if len(regions) < 2 || len(regions) > 3 {
			return fmt.Errorf("stacked_regions layout requires two or three regions")
		}
		if err := validateStackedOutputRects(regions); err != nil {
			return err
		}
	case clipLayoutFaceOverlay:
		if err := validateOverlayOutputRects(regions); err != nil {
			return err
		}
	}
	return nil
}

func validClipRegionRole(role string) bool {
	switch role {
	case "primary_content", "facecam", "speaker", "captions", "full_frame":
		return true
	default:
		return false
	}
}

func validateClipRect(name string, rect ClipRect) error {
	if rect.X < 0 || rect.Y < 0 || rect.W < 10 || rect.H < 10 || rect.X+rect.W > 1000 || rect.Y+rect.H > 1000 {
		return fmt.Errorf("%s must stay within 0..1000 and be at least 10x10", name)
	}
	return nil
}

func clipRectIsFull(rect ClipRect) bool {
	return rect.X == 0 && rect.Y == 0 && rect.W == 1000 && rect.H == 1000
}

func validateStackedOutputRects(regions []ClipRenderRegion) error {
	sorted := append([]ClipRenderRegion(nil), regions...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].OutputRect.Y < sorted[j].OutputRect.Y
	})
	cursor := 0
	hasPrimary := false
	for _, region := range sorted {
		if region.Role == "primary_content" {
			hasPrimary = true
		}
		rect := region.OutputRect
		if rect.X != 0 || rect.W != 1000 {
			return fmt.Errorf("stacked_regions output rectangles must fill the full output width")
		}
		if rect.Y != cursor {
			return fmt.Errorf("stacked_regions output rectangles must touch without gaps")
		}
		cursor += rect.H
	}
	if cursor != 1000 {
		return fmt.Errorf("stacked_regions output rectangles must tile the full output height")
	}
	if !hasPrimary {
		return fmt.Errorf("stacked_regions layout requires a primary_content region")
	}
	return nil
}

func validateOverlayOutputRects(regions []ClipRenderRegion) error {
	primaryZ := 0
	hasPrimary := false
	for _, region := range regions {
		if region.Role == "primary_content" && clipRectIsFull(region.OutputRect) {
			primaryZ = region.ZIndex
			hasPrimary = true
			break
		}
	}
	if !hasPrimary {
		return fmt.Errorf("content_with_face_overlay requires primary_content filling the output")
	}
	for _, region := range regions {
		if region.Role != "primary_content" && region.ZIndex <= primaryZ {
			return fmt.Errorf("overlay regions must have higher z_index than primary_content")
		}
	}
	return nil
}

func normalizeClipAspectRatio(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return clipAspectAuto, nil
	}
	switch value {
	case clipAspectAuto, clipAspectLandscape, clipAspectVertical:
		return value, nil
	default:
		return "", fmt.Errorf("aspect_ratio must be auto, 16:9, or 9:16")
	}
}
