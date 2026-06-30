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
	defaultClipCompositionMaxTokens = 12288
	defaultClipCompositionTimeout   = 2 * time.Minute
	minClipCompositionRepairTokens  = 16384

	clipAspectAuto      = "auto"
	clipAspectLandscape = "16:9"
	clipAspectVertical  = "9:16"

	clipLayoutFullFrame   = "full_frame"
	clipLayoutSingleCrop  = "single_crop"
	clipLayoutStacked     = "stacked_regions"
	clipLayoutFaceOverlay = "content_with_face_overlay"

	clipCaptionRequestAuto = "auto"
	clipCaptionRequestOn   = "on"
	clipCaptionRequestOff  = "off"

	clipCaptionPlanModeBurnedIn = "burned_in"
	clipCaptionPlanModeDisabled = "disabled"

	clipCaptionStyleOpusBold = "opus_bold"
	clipCaptionStyleNone     = "none"

	clipCaptionTimingWord    = "word"
	clipCaptionTimingSegment = "segment"
	clipCaptionTimingNone    = "none"

	clipCaptionAlignLeft   = "left"
	clipCaptionAlignCenter = "center"
	clipCaptionAlignRight  = "right"
	clipCaptionAlignTop    = "top"
	clipCaptionAlignMiddle = "middle"
	clipCaptionAlignBottom = "bottom"

	defaultClipCompositionSourceFrameAspect = 16.0 / 9.0
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
	if _, err := normalizeClipCaptionMode(request.CaptionMode); err != nil {
		return ClipCompositionResult{}, err
	}
	if _, err := captionFontKeysForRequest(request); err != nil {
		return ClipCompositionResult{}, err
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
	if repairErr == nil {
		return repaired, nil
	}
	if clipStructuredResponseWasTruncated(repairResponse, repairErr) {
		return ClipCompositionResult{}, fmt.Errorf("clip composition response was truncated at max_tokens=%d after repair: %w", p.repairMaxTokens(), repairErr)
	}
	return ClipCompositionResult{}, fmt.Errorf("clip composition response failed validation after repair: %w", repairErr)
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
		ResponseFormat: clipCompositionResponseFormat(request),
		Temperature:    0.2,
		MaxTokens:      maxTokens,
	}
}

func clipCompositionRepairMessage(validationErr error, response llm.ChatResponse) string {
	message := strings.TrimSpace(validationErr.Error())
	if clipStructuredResponseWasTruncated(response, validationErr) {
		return "The previous structured JSON response was incomplete or truncated. Return one concise, complete JSON object matching the schema. Validation error: " + message
	}
	parts := []string{
		"The previous structured JSON response was invalid. Return one complete corrected JSON object matching the schema.",
		"Keep the same source-backed clip timing unless the validation error requires changing it.",
		"Include caption_plan when captions are auto or on. Omit switch_decisions unless you need them.",
		"Validation error: " + message,
	}
	if content := strings.TrimSpace(response.Content); content != "" {
		parts = append(parts, "Previous invalid JSON summary: "+clipCompositionInvalidJSONSummary(content))
	}
	return strings.Join(parts, "\n\n")
}

func clipCompositionInvalidJSONSummary(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "empty response"
	}
	const maxSummaryChars = 900
	if len(content) <= maxSummaryChars {
		return content
	}
	return fmt.Sprintf("response length %d chars; prefix: %s", len(content), content[:maxSummaryChars])
}

