package youtube

import (
	"context"
	"strings"
	"testing"

	"github.com/sn0w/panda2/internal/llm"
)

func TestOpenRouterClipCompositionPlannerRepairsInvalidPlan(t *testing.T) {
	client := &fakeClipLLM{
		responses: []llm.ChatResponse{
			{Content: `{"aspect_ratio":"16:9","layout_mode":"single_crop","plans":[{"applies_to_segment_index":0,"source_start_seconds":10,"source_end_seconds":40,"regions":[{"role":"primary_content","source_rect":{"x":0,"y":0,"w":1000,"h":1000},"output_rect":{"x":0,"y":0,"w":1000,"h":1000},"fit":"cover","z_index":0}]}],"confidence":0.7,"reason":"Bad aspect."}`},
			{Content: `{"aspect_ratio":"9:16","layout_mode":"stacked_regions","plans":[{"applies_to_segment_index":0,"source_start_seconds":10,"source_end_seconds":40,"regions":[{"role":"primary_content","source_rect":{"x":0,"y":0,"w":1000,"h":700},"output_rect":{"x":0,"y":0,"w":1000,"h":700},"fit":"cover","z_index":0},{"role":"facecam","source_rect":{"x":700,"y":700,"w":300,"h":300},"output_rect":{"x":0,"y":700,"w":1000,"h":300},"fit":"cover","z_index":1}]}],"confidence":0.9,"reason":"Stack primary content above facecam for vertical viewing."}`},
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
			{Content: `{"aspect_ratio":"9:16","layout_mode":"single_crop","plans":[{"applies_to_segment_index":0,"source_start_seconds":10,"source_end_seconds":40,"regions":[{"role":"speaker","source_rect":{"x":0,"y":0,"w":500,"h":1000},"output_rect":{"x":0,"y":0,"w":1000,"h":1000},"fit":"cover","z_index":0}]}],"confidence":0.92,"reason":"Crop the active speaker for a vertical clip."}`},
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
