package youtube

import (
	"context"
	"strings"
	"testing"

	"github.com/sn0w/panda2/internal/llm"
)

const testDisabledCaptionPlanJSON = `"caption_plan":{"mode":"disabled","style_preset":"none","style_source":"none","timing_quality":"none","font_family":"default","font_color":"white","highlight_color":"yellow","border_color":"black","border_thickness":"thick","background_color":"transparent","background_opacity":0,"regions":[],"cues":[],"confidence":1,"reason":"Captions were explicitly disabled for this test."}`

func TestOpenRouterClipCompositionPlannerRepairsInvalidPlan(t *testing.T) {
	client := &fakeClipLLM{
		responses: []llm.ChatResponse{
			{Content: `{"aspect_ratio":"16:9","layout_mode":"single_crop","plans":[{"applies_to_segment_index":0,"source_start_seconds":10,"source_end_seconds":40,"regions":[{"role":"primary_content","source_rect":{"x":0,"y":0,"w":1000,"h":1000},"output_rect":{"x":0,"y":0,"w":1000,"h":1000},"fit":"cover","z_index":0}]}],` + testDisabledCaptionPlanJSON + `,"confidence":0.7,"reason":"Bad aspect."}`},
			{Content: `{"aspect_ratio":"9:16","layout_mode":"stacked_regions","plans":[{"applies_to_segment_index":0,"source_start_seconds":10,"source_end_seconds":40,"regions":[{"role":"primary_content","source_rect":{"x":0,"y":0,"w":1000,"h":700},"output_rect":{"x":0,"y":0,"w":1000,"h":700},"fit":"cover","z_index":0},{"role":"facecam","source_rect":{"x":700,"y":700,"w":300,"h":300},"output_rect":{"x":0,"y":700,"w":1000,"h":300},"fit":"cover","z_index":1}]}],` + testDisabledCaptionPlanJSON + `,"confidence":0.9,"reason":"Stack primary content above facecam for vertical viewing."}`},
		},
	}
	planner := NewOpenRouterClipCompositionPlanner(ClipCompositionPlannerConfig{
		Client: client,
		Model:  "google/gemini-3.5-flash",
	})
	result, err := planner.Plan(context.Background(), ClipCompositionRequest{
		Title:           "Deep Dive",
		URL:             "https://www.youtube.com/watch?v=deep",
		Uploader:        "Teacher",
		RequestedAspect: "9:16",
		CaptionMode:     clipCaptionRequestOff,
		Clip: ClipDecision{
			Rank:  1,
			Title: "Best Moment",
			Type:  "continuous",
			Segments: []ClipDecisionSegment{{
				StartSeconds: 10,
				EndSeconds:   40,
				Transcript:   "The best moment happens here.",
			}},
			Reason: "Strong moment.",
		},
		Thumbnails: []ClipThumbnail{{
			ID:                  "thumb_01",
			SourceSeconds:       11,
			ClipSegmentIndex:    0,
			ClipOffsetSeconds:   1,
			SampleReason:        "possible_speaker_switch_after",
			Width:               640,
			Height:              360,
			MIMEType:            "image/jpeg",
			Data:                []byte("jpeg bytes"),
			TranscriptNearFrame: "The best moment happens here.",
		}},
		TranscriptTimeline: []ClipCompositionTranscriptSegment{{
			ClipSegmentIndex: 0,
			StartSeconds:     10,
			EndSeconds:       20,
			Text:             "Speaker A introduces the point.",
		}, {
			ClipSegmentIndex: 0,
			StartSeconds:     20,
			EndSeconds:       40,
			Text:             "Speaker B responds.",
		}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if result.AspectRatio != "9:16" || result.LayoutMode != "stacked_regions" || len(result.Plans) != 1 {
		t.Fatalf("unexpected repaired result: %+v", result)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected one repair retry, got %d requests", len(client.requests))
	}
	first := client.requests[0]
	if first.ResponseFormat == nil || first.ResponseFormat.Type != "json_schema" || first.ResponseFormat.JSONSchema == nil || !first.ResponseFormat.JSONSchema.Strict {
		t.Fatalf("expected strict structured output request, got %+v", first.ResponseFormat)
	}
	if len(first.Messages[1].ContentParts) != 2 || first.Messages[1].ContentParts[1].ImageURL == nil || !strings.HasPrefix(first.Messages[1].ContentParts[1].ImageURL.URL, "data:image/jpeg;base64,") {
		t.Fatalf("expected thumbnail image content part, got %+v", first.Messages[1].ContentParts)
	}
	userPrompt := first.Messages[1].ContentParts[0].Text
	for _, want := range []string{`"sample_reason":"possible_speaker_switch_after"`, `"transcript_timeline"`, `"dynamic_regions"`} {
		if !strings.Contains(userPrompt, want) {
			t.Fatalf("expected composition prompt to contain %s, got %s", want, userPrompt)
		}
	}
	if !strings.Contains(client.requests[1].Messages[1].ContentParts[0].Text, "previous response failed validation") {
		t.Fatalf("expected repair prompt to include validation error, got %s", client.requests[1].Messages[1].ContentParts[0].Text)
	}
}

func TestOpenRouterClipCompositionPlannerExpandsRepairBudgetForTruncatedJSON(t *testing.T) {
	client := &fakeClipLLM{
		responses: []llm.ChatResponse{
			{Content: `{"aspect_ratio":"9:16","layout_mode":"single_crop"`, FinishReason: "length"},
			{Content: `{"aspect_ratio":"9:16","layout_mode":"single_crop","plans":[{"applies_to_segment_index":0,"source_start_seconds":10,"source_end_seconds":40,"regions":[{"role":"speaker","source_rect":{"x":0,"y":0,"w":500,"h":1000},"output_rect":{"x":0,"y":0,"w":1000,"h":1000},"fit":"cover","z_index":0}]}],` + testDisabledCaptionPlanJSON + `,"confidence":0.92,"reason":"Crop the active speaker for a vertical clip."}`},
		},
	}
	planner := NewOpenRouterClipCompositionPlanner(ClipCompositionPlannerConfig{
		Client:    client,
		Model:     "google/gemini-3.5-flash",
		MaxTokens: 4096,
	})

	result, err := planner.Plan(context.Background(), ClipCompositionRequest{
		Title:           "Split Screen",
		URL:             "https://www.youtube.com/watch?v=split",
		Uploader:        "Panel",
		RequestedAspect: "9:16",
		CaptionMode:     clipCaptionRequestOff,
		Clip: ClipDecision{
			Rank:  1,
			Title: "Speaker Switch",
			Type:  "continuous",
			Segments: []ClipDecisionSegment{{
				StartSeconds: 10,
				EndSeconds:   40,
				Transcript:   "Left speaker talks, then right speaker answers.",
			}},
		},
		Thumbnails: []ClipThumbnail{{
			ID:                "thumb_01",
			SourceSeconds:     20,
			ClipSegmentIndex:  0,
			ClipOffsetSeconds: 10,
			SampleReason:      "possible_speaker_switch_after",
			Width:             640,
			Height:            360,
			MIMEType:          "image/jpeg",
			Data:              []byte("jpeg bytes"),
		}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if result.LayoutMode != "single_crop" || result.Plans[0].Regions[0].Role != "speaker" {
		t.Fatalf("unexpected repaired result: %+v", result)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected one repair retry, got %d requests", len(client.requests))
	}
	if client.requests[0].MaxTokens != 4096 || client.requests[1].MaxTokens != minClipCompositionRepairTokens {
		t.Fatalf("expected expanded repair budget, got first=%d repair=%d", client.requests[0].MaxTokens, client.requests[1].MaxTokens)
	}
	repairPrompt := client.requests[1].Messages[1].ContentParts[0].Text
	if !strings.Contains(repairPrompt, "incomplete or truncated") {
		t.Fatalf("expected repair prompt to call out truncation, got %s", repairPrompt)
	}
}

func TestValidateClipCompositionRejectsUntiledStackedRegions(t *testing.T) {
	err := ValidateClipCompositionResult(ClipCompositionResult{
		AspectRatio: "9:16",
		LayoutMode:  "stacked_regions",
		Confidence:  0.8,
		Reason:      "Bad stack.",
		CaptionPlan: disabledTestCaptionPlan(),
		Plans: []ClipFrameRenderPlan{{
			AppliesToSegmentIndex: 0,
			SourceStartSeconds:    10,
			SourceEndSeconds:      40,
			Regions: []ClipRenderRegion{
				{Role: "primary_content", SourceRect: ClipRect{X: 0, Y: 0, W: 1000, H: 700}, OutputRect: ClipRect{X: 0, Y: 0, W: 1000, H: 650}, Fit: "cover"},
				{Role: "facecam", SourceRect: ClipRect{X: 700, Y: 700, W: 300, H: 300}, OutputRect: ClipRect{X: 0, Y: 650, W: 1000, H: 300}, Fit: "cover", ZIndex: 1},
			},
		}},
	}, ClipCompositionRequest{
		RequestedAspect: "9:16",
		CaptionMode:     clipCaptionRequestOff,
		Clip: ClipDecision{Segments: []ClipDecisionSegment{{
			StartSeconds: 10,
			EndSeconds:   40,
			Transcript:   "Clip transcript.",
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), "tile the full output height") {
		t.Fatalf("expected stacked tiling error, got %v", err)
	}
}

func TestValidateClipCompositionAllowsDynamicSingleCropWithinSegment(t *testing.T) {
	err := ValidateClipCompositionResult(ClipCompositionResult{
		AspectRatio: "9:16",
		LayoutMode:  "single_crop",
		Confidence:  0.86,
		Reason:      "Switch crop from left speaker to right speaker at the speaker handoff.",
		CaptionPlan: disabledTestCaptionPlan(),
		Plans: []ClipFrameRenderPlan{
			{
				AppliesToSegmentIndex: 0,
				SourceStartSeconds:    10,
				SourceEndSeconds:      20,
				Regions: []ClipRenderRegion{{
					Role:       "speaker",
					SourceRect: ClipRect{X: 0, Y: 0, W: 500, H: 1000},
					OutputRect: ClipRect{X: 0, Y: 0, W: 1000, H: 1000},
					Fit:        "cover",
				}},
			},
			{
				AppliesToSegmentIndex: 0,
				SourceStartSeconds:    20,
				SourceEndSeconds:      40,
				Regions: []ClipRenderRegion{{
					Role:       "speaker",
					SourceRect: ClipRect{X: 500, Y: 0, W: 500, H: 1000},
					OutputRect: ClipRect{X: 0, Y: 0, W: 1000, H: 1000},
					Fit:        "cover",
				}},
			},
		},
	}, ClipCompositionRequest{
		RequestedAspect: "9:16",
		CaptionMode:     clipCaptionRequestOff,
		Clip: ClipDecision{Segments: []ClipDecisionSegment{{
			StartSeconds: 10,
			EndSeconds:   40,
			Transcript:   "Left speaker then right speaker.",
		}}},
	})
	if err != nil {
		t.Fatalf("expected dynamic single-crop plans to validate, got %v", err)
	}
}

func TestValidateClipCompositionAcceptsWordBackedCaptionPlan(t *testing.T) {
	request := captionTestCompositionRequest()
	result := captionTestCompositionResult()

	if err := ValidateClipCompositionResult(result, request); err != nil {
		t.Fatalf("expected word-backed caption plan to validate, got %v", err)
	}
}

func TestValidateClipCompositionAcceptsShortTwoLineCaptionAnchor(t *testing.T) {
	request := captionTestCompositionRequest()
	result := captionTestCompositionResult()
	result.CaptionPlan.Regions[0].ID = "global_captions"
	result.CaptionPlan.Regions[0].OutputRect.H = 80
	result.CaptionPlan.Regions[0].MaxLines = 2
	result.CaptionPlan.Cues[0].CaptionRegionID = "global_captions"

	if err := ValidateClipCompositionResult(result, request); err != nil {
		t.Fatalf("expected short two-line caption anchor to validate, got %v", err)
	}
}

func TestValidateClipCompositionAcceptsDenseCaptionCueForRendererWrapping(t *testing.T) {
	request := captionTestCompositionRequest()
	text := "This caption plan is readable even when model stretches"
	request.Clip.Segments[0].Transcript = text
	request.TranscriptTimeline[0].Text = text
	request.TranscriptTimeline[0].Words = []TranscriptWord{
		{ID: "w_0001", StartSeconds: 10.00, EndSeconds: 10.20, Text: "This"},
		{ID: "w_0002", StartSeconds: 10.21, EndSeconds: 10.45, Text: "caption"},
		{ID: "w_0003", StartSeconds: 10.46, EndSeconds: 10.75, Text: "plan"},
		{ID: "w_0004", StartSeconds: 10.76, EndSeconds: 10.95, Text: "is"},
		{ID: "w_0005", StartSeconds: 10.96, EndSeconds: 11.30, Text: "readable"},
		{ID: "w_0006", StartSeconds: 11.31, EndSeconds: 11.60, Text: "even"},
		{ID: "w_0007", StartSeconds: 11.61, EndSeconds: 11.90, Text: "when"},
		{ID: "w_0008", StartSeconds: 11.91, EndSeconds: 12.20, Text: "model"},
		{ID: "w_0009", StartSeconds: 12.21, EndSeconds: 12.55, Text: "stretches"},
	}
	result := captionTestCompositionResult()
	result.CaptionPlan.Regions[0].ID = "global_captions"
	result.CaptionPlan.Cues[0].CaptionRegionID = "global_captions"
	result.CaptionPlan.Cues[0].WordIDs = []string{"w_0001", "w_0009"}
	result.CaptionPlan.Cues[0].EmphasisWordIDs = []string{"w_0002"}

	if err := ValidateClipCompositionResult(result, request); err != nil {
		t.Fatalf("expected dense cue to remain renderable instead of failing composition, got %v", err)
	}
}

func TestClipCompositionSchemaUsesCompactCaptionCues(t *testing.T) {
	schema := string(clipCompositionSchema())
	for _, want := range []string{`"word_ids"`, `"source_segment_ids"`, `"emphasis_word_ids"`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("expected schema to contain %s, got %s", want, schema)
		}
	}
	for _, legacy := range []string{`"start_word_id"`, `"end_word_id"`} {
		if strings.Contains(schema, legacy) {
			t.Fatalf("schema still contains legacy cue field %s: %s", legacy, schema)
		}
	}
}

func TestValidateClipCompositionAcceptsRequestedCaptionStyle(t *testing.T) {
	request := captionTestCompositionRequest()
	request.CaptionInstructions = "use white Inter font with green medium outline"
	result := captionTestCompositionResult()
	result.CaptionPlan.StyleSource = clipCaptionStyleSourceUserSpecified
	result.CaptionPlan.FontFamily = clipCaptionFontInter
	result.CaptionPlan.FontColor = "white"
	result.CaptionPlan.BorderColor = "green"
	result.CaptionPlan.BorderThickness = clipCaptionBorderMedium

	if err := ValidateClipCompositionResult(result, request); err != nil {
		t.Fatalf("expected requested caption style to validate, got %v", err)
	}
}

func TestValidateClipCompositionRequiresOpusStyleWhenCaptionStyleUnspecified(t *testing.T) {
	request := captionTestCompositionRequest()
	result := captionTestCompositionResult()
	result.CaptionPlan.StyleSource = clipCaptionStyleSourceCreativeMix
	result.CaptionPlan.BorderColor = "green"

	err := ValidateClipCompositionResult(result, request)
	if err == nil || !strings.Contains(err.Error(), "style_source must be opus_default") {
		t.Fatalf("expected opus default style requirement, got %v", err)
	}
}

func TestValidateClipCompositionAcceptsCreativeAndClipPaletteCaptionStyles(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		captionInstructions  string
		styleSource          string
		borderColor          string
		highlightColor       string
		backgroundColor      string
		backgroundOpacity    float64
		expectedStyleSnippet string
	}{
		{
			name:                 "creative mix",
			captionInstructions:  "mix up the caption style and make it colorful",
			styleSource:          clipCaptionStyleSourceCreativeMix,
			borderColor:          "purple",
			highlightColor:       "orange",
			backgroundColor:      "transparent",
			backgroundOpacity:    0,
			expectedStyleSnippet: clipCaptionStyleSourceCreativeMix,
		},
		{
			name:                 "clip palette",
			captionInstructions:  "choose caption colors based on the colors in the clip",
			styleSource:          clipCaptionStyleSourceClipPalette,
			borderColor:          "#14b8a6",
			highlightColor:       "#facc15",
			backgroundColor:      "black",
			backgroundOpacity:    0.55,
			expectedStyleSnippet: clipCaptionStyleSourceClipPalette,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := captionTestCompositionRequest()
			request.CaptionInstructions = tc.captionInstructions
			result := captionTestCompositionResult()
			result.CaptionPlan.StyleSource = tc.styleSource
			result.CaptionPlan.BorderColor = tc.borderColor
			result.CaptionPlan.HighlightColor = tc.highlightColor
			result.CaptionPlan.BackgroundColor = tc.backgroundColor
			result.CaptionPlan.BackgroundOpacity = tc.backgroundOpacity

			if err := ValidateClipCompositionResult(result, request); err != nil {
				t.Fatalf("expected %s caption style to validate, got %v", tc.expectedStyleSnippet, err)
			}
		})
	}
}

func TestValidateClipCompositionRejectsMissingCaptionWordReference(t *testing.T) {
	request := captionTestCompositionRequest()
	result := captionTestCompositionResult()
	result.CaptionPlan.Cues[0].WordIDs[1] = "w_9999"

	err := ValidateClipCompositionResult(result, request)
	if err == nil || !strings.Contains(err.Error(), "word-timed cue references missing word IDs") {
		t.Fatalf("expected missing word reference error, got %v", err)
	}
}

func TestValidateClipCompositionRejectsInvalidCaptionStyle(t *testing.T) {
	request := captionTestCompositionRequest()
	result := captionTestCompositionResult()
	result.CaptionPlan.BorderThickness = "chunky"

	err := ValidateClipCompositionResult(result, request)
	if err == nil || !strings.Contains(err.Error(), "caption_plan border_thickness") {
		t.Fatalf("expected invalid caption style error, got %v", err)
	}
}

func TestBuildClipASSSubtitlePositionsAndHighlightsWords(t *testing.T) {
	request := captionTestCompositionRequest()
	result := captionTestCompositionResult()
	refs := newClipCaptionReferences(request.TranscriptTimeline)
	style := clipCaptionASSStyle{
		Font:            clipCaptionResolvedFont{Key: clipCaptionFontInter, Family: "Inter", Path: "/fonts/Inter.ttf"},
		PrimaryColor:    "&H00FFFFFF",
		HighlightColor:  "&H0000E6FF",
		BorderColor:     "&H0000FF00",
		BackColor:       "&H80000000",
		BorderStyle:     1,
		BorderThickness: captionBorderSize(ClipResolution{Width: 1080, Height: 1920}, clipCaptionBorderMedium),
		ShadowSize:      captionShadowSize(ClipResolution{Width: 1080, Height: 1920}),
	}

	ass, err := buildClipASSSubtitle(*result.CaptionPlan, result.Plans[0], ClipResolution{Width: 1080, Height: 1920}, refs, style)
	if err != nil {
		t.Fatalf("buildClipASSSubtitle: %v", err)
	}
	for _, want := range []string{
		"PlayResX: 1080",
		"Style: PandaCaption,Inter",
		",&H00FFFFFF,&H0000E6FF,&H0000FF00,&H80000000,",
		",1,12,5,",
		"{\\an5\\pos(540,1536)\\q2}",
		"{\\k35}This",
		"{\\c&H00E6FF&}{\\k40}caption{\\c&HFFFFFF&}",
		"Dialogue: 0,0:00:00.00,0:00:01.55",
	} {
		if !strings.Contains(ass, want) {
			t.Fatalf("expected ASS to contain %s, got %s", want, ass)
		}
	}
	if strings.Contains(ass, `\clip(`) {
		t.Fatalf("caption text should not use ASS clipping, got %s", ass)
	}
	filter := appendClipCaptionFilterGraph("[0:v]scale=1080:1920[vout]", "/tmp/caption one.ass", "/fonts/Caption.ttf")
	if !strings.Contains(filter, "[vout]subtitles=filename='/tmp/caption one.ass':fontsdir='/fonts',format=yuv420p[vcaption]") {
		t.Fatalf("unexpected caption filter graph: %s", filter)
	}
}

func TestBuildClipASSSubtitleOpusDefaultUppercasesAndWraps(t *testing.T) {
	request := captionTestCompositionRequest()
	request.TranscriptTimeline[0].Text = "It truly is AI magic"
	request.TranscriptTimeline[0].Words = []TranscriptWord{
		{ID: "w_0001", StartSeconds: 10.00, EndSeconds: 10.20, Text: "It"},
		{ID: "w_0002", StartSeconds: 10.21, EndSeconds: 10.50, Text: "truly"},
		{ID: "w_0003", StartSeconds: 10.51, EndSeconds: 10.70, Text: "is"},
		{ID: "w_0004", StartSeconds: 10.71, EndSeconds: 10.95, Text: "AI"},
		{ID: "w_0005", StartSeconds: 10.96, EndSeconds: 11.30, Text: "magic"},
	}
	result := captionTestCompositionResult()
	result.CaptionPlan.Cues[0].WordIDs = []string{"w_0001", "w_0005"}
	result.CaptionPlan.Cues[0].EmphasisWordIDs = []string{"w_0004", "w_0005"}
	refs := newClipCaptionReferences(request.TranscriptTimeline)
	style := clipCaptionASSStyle{
		Font:            clipCaptionResolvedFont{Key: clipCaptionFontDefault, Family: "Arial", Path: "/fonts/Arial.ttf"},
		PrimaryColor:    "&H00FFFFFF",
		HighlightColor:  "&H0000E6FF",
		BorderColor:     "&H00000000",
		BackColor:       "&H80000000",
		BorderStyle:     1,
		BorderThickness: captionBorderSize(ClipResolution{Width: 1080, Height: 1920}, clipCaptionBorderThick),
		ShadowSize:      captionShadowSize(ClipResolution{Width: 1080, Height: 1920}),
		Uppercase:       true,
	}

	ass, err := buildClipASSSubtitle(*result.CaptionPlan, result.Plans[0], ClipResolution{Width: 1080, Height: 1920}, refs, style)
	if err != nil {
		t.Fatalf("buildClipASSSubtitle: %v", err)
	}
	for _, want := range []string{
		"{\\k20}IT {\\k29}TRULY {\\k19}IS\\N",
		"{\\c&H00E6FF&}{\\k24}AI{\\c&HFFFFFF&} {\\c&H00E6FF&}{\\k34}MAGIC{\\c&HFFFFFF&}",
		",1,17,5,",
	} {
		if !strings.Contains(ass, want) {
			t.Fatalf("expected Opus-style ASS to contain %s, got %s", want, ass)
		}
	}
}

func TestCaptionASSFitOverrideShrinksLongLinesWithinRegion(t *testing.T) {
	target := ClipResolution{Width: 1080, Height: 1920}
	region := ClipCaptionRegion{
		ID:              "global_captions",
		OutputRect:      ClipRect{X: 250, Y: 720, W: 500, H: 160},
		HorizontalAlign: clipCaptionAlignCenter,
		VerticalAlign:   clipCaptionAlignMiddle,
		MaxLines:        2,
		ZIndex:          20,
	}
	style := clipCaptionASSStyle{
		BorderThickness: captionBorderSize(target, clipCaptionBorderThick),
		ShadowSize:      captionShadowSize(target),
	}
	eventText := `{\\k20}BY\N{\\k52}UNLAWFULLY {\\k48}RETAINING`

	override := captionASSFitOverride(eventText, region, target, style)
	if !strings.Contains(override, `\fs`) {
		t.Fatalf("expected long caption line to receive a fit override, got %q", override)
	}
	if captionEstimatedMaxLineWidth(eventText, captionFontSize(target), captionStyleScaleX) <= captionAvailableTextWidth(region, target, style) {
		t.Fatalf("test fixture should exceed available width without fitting")
	}
}

func TestBuildStackedVerticalFilterGraphUsesExactPixelRects(t *testing.T) {
	filter, err := buildClipRenderFilterGraph("stacked_regions", []ClipRenderRegion{
		{Role: "primary_content", SourceRect: ClipRect{X: 0, Y: 0, W: 1000, H: 700}, OutputRect: ClipRect{X: 0, Y: 0, W: 1000, H: 700}, Fit: "cover"},
		{Role: "facecam", SourceRect: ClipRect{X: 700, Y: 700, W: 300, H: 300}, OutputRect: ClipRect{X: 0, Y: 700, W: 1000, H: 300}, Fit: "cover", ZIndex: 1},
	}, ClipResolution{Width: 1080, Height: 1920}, 30)
	if err != nil {
		t.Fatalf("buildClipRenderFilterGraph: %v", err)
	}
	for _, want := range []string{"color=c=black:s=1080x1920", "scale=1080:1344", "scale=1080:576", "overlay=x=0:y=1344", "[composed]setsar=1,format=yuv420p[vout]"} {
		if !strings.Contains(filter, want) {
			t.Fatalf("expected filter to contain %s, got %s", want, filter)
		}
	}
}

func disabledTestCaptionPlan() *ClipCaptionPlan {
	return applyTestCaptionStyle(&ClipCaptionPlan{
		Mode:          clipCaptionPlanModeDisabled,
		StylePreset:   clipCaptionStyleNone,
		TimingQuality: clipCaptionTimingNone,
		Confidence:    1,
		Reason:        "Captions were explicitly disabled for this test.",
	})
}

func applyTestCaptionStyle(plan *ClipCaptionPlan) *ClipCaptionPlan {
	if plan.Mode == clipCaptionPlanModeDisabled {
		plan.StyleSource = clipCaptionStyleSourceNone
	} else {
		plan.StyleSource = clipCaptionStyleSourceOpusDefault
	}
	plan.FontFamily = clipCaptionFontDefault
	plan.FontColor = "white"
	plan.HighlightColor = "yellow"
	plan.BorderColor = "black"
	plan.BorderThickness = clipCaptionBorderThick
	plan.BackgroundColor = "transparent"
	plan.BackgroundOpacity = 0
	return plan
}

func captionTestCompositionRequest() ClipCompositionRequest {
	return ClipCompositionRequest{
		RequestedAspect: "9:16",
		CaptionMode:     clipCaptionRequestAuto,
		Clip: ClipDecision{Segments: []ClipDecisionSegment{{
			StartSeconds: 10,
			EndSeconds:   16,
			Transcript:   "This caption plan is readable.",
		}}},
		TranscriptTimeline: []ClipCompositionTranscriptSegment{{
			ClipSegmentIndex: 0,
			ID:               "s_0001",
			StartSeconds:     10,
			EndSeconds:       16,
			Text:             "This caption plan is readable.",
			Words: []TranscriptWord{
				{ID: "w_0001", StartSeconds: 10.00, EndSeconds: 10.35, Text: "This"},
				{ID: "w_0002", StartSeconds: 10.36, EndSeconds: 10.76, Text: "caption"},
				{ID: "w_0003", StartSeconds: 10.77, EndSeconds: 11.10, Text: "plan"},
				{ID: "w_0004", StartSeconds: 11.11, EndSeconds: 11.55, Text: "is"},
				{ID: "w_0005", StartSeconds: 11.56, EndSeconds: 12.05, Text: "readable."},
			},
		}},
	}
}

func captionTestCompositionResult() ClipCompositionResult {
	return ClipCompositionResult{
		AspectRatio: "9:16",
		LayoutMode:  "single_crop",
		Confidence:  0.9,
		Reason:      "Use a full-height speaker crop with captions in a lower safe band.",
		Plans: []ClipFrameRenderPlan{{
			AppliesToSegmentIndex: 0,
			SourceStartSeconds:    10,
			SourceEndSeconds:      16,
			Regions: []ClipRenderRegion{{
				Role:       "speaker",
				SourceRect: ClipRect{X: 0, Y: 0, W: 1000, H: 1000},
				OutputRect: ClipRect{X: 0, Y: 0, W: 1000, H: 1000},
				Fit:        "cover",
				ZIndex:     0,
			}},
		}},
		CaptionPlan: applyTestCaptionStyle(&ClipCaptionPlan{
			Mode:          clipCaptionPlanModeBurnedIn,
			StylePreset:   clipCaptionStyleOpusBold,
			TimingQuality: clipCaptionTimingWord,
			Regions: []ClipCaptionRegion{{
				ID:              "bottom_global",
				OutputRect:      ClipRect{X: 80, Y: 720, W: 840, H: 160},
				HorizontalAlign: clipCaptionAlignCenter,
				VerticalAlign:   clipCaptionAlignMiddle,
				MaxLines:        2,
				ZIndex:          20,
			}},
			Cues: []ClipCaptionCue{{
				CaptionRegionID: "bottom_global",
				WordIDs:         []string{"w_0001", "w_0004"},
				EmphasisWordIDs: []string{"w_0002"},
			}},
			Confidence: 0.88,
			Reason:     "Lower captions avoid the speaker's face while staying readable.",
		}),
	}
}