func clipCompositionSystemPrompt() string {
	return strings.Join([]string{
		"You are Panda's visual composition planner for short-form YouTube clips.",
		"Use the supplied thumbnails, transcript snippets, and metadata to choose the final visual composition.",
		"Find the one or two visible content regions that should be featured in the clip, then choose the layout and rectangles yourself.",
		"Use normalized 0..1000 coordinates. source_rect crops the original video frame; output_rect places that crop on the final canvas.",
		"Return strict JSON matching the schema only. Do not include Markdown or commentary.",
		"Caption planning is required when caption_mode is auto or on, and may be omitted when caption_mode is off.",
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
	availableCaptionFonts := mustCaptionFontKeysForRequest(request)
	sourceFrame := clipCompositionSourceFramePrompt(request)
	payload := map[string]any{
		"video": map[string]any{
			"title":    strings.TrimSpace(request.Title),
			"url":      strings.TrimSpace(request.URL),
			"uploader": strings.TrimSpace(request.Uploader),
		},
		"requested_aspect_ratio":    strings.TrimSpace(request.RequestedAspect),
		"layout_instructions":       strings.TrimSpace(request.LayoutInstructions),
		"caption_mode":              normalizedCaptionModeForPrompt(request.CaptionMode),
		"caption_instructions":      strings.TrimSpace(request.CaptionInstructions),
		"available_caption_fonts":   availableCaptionFonts,
		"clip":                      request.Clip,
		"transcript_timeline":       request.TranscriptTimeline,
		"thumbnails":                thumbnails,
		"source_frame":              sourceFrame,
		"coordinate_system":         "normalized 0..1000; output_rect maps to the final 16:9 or 9:16 canvas",
		"response_guidance":         "Return the smallest complete JSON that renders. One render plan per clip segment is fine unless the visible focus changes during the clip.",
		"caption_plan_guidance":     "For caption_mode auto/on, include caption_plan using the supplied transcript IDs and available_caption_fonts. For word-timed cues, word_ids should contain only the cue's start word ID and end word ID; repeat the same ID twice for a single-word cue. For caption_mode off, caption_plan may be omitted.",
		"optional_switch_decisions": "switch_decisions may be omitted. If included, use same_region or switch_region.",
	}
	data, _ := json.Marshal(payload)
	prompt := "Plan the visual render composition for this clip. Return one complete JSON object only.\n\n" + string(data)
	if strings.TrimSpace(validationError) != "" {
		prompt += "\n\nYour previous response failed validation. Repair the JSON without changing the source-backed clip timing unless required by the error. Validation error: " + validationError
	}
	return prompt
}

func clipCompositionSchema(request ClipCompositionRequest) json.RawMessage {
	required := []string{"aspect_ratio", "layout_mode", "plans", "confidence", "reason"}
	if normalizedCaptionModeForPrompt(request.CaptionMode) != clipCaptionRequestOff {
		required = append(required, "caption_plan")
	}
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
									"role":        map[string]any{"type": "string", "enum": []string{"primary_content", "facecam", "speaker", "source_captions", "full_frame"}},
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
			"switch_decisions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"before_thumbnail_id": map[string]any{"type": "string"},
						"after_thumbnail_id":  map[string]any{"type": "string"},
						"visual_decision": map[string]any{
							"type": "string",
							"enum": []string{"same_region", "switch_region"},
						},
						"confidence": map[string]any{"type": "number"},
						"reason":     map[string]any{"type": "string"},
					},
					"required": []string{"before_thumbnail_id", "after_thumbnail_id", "visual_decision", "confidence", "reason"},
				},
			},
			"caption_plan": clipCaptionPlanSchema(mustCaptionFontKeysForRequest(request)),
			"confidence":   map[string]any{"type": "number"},
			"reason":       map[string]any{"type": "string"},
		},
		"required": required,
	})
	return json.RawMessage(schema)
}

func clipCaptionPlanSchema(fontKeys []string) map[string]any {
	if len(fontKeys) == 0 {
		fontKeys = allClipCaptionFontKeys()
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode": map[string]any{
				"type": "string",
				"enum": []string{clipCaptionPlanModeBurnedIn, clipCaptionPlanModeDisabled},
			},
			"style_preset": map[string]any{
				"type": "string",
				"enum": []string{clipCaptionStyleOpusBold, clipCaptionStyleNone},
			},
			"style_source": map[string]any{
				"type": "string",
				"enum": []string{clipCaptionStyleSourceNone, clipCaptionStyleSourceOpusDefault, clipCaptionStyleSourceUserSpecified, clipCaptionStyleSourceCreativeMix, clipCaptionStyleSourceClipPalette},
			},
			"animation": map[string]any{
				"type": "string",
				"enum": allClipCaptionAnimations(),
			},
			"timing_quality": map[string]any{
				"type": "string",
				"enum": []string{clipCaptionTimingWord, clipCaptionTimingSegment, clipCaptionTimingNone},
			},
			"font_family": map[string]any{
				"type": "string",
				"enum": fontKeys,
			},
			"font_color": map[string]any{
				"type":        "string",
				"description": "Caption text color as a supported named color or #RRGGBB.",
			},
			"highlight_color": map[string]any{
				"type":        "string",
				"description": "Emphasis word color as a supported named color or #RRGGBB.",
			},
			"border_color": map[string]any{
				"type":        "string",
				"description": "Caption outline/border color as a supported named color or #RRGGBB.",
			},
			"border_thickness": map[string]any{
				"type": "string",
				"enum": []string{clipCaptionBorderNone, clipCaptionBorderThin, clipCaptionBorderMedium, clipCaptionBorderThick, clipCaptionBorderExtraThick},
			},
			"background_color": map[string]any{
				"type":        "string",
				"description": "Rectangular caption bubble/background color as transparent, a supported named color, or #RRGGBB.",
			},
			"background_opacity": map[string]any{
				"type":        "number",
				"description": "Caption bubble opacity from 0 to 1. Use 0 when background_color is transparent.",
			},
			"regions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"id":               map[string]any{"type": "string"},
						"output_rect":      clipRectSchema(),
						"horizontal_align": map[string]any{"type": "string", "enum": []string{clipCaptionAlignLeft, clipCaptionAlignCenter, clipCaptionAlignRight}},
						"vertical_align":   map[string]any{"type": "string", "enum": []string{clipCaptionAlignTop, clipCaptionAlignMiddle, clipCaptionAlignBottom}},
						"max_lines":        map[string]any{"type": "integer", "minimum": 1, "maximum": 2},
						"z_index":          map[string]any{"type": "integer"},
					},
					"required": []string{"id", "output_rect", "horizontal_align", "vertical_align", "max_lines", "z_index"},
				},
			},
			"cues": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"caption_region_id": map[string]any{"type": "string"},
						"word_ids": map[string]any{
							"type":        "array",
							"description": "For word timing, use only [start_word_id, end_word_id]. Repeat the same ID twice for one-word cues.",
							"items":       map[string]any{"type": "string"},
						},
						"source_segment_ids": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
						"emphasis_word_ids": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
					},
					"required": []string{"caption_region_id", "word_ids", "source_segment_ids", "emphasis_word_ids"},
				},
			},
			"confidence": map[string]any{"type": "number"},
			"reason":     map[string]any{"type": "string"},
		},
		"required": []string{"mode", "style_preset", "style_source", "animation", "timing_quality", "font_family", "font_color", "highlight_color", "border_color", "border_thickness", "background_color", "background_opacity", "regions", "cues", "confidence", "reason"},
	}
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

func clipCompositionResponseFormat(request ClipCompositionRequest) *llm.ResponseFormat {
	return &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.ResponseFormatSchema{
			Name:   "youtube_clip_composition",
			Strict: true,
			Schema: clipCompositionSchema(request),
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
	if err := validateClipCompositionCoverage(result, request); err != nil {
		return err
	}
	return validateClipCaptionPlan(result, request)
}

func clipCompositionSourceFramePrompt(request ClipCompositionRequest) map[string]any {
	width, height, ok := clipCompositionSourceFrameDimensions(request)
	aspect := clipCompositionSourceFrameAspect(request)
	if ok {
		return map[string]any{
			"width":        width,
			"height":       height,
			"aspect_ratio": aspect,
			"source":       "thumbnail_dimensions",
		}
	}
	return map[string]any{
		"aspect_ratio": aspect,
		"source":       "default_16_9",
	}
}

func clipCompositionSourceFrameAspect(request ClipCompositionRequest) float64 {
	aspect, ok := clipCompositionKnownSourceFrameAspect(request)
	if ok {
		return aspect
	}
	return defaultClipCompositionSourceFrameAspect
}

func clipCompositionKnownSourceFrameAspect(request ClipCompositionRequest) (float64, bool) {
	width, height, ok := clipCompositionSourceFrameDimensions(request)
	if !ok {
		return 0, false
	}
	return float64(width) / float64(height), true
}

func clipCompositionSourceFrameDimensions(request ClipCompositionRequest) (int, int, bool) {
	for _, thumbnail := range request.Thumbnails {
		if thumbnail.Width > 0 && thumbnail.Height > 0 {
			return thumbnail.Width, thumbnail.Height, true
		}
	}
	return 0, 0, false
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
		if err := validateClipCompositionRegions(plan.Regions); err != nil {
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

func validateClipCompositionRegions(regions []ClipRenderRegion) error {
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
	return nil
}

func validClipRegionRole(role string) bool {
	switch role {
	case "primary_content", "facecam", "speaker", "source_captions", "full_frame":
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
