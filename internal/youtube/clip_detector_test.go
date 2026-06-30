package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/llm"
)

type fakeClipLLM struct {
	requests  []llm.ChatRequest
	responses []llm.ChatResponse
	errors    []error
	response  llm.ChatResponse
	err       error
}

func (f *fakeClipLLM) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.requests = append(f.requests, request)
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return llm.ChatResponse{}, err
		}
	}
	if len(f.responses) > 0 {
		response := f.responses[0]
		f.responses = f.responses[1:]
		return response, nil
	}
	return f.response, f.err
}

func closeFloat(left float64, right float64) bool {
	if left > right {
		return left-right < 0.001
	}
	return right-left < 0.001
}

type blockingClipLLM struct {
	requests []llm.ChatRequest
	release  chan struct{}
}

func (f *blockingClipLLM) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.requests = append(f.requests, request)
	<-f.release
	return llm.ChatResponse{}, nil
}

type chunkedClipLLM struct {
	requests []llm.ChatRequest
}

func (f *chunkedClipLLM) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.requests = append(f.requests, request)
	if len(request.Messages) < 2 {
		return llm.ChatResponse{}, fmt.Errorf("missing user message")
	}
	content := request.Messages[1].Content
	if strings.Contains(content, `"candidates"`) {
		var input clipDetectionCandidateSelectionInput
		if err := json.Unmarshal([]byte(content), &input); err != nil {
			return llm.ChatResponse{}, err
		}
		if len(input.Candidates) < 2 {
			return llm.ChatResponse{}, fmt.Errorf("expected at least two candidates")
		}
		response := clipDetectionCandidateSelectionResponse{
			SelectedClipIDs: []string{
				input.Candidates[0].ID,
				input.Candidates[len(input.Candidates)-1].ID,
			},
		}
		data, err := json.Marshal(response)
		if err != nil {
			return llm.ChatResponse{}, err
		}
		return llm.ChatResponse{Content: string(data)}, nil
	}

	var input clipDetectionInput
	if err := json.Unmarshal([]byte(content), &input); err != nil {
		return llm.ChatResponse{}, err
	}
	clips := make([]clipDetectionResponseClip, 0, 2)
	for _, unit := range input.SpeechUnits {
		duration := unit.EndSeconds - unit.StartSeconds
		if !unit.StartBoundaryClean || !unit.EndBoundaryClean || duration < 5 || duration > 30 {
			continue
		}
		rank := len(clips) + 1
		clips = append(clips, clipDetectionResponseClip{
			Rank:  rank,
			Title: "Candidate " + strings.TrimSpace(unit.StartWordID),
			Type:  "continuous",
			Segments: []clipDetectionResponseSegment{
				testResponseSegment(unit.StartWordID, unit.EndWordID, unit.Text),
			},
			Reason:            "Standalone window candidate.",
			Confidence:        0.8,
			ViralityScore:     82,
			HookScore:         84,
			RetentionScore:    80,
			ShareabilityScore: 81,
			DurationPolicy:    "short_exception",
			ExceptionReason:   "Standalone soundbite works shorter.",
		})
		if len(clips) == 2 {
			break
		}
	}
	if len(clips) < 2 {
		return llm.ChatResponse{}, fmt.Errorf("expected at least two clean speech units in chunk")
	}
	data, err := json.Marshal(clipDetectionResponse{Clips: clips})
	if err != nil {
		return llm.ChatResponse{}, err
	}
	return llm.ChatResponse{Content: string(data)}, nil
}

func TestOpenRouterClipDetectorUsesConfiguredModelAndJSONSchemaPrompt(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: clipDetectionTestResponse(t,
				clipDetectionResponseClip{
					Rank:  1,
					Title: "Best Moment",
					Type:  "spliced",
					Segments: []clipDetectionResponseSegment{
						testResponseSegment("w_0001", "w_0009", "The important part starts here. The setup gets sharper."),
						testResponseSegment("w_0010", "w_0013", "Payoff lands cleanly."),
					},
					Reason:            "Strong hook and payoff without the pause.",
					Confidence:        0.8,
					ViralityScore:     82,
					HookScore:         88,
					RetentionScore:    76,
					ShareabilityScore: 83,
					DurationPolicy:    "requested_duration",
				},
				clipDetectionResponseClip{
					Rank:  2,
					Title: "Full Context",
					Type:  "continuous",
					Segments: []clipDetectionResponseSegment{
						testResponseSegment("w_0014", "w_0021", "A longer version with setup. The fuller context lands cleanly."),
					},
					Reason:            "Longer version with setup.",
					Confidence:        0.72,
					ViralityScore:     75,
					HookScore:         72,
					RetentionScore:    74,
					ShareabilityScore: 78,
					DurationPolicy:    "requested_duration",
				},
			),
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{
		Client: client,
		Model:  "provider/clip-model",
	})
	decision, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Title:              "Deep Dive",
		URL:                "https://www.youtube.com/watch?v=deep",
		Uploader:           "Teacher",
		Duration:           2 * time.Minute,
		Instructions:       "clip the key explanation",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           testClipTranscriptSegments(),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(decision.Clips) != 2 || decision.Clips[0].Title != "Best Moment" || decision.Clips[0].Type != "spliced" || len(decision.Clips[0].Segments) != 2 || !closeFloat(decision.Clips[0].Segments[0].StartSeconds, 9.82) || !closeFloat(decision.Clips[0].Segments[0].EndSeconds, 18.28) || !closeFloat(decision.Clips[0].Segments[1].StartSeconds, 20.82) || !closeFloat(decision.Clips[0].Segments[1].EndSeconds, 27.28) {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if decision.Clips[0].Segments[0].Transcript != "The important part starts here. The setup gets sharper." {
		t.Fatalf("expected aligned transcript text, got %q", decision.Clips[0].Segments[0].Transcript)
	}
	if decision.Clips[0].Segments[0].SpeechStartSeconds != 10 || decision.Clips[0].Segments[0].SpeechEndSeconds != 18 {
		t.Fatalf("expected speech boundaries to remain separate from render padding, got %+v", decision.Clips[0].Segments[0])
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(client.requests))
	}
	request := client.requests[0]
	if request.Model != "provider/clip-model" {
		t.Fatalf("expected configured clip model, got %q", request.Model)
	}
	if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema == nil || !request.ResponseFormat.JSONSchema.Strict {
		t.Fatalf("expected strict json schema response format, got %+v", request.ResponseFormat)
	}
	if request.MaxTokens != defaultClipDetectionMaxTokens {
		t.Fatalf("expected default max tokens %d, got %d", defaultClipDetectionMaxTokens, request.MaxTokens)
	}
	systemPrompt := request.Messages[0].Content
	for _, want := range []string{`supplied strict JSON schema`, "word-backed span", "start_word_id", "boundary_options", "clean_start_word_ids", "clean_end_word_ids", "Do not return start_seconds", "min_splice_source_gap_seconds", "Clip type should match segment count", "Every clip must make sense when watched alone", "prepositions", "do not wander to generic best moments", "Broad/default requests should be expansive", "quality bar applies to every clip", "Boundary options are formatting constraints", "who or what is being discussed", "Do not start a clip with orphaned pronouns", "Reject subjectless fragments", "complete mini-arc"} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("expected schema prompt to contain %s, got %s", want, systemPrompt)
		}
	}
	schema := string(request.ResponseFormat.JSONSchema.Schema)
	for _, want := range []string{`"clips"`, `"segments"`, `"start_word_id"`, `"end_word_id"`, `"boundary_reason"`, `"virality_score"`, `"duration_policy"`, `"additionalProperties":false`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("expected response schema to contain %s, got %s", want, schema)
		}
	}
	for _, legacy := range []string{`"start_segment_index"`, `"end_segment_index"`, `"start_seconds"`, `"end_seconds"`, `"total_duration_seconds"`} {
		if strings.Contains(schema, legacy) {
			t.Fatalf("clip detection schema still contains legacy field %s: %s", legacy, schema)
		}
	}
	for _, unsupported := range []string{`"exclusiveMinimum"`, `"minimum"`, `"maximum"`, `"minItems"`, `"maxItems"`, `"minLength"`, `"maxLength"`} {
		if strings.Contains(schema, unsupported) {
			t.Fatalf("clip detection schema should avoid provider-fragile keyword %s: %s", unsupported, schema)
		}
	}
	if len(request.Tools) != 0 {
		t.Fatalf("clip detection should not expose tools, got %+v", request.Tools)
	}
	userContent := request.Messages[1].Content
	if !strings.Contains(systemPrompt, "constraints.min_clips") || !strings.Contains(systemPrompt, "max_clips") || !strings.Contains(userContent, "clip the key explanation") || !strings.Contains(userContent, "transcript_segments") || !strings.Contains(userContent, "speech_units") || !strings.Contains(userContent, "boundary_options") || !strings.Contains(userContent, `"clean_start_word_ids"`) || !strings.Contains(userContent, `"clean_end_word_ids"`) || !strings.Contains(userContent, `"start_word_id":"w_0001"`) || !strings.Contains(userContent, `"word_id":"w_0001"`) || !strings.Contains(userContent, `"start_boundary_clean":true`) || !strings.Contains(userContent, `"end_boundary_clean":true`) || !strings.Contains(userContent, `"render_lead_pad_seconds":0.18`) || !strings.Contains(userContent, `"render_tail_pad_seconds":0.28`) || !strings.Contains(userContent, `"min_splice_source_gap_seconds":0.48`) || !strings.Contains(userContent, `"min_clips":2`) || !strings.Contains(userContent, `"max_clips":12`) {
		t.Fatalf("expected structured detector input, got %s", userContent)
	}
}

func TestClipDetectionSelectionPromptRejectsSubjectlessCandidates(t *testing.T) {
	systemPrompt := clipDetectionSelectionSystemPrompt()
	for _, want := range []string{
		"concrete subject and mini-arc",
		"missing subject",
		"contextless pronouns",
		"setup-only fragments",
		"answer-only fragments",
		"payoff without setup",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("expected selection prompt to contain %s, got %s", want, systemPrompt)
		}
	}
}

func TestClipDetectionSpeechUnitsDoNotSplitOnCommaOrSegmentContinuation(t *testing.T) {
	segments := []TranscriptSegment{
		{
			ID:           "s_0001",
			StartSeconds: 100.0,
			EndSeconds:   101.1,
			Text:         "I mean, business analyst,",
			Words: []TranscriptWord{
				testWord("w_0081", 100.00, 100.20, "I"),
				testWord("w_0082", 100.20, 100.45, "mean,"),
				testWord("w_0083", 100.45, 100.85, "business"),
				testWord("w_0084", 100.85, 101.10, "analyst,"),
			},
		},
		{
			ID:           "s_0002",
			StartSeconds: 101.1,
			EndSeconds:   102.7,
			Text:         "is remote work leading to burnout.",
			Words: []TranscriptWord{
				testWord("w_0085", 101.10, 101.30, "is"),
				testWord("w_0086", 101.30, 101.60, "remote"),
				testWord("w_0087", 101.60, 101.90, "work"),
				testWord("w_0088", 101.90, 102.20, "leading"),
				testWord("w_0089", 102.20, 102.40, "to"),
				testWord("w_0090", 102.40, 102.70, "burnout."),
			},
		},
	}
	refs, _, err := transcriptWordReferences(segments)
	if err != nil {
		t.Fatalf("transcriptWordReferences: %v", err)
	}

	units := clipDetectionSpeechUnits(refs)
	if len(units) != 1 {
		t.Fatalf("expected one complete sentence unit, got %+v", units)
	}
	unit := units[0]
	if unit.StartWordID != "w_0081" || unit.EndWordID != "w_0090" || !strings.Contains(unit.Text, "analyst, is remote") {
		t.Fatalf("expected comma and segment continuation to stay together, got %+v", unit)
	}
	if !unit.StartBoundaryClean || !unit.EndBoundaryClean {
		t.Fatalf("expected full unit boundaries to be clean, got %+v", unit)
	}
	for _, unit := range units {
		if unit.StartWordID == "w_0085" {
			t.Fatalf("mid-sentence continuation word should not be advertised as a unit start: %+v", unit)
		}
	}
}

func TestClipDetectionSpeechUnitsSkipWeakStarterAfterCleanBoundary(t *testing.T) {
	segments := []TranscriptSegment{{
		ID:           "s_0001",
		StartSeconds: 200.0,
		EndSeconds:   204.0,
		Text:         "Does that look like? Well, first of all, number one.",
		Words: []TranscriptWord{
			testWord("w_1370", 200.00, 200.20, "does"),
			testWord("w_1371", 200.20, 200.40, "that"),
			testWord("w_1372", 200.40, 200.65, "look"),
			testWord("w_1373", 200.65, 200.90, "like?"),
			testWord("w_1374", 201.00, 201.20, "Well,"),
			testWord("w_1375", 201.20, 201.45, "first"),
			testWord("w_1376", 201.45, 201.65, "of"),
			testWord("w_1377", 201.65, 201.85, "all,"),
			testWord("w_1378", 201.85, 202.15, "number"),
			testWord("w_1379", 202.15, 202.50, "one."),
		},
	}}
	refs, refsByID, err := transcriptWordReferences(segments)
	if err != nil {
		t.Fatalf("transcriptWordReferences: %v", err)
	}

	units := clipDetectionSpeechUnits(refs)
	if len(units) != 3 {
		t.Fatalf("expected question, weak starter, and clean continuation units, got %+v", units)
	}
	if units[1].StartWordID != "w_1374" || units[1].StartBoundaryClean || units[1].StartBoundaryReason != "starts_on_weak_boundary_word" {
		t.Fatalf("expected weak starter unit to be marked dirty, got %+v", units[1])
	}
	if units[2].StartWordID != "w_1375" || !units[2].StartBoundaryClean || units[2].StartBoundaryReason != "after_weak_boundary_starter" {
		t.Fatalf("expected first real word after weak starter to be a clean start, got %+v", units[2])
	}
	options := clipDetectionBoundaryOptionsInputForRefs(refs, units)
	if hasClipBoundaryOption(options.CleanStartWordIDs, "w_1374") {
		t.Fatalf("weak starter should not be a clean start option: %+v", options.CleanStartWordIDs)
	}
	if !hasClipBoundaryOption(options.CleanStartWordIDs, "w_1375") {
		t.Fatalf("word after weak starter should be a clean start option: %+v", options.CleanStartWordIDs)
	}
	aligned, err := alignClipDecisionSegmentToTranscript(testResponseSegment("w_1375", "w_1379", "first of all, number one."), refs, refsByID)
	if err != nil {
		t.Fatalf("expected detector to accept clean start after weak starter: %v", err)
	}
	if aligned.Transcript != "first of all, number one." {
		t.Fatalf("expected weak starter to be omitted from aligned transcript, got %q", aligned.Transcript)
	}
}

func TestClipDetectionSpeechUnitsSkipWeakStarterWithoutCommaAfterCleanBoundary(t *testing.T) {
	segments := []TranscriptSegment{{
		ID:           "s_0001",
		StartSeconds: 220.0,
		EndSeconds:   224.0,
		Text:         "end of his rope. But wouldn't you know it,",
		Words: []TranscriptWord{
			testWord("w_0342", 220.00, 220.20, "end"),
			testWord("w_0343", 220.20, 220.40, "of"),
			testWord("w_0344", 220.40, 220.60, "his"),
			testWord("w_0345", 220.60, 220.90, "rope."),
			testWord("w_0346", 221.00, 221.15, "But"),
			testWord("w_0347", 221.15, 221.45, "wouldn't"),
			testWord("w_0348", 221.45, 221.60, "you"),
			testWord("w_0349", 221.60, 221.85, "know"),
			testWord("w_0350", 221.85, 222.10, "it,"),
		},
	}}
	refs, refsByID, err := transcriptWordReferences(segments)
	if err != nil {
		t.Fatalf("transcriptWordReferences: %v", err)
	}

	units := clipDetectionSpeechUnits(refs)
	if len(units) != 3 {
		t.Fatalf("expected sentence, weak starter, and clean continuation units, got %+v", units)
	}
	if units[1].StartWordID != "w_0346" || units[1].StartBoundaryClean || units[1].StartBoundaryReason != "starts_on_weak_boundary_word" {
		t.Fatalf("expected But to be marked dirty context, got %+v", units[1])
	}
	if units[2].StartWordID != "w_0347" || !units[2].StartBoundaryClean || units[2].StartBoundaryReason != "after_weak_boundary_starter" {
		t.Fatalf("expected word after But to be a clean start, got %+v", units[2])
	}
	options := clipDetectionBoundaryOptionsInputForRefs(refs, units)
	if hasClipBoundaryOption(options.CleanStartWordIDs, "w_0346") {
		t.Fatalf("weak starter should not be a clean start option: %+v", options.CleanStartWordIDs)
	}
	if !hasClipBoundaryOption(options.CleanStartWordIDs, "w_0347") {
		t.Fatalf("word after weak starter should be a clean start option: %+v", options.CleanStartWordIDs)
	}
	aligned, err := alignClipDecisionSegmentToTranscript(testResponseSegment("w_0347", "w_0350", "wouldn't you know it,"), refs, refsByID)
	if err != nil {
		t.Fatalf("expected detector to accept clean start after But: %v", err)
	}
	if aligned.Transcript != "wouldn't you know it," {
		t.Fatalf("expected weak starter to be omitted from aligned transcript, got %q", aligned.Transcript)
	}
}

func TestClipDetectionSpeechUnitsMarkForcedSplitsAsContinuationBoundaries(t *testing.T) {
	words := make([]TranscriptWord, 0, clipDetectionMaxWordsPerUnit+2)
	for index := 0; index < clipDetectionMaxWordsPerUnit+2; index++ {
		id := fmt.Sprintf("w_%04d", index+1)
		start := float64(index) * 0.21
		words = append(words, testWord(id, start, start+0.2, fmt.Sprintf("word%d", index+1)))
	}
	refs, _, err := transcriptWordReferences([]TranscriptSegment{{
		ID:           "s_0001",
		StartSeconds: 0,
		EndSeconds:   float64(len(words)),
		Text:         "long run on sentence",
		Words:        words,
	}})
	if err != nil {
		t.Fatalf("transcriptWordReferences: %v", err)
	}

	units := clipDetectionSpeechUnits(refs)
	if len(units) != 2 {
		t.Fatalf("expected forced max-word split, got %+v", units)
	}
	if units[0].EndBoundaryClean || units[0].EndBoundaryReason != "continues_next_sentence" {
		t.Fatalf("expected forced split end to be marked dirty, got %+v", units[0])
	}
	if units[1].StartBoundaryClean || units[1].StartBoundaryReason != "continues_previous_sentence" {
		t.Fatalf("expected forced split start to be marked dirty, got %+v", units[1])
	}
}

func TestClipDetectionBoundaryOptionsExcludeForcedSplitContinuationStart(t *testing.T) {
	refs, _, err := transcriptWordReferences(forcedSplitBackgroundTranscriptSegments())
	if err != nil {
		t.Fatalf("transcriptWordReferences: %v", err)
	}

	units := clipDetectionSpeechUnits(refs)
	foundDirtyEnd := false
	foundDirtyStart := false
	for _, unit := range units {
		if unit.EndWordID == "w_0958" {
			foundDirtyEnd = true
			if unit.EndBoundaryClean {
				t.Fatalf("expected forced split to end dirty at w_0958, got %+v", unit)
			}
		}
		if unit.StartWordID == "w_0959" {
			foundDirtyStart = true
			if unit.StartBoundaryClean {
				t.Fatalf("expected forced split to expose w_0959 only as dirty context, got %+v", unit)
			}
		}
	}
	if !foundDirtyEnd || !foundDirtyStart {
		t.Fatalf("expected forced split around w_0958/w_0959, got %+v", units)
	}

	options := clipDetectionBoundaryOptionsInputForRefs(refs, units)
	if hasClipBoundaryOption(options.CleanStartWordIDs, "w_0959") {
		t.Fatalf("dirty continuation start w_0959 must not be advertised as a clean start option: %+v", options.CleanStartWordIDs)
	}
	if hasClipBoundaryOption(options.CleanEndWordIDs, "w_0958") {
		t.Fatalf("dirty continuation end w_0958 must not be advertised as a clean end option: %+v", options.CleanEndWordIDs)
	}
	if !hasClipBoundaryOption(options.CleanStartWordIDs, "w_0941") {
		t.Fatalf("expected true sentence start w_0941 to be advertised: %+v", options.CleanStartWordIDs)
	}
	if !hasClipBoundaryOption(options.CleanEndWordIDs, "w_0964") {
		t.Fatalf("expected true sentence end w_0964 to be advertised: %+v", options.CleanEndWordIDs)
	}
}

func TestOpenRouterClipDetectorRequestExcludesDirtyForcedSplitFromBoundaryOptions(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: clipDetectionTestResponse(t,
				clipDetectionResponseClip{
					Rank:  1,
					Title: "Background Setup",
					Type:  "continuous",
					Segments: []clipDetectionResponseSegment{
						testResponseSegment("w_0941", "w_0964", "there are times where the game audio and the music still sound like they might have playing in background while you doomscroll on stream."),
					},
					Reason:            "Complete sentence with the setup and payoff.",
					Confidence:        0.8,
					ViralityScore:     82,
					HookScore:         85,
					RetentionScore:    80,
					ShareabilityScore: 81,
					DurationPolicy:    "short_exception",
					ExceptionReason:   "Standalone soundbite works shorter.",
				},
				clipDetectionResponseClip{
					Rank:  2,
					Title: "Clean Followup",
					Type:  "continuous",
					Segments: []clipDetectionResponseSegment{
						testResponseSegment("w_0965", "w_0968", "Another clean moment lands."),
					},
					Reason:            "Second viable clean sentence.",
					Confidence:        0.72,
					ViralityScore:     74,
					HookScore:         72,
					RetentionScore:    74,
					ShareabilityScore: 75,
					DurationPolicy:    "short_exception",
					ExceptionReason:   "Standalone soundbite works shorter.",
				},
			),
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           6 * time.Minute,
		Instructions:       "clip the background line",
		MinDurationSeconds: 1,
		MaxDurationSeconds: 30,
		Segments:           forcedSplitBackgroundTranscriptSegments(),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(client.requests))
	}
	var input clipDetectionInput
	if err := json.Unmarshal([]byte(client.requests[0].Messages[1].Content), &input); err != nil {
		t.Fatalf("unmarshal detector input: %v", err)
	}
	dirtyUnitFound := false
	for _, unit := range input.SpeechUnits {
		if unit.StartWordID == "w_0959" {
			dirtyUnitFound = true
			if unit.StartBoundaryClean {
				t.Fatalf("w_0959 should only appear as dirty context, got %+v", unit)
			}
		}
	}
	if !dirtyUnitFound {
		t.Fatalf("expected w_0959 to remain visible in speech_units context, got %+v", input.SpeechUnits)
	}
	if hasClipBoundaryOption(input.BoundaryOptions.CleanStartWordIDs, "w_0959") {
		t.Fatalf("detector request must not list w_0959 as a clean start option: %+v", input.BoundaryOptions.CleanStartWordIDs)
	}
	if hasClipBoundaryOption(input.BoundaryOptions.CleanEndWordIDs, "w_0958") {
		t.Fatalf("detector request must not list w_0958 as a clean end option: %+v", input.BoundaryOptions.CleanEndWordIDs)
	}
}

func TestClipDetectionWindowContextUsesFullTranscriptBoundaryQuality(t *testing.T) {
	full := []TranscriptSegment{{
		ID:           "s_0001",
		StartSeconds: 0,
		EndSeconds:   2,
		Text:         "The setup continues here.",
		Words: []TranscriptWord{
			testWord("w_0001", 0.0, 0.2, "The"),
			testWord("w_0002", 0.3, 0.5, "setup"),
			testWord("w_0003", 0.6, 0.8, "continues"),
			testWord("w_0004", 0.9, 1.1, "here."),
		},
	}}
	window := []TranscriptSegment{{
		ID:           "s_0001_part_001",
		StartSeconds: 0.6,
		EndSeconds:   1.1,
		Text:         "continues here.",
		Words: []TranscriptWord{
			testWord("w_0003", 0.6, 0.8, "continues"),
			testWord("w_0004", 0.9, 1.1, "here."),
		},
	}}

	_, units, options, err := clipDetectionTranscriptContextWithBoundarySegments(window, full)
	if err != nil {
		t.Fatalf("clipDetectionTranscriptContextWithBoundarySegments: %v", err)
	}
	if hasClipBoundaryOption(options.CleanStartWordIDs, "w_0003") {
		t.Fatalf("window-local first word should not become a clean start option: %+v", options.CleanStartWordIDs)
	}
	found := false
	for _, unit := range units {
		if unit.StartWordID != "w_0003" {
			continue
		}
		found = true
		if unit.StartBoundaryClean || unit.StartBoundaryReason != "continues_previous_sentence" || unit.PreviousWord != "setup" {
			t.Fatalf("expected full-transcript boundary quality on window unit, got %+v", unit)
		}
	}
	if !found {
		t.Fatalf("expected window speech unit starting at w_0003, got %+v", units)
	}
}

func TestOpenRouterClipDetectorChunksOversizedTranscriptAndSelectsCandidates(t *testing.T) {
	segments := oversizedClipTranscriptSegments(180)
	request := ClipDetectionRequest{
		Title:              "Long Stream",
		URL:                "https://www.youtube.com/watch?v=long",
		Uploader:           "Streamer",
		Duration:           30 * time.Minute,
		Instructions:       "clip the strongest standalone moments",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		MaxClips:           defaultClipDetectionMaxClips,
		Segments:           segments,
	}
	fullData, err := clipDetectionRequestData(request, request.Segments, nil, defaultClipDetectionMaxClips)
	if err != nil {
		t.Fatalf("clipDetectionRequestData: %v", err)
	}
	if len(fullData) <= clipDetectionInputMaxBytes {
		t.Fatalf("test transcript should exceed single request budget, got %d bytes", len(fullData))
	}

	client := &chunkedClipLLM{}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	result, err := detector.Detect(context.Background(), request)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(result.Clips) != 2 {
		t.Fatalf("expected selector to choose two final clips, got %+v", result)
	}
	if result.Clips[0].Rank != 1 || result.Clips[1].Rank != 2 {
		t.Fatalf("expected final ranks to be normalized, got %+v", result.Clips)
	}
	if result.Clips[0].Segments[0].StartWordID == result.Clips[1].Segments[0].StartWordID {
		t.Fatalf("expected final clips from distinct candidates, got %+v", result.Clips)
	}

	detectionRequests := 0
	selectionRequests := 0
	for _, modelRequest := range client.requests {
		if len(modelRequest.Messages) < 2 {
			t.Fatalf("unexpected model request: %+v", modelRequest)
		}
		userContent := modelRequest.Messages[1].Content
		if len(userContent) > clipDetectionInputMaxBytes {
			t.Fatalf("model request exceeded clip detection budget: %d bytes", len(userContent))
		}
		if strings.Contains(userContent, `"candidates"`) {
			selectionRequests++
			continue
		}
		detectionRequests++
	}
	if detectionRequests < 2 {
		t.Fatalf("expected oversized transcript to be detected in chunks, got %d detection request(s)", detectionRequests)
	}
	if selectionRequests != 1 {
		t.Fatalf("expected one final candidate selector request, got %d", selectionRequests)
	}
}

func TestOpenRouterClipDetectorShrinksRenderPaddingAtMaxDuration(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{Content: clipDetectionTestResponse(t,
			clipDetectionResponseClip{
				Rank:  1,
				Title: "Full Length",
				Type:  "continuous",
				Segments: []clipDetectionResponseSegment{
					testResponseSegment("w_0001", "w_0004", "The full length moment lands cleanly."),
				},
				Reason:            "Strong standalone moment.",
				Confidence:        0.8,
				ViralityScore:     82,
				HookScore:         85,
				RetentionScore:    80,
				ShareabilityScore: 81,
				DurationPolicy:    "requested_duration",
			},
			testSecondResponseClip("w_0005", "w_0007"),
		)},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments: []TranscriptSegment{
			{
				ID:           "s_0001",
				StartSeconds: 10,
				EndSeconds:   40,
				Text:         "The full length moment lands cleanly.",
				Words: []TranscriptWord{
					testWord("w_0001", 10.0, 10.2, "The"),
					testWord("w_0002", 15.0, 15.2, "Full"),
					testWord("w_0003", 25.0, 25.2, "length"),
					testWord("w_0004", 39.8, 40.0, "cleanly."),
				},
			},
			{
				ID:           "s_0002",
				StartSeconds: 45,
				EndSeconds:   55,
				Text:         "The second moment lands.",
				Words: []TranscriptWord{
					testWord("w_0005", 45.0, 45.3, "The"),
					testWord("w_0006", 47.0, 47.3, "second"),
					testWord("w_0007", 54.7, 55.0, "lands."),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	first := result.Clips[0].Segments[0]
	if first.StartWordID != "w_0001" || first.EndWordID != "w_0004" || !closeFloat(first.StartSeconds, 10) || !closeFloat(first.EndSeconds, 40) {
		t.Fatalf("expected model word span with shrunken padding, got %+v", first)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected padding normalization without repair retry, got %d", len(client.requests))
	}
}

func TestOpenRouterClipDetectorMergesSpliceSegmentsTooCloseForRenderPadding(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: clipDetectionTestResponse(t,
				clipDetectionResponseClip{
					Rank:  1,
					Title: "Close Splice",
					Type:  "spliced",
					Segments: []clipDetectionResponseSegment{
						testResponseSegment("w_0001", "w_0003", "The setup lands."),
						testResponseSegment("w_0004", "w_0006", "The payoff lands."),
					},
					Reason:            "The payoff belongs directly after the setup.",
					Confidence:        0.82,
					ViralityScore:     84,
					HookScore:         86,
					RetentionScore:    80,
					ShareabilityScore: 82,
					DurationPolicy:    "short_exception",
					ExceptionReason:   "Standalone soundbite works shorter.",
				},
				testSecondResponseClip("w_0007", "w_0009"),
			),
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 1,
		MaxDurationSeconds: 30,
		Segments:           renderPaddingSpliceTranscriptSegments(11.30),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	firstClip := result.Clips[0]
	if firstClip.Type != "continuous" || len(firstClip.Segments) != 1 {
		t.Fatalf("expected close splice to normalize into one continuous segment, got %+v", firstClip)
	}
	segment := firstClip.Segments[0]
	if segment.StartWordID != "w_0001" || segment.EndWordID != "w_0006" {
		t.Fatalf("expected merged word span to preserve outer boundaries, got %+v", segment)
	}
	if segment.Transcript != "The setup lands. The payoff lands." {
		t.Fatalf("expected merged transcript to include the full continuous source span, got %q", segment.Transcript)
	}
	if !closeFloat(segment.StartSeconds, 9.82) || !closeFloat(segment.EndSeconds, 13.28) || !closeFloat(segment.SpeechStartSeconds, 10.0) || !closeFloat(segment.SpeechEndSeconds, 13.0) {
		t.Fatalf("expected merged segment to preserve speech timing and render padding, got %+v", segment)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected close splice to normalize without repair, got %d requests", len(client.requests))
	}
}

func TestOpenRouterClipDetectorKeepsThresholdSpliceSeparate(t *testing.T) {
	secondStart := 11.0 + clipRequiredSpliceSourceGapSeconds()
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: clipDetectionTestResponse(t,
				clipDetectionResponseClip{
					Rank:  1,
					Title: "Threshold Splice",
					Type:  "spliced",
					Segments: []clipDetectionResponseSegment{
						testResponseSegment("w_0001", "w_0003", "The setup lands."),
						testResponseSegment("w_0004", "w_0006", "The payoff lands."),
					},
					Reason:            "The cuts leave enough source gap for render padding.",
					Confidence:        0.82,
					ViralityScore:     84,
					HookScore:         86,
					RetentionScore:    80,
					ShareabilityScore: 82,
					DurationPolicy:    "short_exception",
					ExceptionReason:   "Standalone soundbite works shorter.",
				},
				testSecondResponseClip("w_0007", "w_0009"),
			),
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 1,
		MaxDurationSeconds: 30,
		Segments:           renderPaddingSpliceTranscriptSegments(secondStart),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	firstClip := result.Clips[0]
	if firstClip.Type != "spliced" || len(firstClip.Segments) != 2 {
		t.Fatalf("expected threshold-safe splice to remain separate, got %+v", firstClip)
	}
	if !closeFloat(firstClip.Segments[0].EndSeconds, 11.28) || !closeFloat(firstClip.Segments[1].StartSeconds, secondStart-clipBoundaryLeadPadSeconds) {
		t.Fatalf("expected threshold-safe padded timings, got %+v", firstClip.Segments)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(client.requests))
	}
}

func TestOpenRouterClipDetectorPartiallyMergesCloseSplicePair(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: clipDetectionTestResponse(t,
				clipDetectionResponseClip{
					Rank:  1,
					Title: "Partial Splice",
					Type:  "spliced",
					Segments: []clipDetectionResponseSegment{
						testResponseSegment("w_0001", "w_0003", "The setup lands."),
						testResponseSegment("w_0004", "w_0006", "The payoff lands."),
						testResponseSegment("w_0007", "w_0009", "This lands again."),
					},
					Reason:            "The first two spans are effectively continuous, then the final beat jumps forward.",
					Confidence:        0.82,
					ViralityScore:     84,
					HookScore:         86,
					RetentionScore:    80,
					ShareabilityScore: 82,
					DurationPolicy:    "short_exception",
					ExceptionReason:   "Standalone soundbite works shorter.",
				},
				testSecondResponseClip("w_0010", "w_0012"),
			),
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 1,
		MaxDurationSeconds: 30,
		Segments: []TranscriptSegment{{
			ID:           "s_0001",
			StartSeconds: 10,
			EndSeconds:   27,
			Text:         "The setup lands. The payoff lands. This lands again. The second moment lands.",
			Words: []TranscriptWord{
				testWord("w_0001", 10.0, 10.2, "The"),
				testWord("w_0002", 10.4, 10.6, "setup"),
				testWord("w_0003", 10.8, 11.0, "lands."),
				testWord("w_0004", 11.3, 11.5, "The"),
				testWord("w_0005", 11.7, 11.9, "payoff"),
				testWord("w_0006", 12.3, 12.5, "lands."),
				testWord("w_0007", 14.0, 14.2, "This"),
				testWord("w_0008", 15.0, 15.2, "lands"),
				testWord("w_0009", 16.6, 17.0, "again."),
				testWord("w_0010", 20.0, 20.3, "The"),
				testWord("w_0011", 23.0, 23.3, "second"),
				testWord("w_0012", 26.6, 27.0, "lands."),
			},
		}},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	firstClip := result.Clips[0]
	if firstClip.Type != "spliced" || len(firstClip.Segments) != 2 {
		t.Fatalf("expected only the close pair to merge and clip to remain spliced, got %+v", firstClip)
	}
	if firstClip.Segments[0].StartWordID != "w_0001" || firstClip.Segments[0].EndWordID != "w_0006" || firstClip.Segments[1].StartWordID != "w_0007" || firstClip.Segments[1].EndWordID != "w_0009" {
		t.Fatalf("expected close pair merge with final segment preserved, got %+v", firstClip.Segments)
	}
	if firstClip.Segments[0].Transcript != "The setup lands. The payoff lands." {
		t.Fatalf("expected merged transcript to include the close pair, got %q", firstClip.Segments[0].Transcript)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected partial merge without repair, got %d requests", len(client.requests))
	}
}

func TestOpenRouterClipDetectorShrinksRenderPaddingForMergedSpliceAtMaxDuration(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{Content: clipDetectionTestResponse(t,
			clipDetectionResponseClip{
				Rank:  1,
				Title: "Too Long Close Splice",
				Type:  "spliced",
				Segments: []clipDetectionResponseSegment{
					testResponseSegment("w_0001", "w_0003", "Intro. The long setup lands."),
					testResponseSegment("w_0004", "w_0006", "The payoff lands."),
				},
				Reason:            "The full close splice is exactly the requested max duration before padding.",
				Confidence:        0.82,
				ViralityScore:     84,
				HookScore:         86,
				RetentionScore:    80,
				ShareabilityScore: 82,
				DurationPolicy:    "requested_duration",
			},
			testSecondResponseClip("w_0007", "w_0009"),
		)},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments: []TranscriptSegment{
			{
				ID:           "s_0001",
				StartSeconds: 10,
				EndSeconds:   40,
				Text:         "Intro. The long setup lands. The payoff lands.",
				Words: []TranscriptWord{
					testWord("w_0001", 10.0, 10.2, "Intro."),
					testWord("w_0002", 15.0, 15.2, "The"),
					testWord("w_0003", 25.0, 25.2, "lands."),
					testWord("w_0004", 25.5, 25.7, "The"),
					testWord("w_0005", 34.0, 34.3, "payoff"),
					testWord("w_0006", 39.8, 40.0, "lands."),
				},
			},
			{
				ID:           "s_0002",
				StartSeconds: 45,
				EndSeconds:   55,
				Text:         "The second moment lands.",
				Words: []TranscriptWord{
					testWord("w_0007", 45.0, 45.3, "The"),
					testWord("w_0008", 47.0, 47.3, "second"),
					testWord("w_0009", 54.7, 55.0, "lands."),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	first := result.Clips[0].Segments[0]
	if first.StartWordID != "w_0001" || first.EndWordID != "w_0006" || !closeFloat(first.StartSeconds, 10) || !closeFloat(first.EndSeconds, 40) {
		t.Fatalf("expected merged splice word span with shrunken padding, got %+v", first)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected merged splice padding normalization without repair, got %d requests", len(client.requests))
	}
}

func TestOpenRouterClipDetectorUsesDefaultViralPresetWithoutInstructions(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: clipDetectionTestResponse(t,
				testFirstResponseClip("w_0001", "w_0005"),
				testSecondResponseClip("w_0006", "w_0009"),
			),
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{
		Client: client,
		Model:  "provider/clip-model",
	})
	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Title:              "Deep Dive",
		URL:                "https://www.youtube.com/watch?v=deep",
		Uploader:           "Teacher",
		Duration:           time.Minute,
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           basicClipTranscriptSegments(),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(client.requests))
	}
	content := client.requests[0].Messages[1].Content
	if !strings.Contains(content, "viral short-form clips") || strings.Contains(content, `"instructions":""`) {
		t.Fatalf("expected default viral instructions in detector input, got %s", content)
	}
}

func TestOpenRouterClipDetectorRejectsEmptyStructuredOutput(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{Content: "   "},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{
		Client: client,
		Model:  "provider/clip-model",
	})
	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Title:              "Deep Dive",
		URL:                "https://www.youtube.com/watch?v=deep",
		Uploader:           "Teacher",
		Duration:           time.Minute,
		Instructions:       "clip the best moment",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           basicClipTranscriptSegments(),
	})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("expected empty response error, got %v", err)
	}
	if len(client.requests) != maxClipDetectionRepairAttempts+1 {
		t.Fatalf("expected all repair retries after repeated empty responses, got %d request(s)", len(client.requests))
	}
	if client.requests[0].ResponseFormat == nil || client.requests[0].ResponseFormat.Type != "json_schema" {
		t.Fatalf("expected json schema request, got %+v", client.requests[0].ResponseFormat)
	}
	if !strings.Contains(client.requests[1].Messages[0].Content, "failed validation") {
		t.Fatalf("expected repair prompt to include validation context, got %s", client.requests[1].Messages[0].Content)
	}
}

func TestOpenRouterClipDetectorExpandsRepairBudgetForTruncatedJSON(t *testing.T) {
	client := &fakeClipLLM{
		responses: []llm.ChatResponse{
			{Content: `{"clips":[{"rank":1,"title":"Cut off"`, FinishReason: "length"},
			{Content: clipDetectionTestResponse(t, testFirstResponseClip("w_0001", "w_0005"), testSecondResponseClip("w_0006", "w_0009"))},
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{
		Client:    client,
		Model:     "provider/clip-model",
		MaxTokens: 4096,
	})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Title:              "Deep Dive",
		URL:                "https://www.youtube.com/watch?v=deep",
		Uploader:           "Teacher",
		Duration:           time.Minute,
		Instructions:       "clip the best moment",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           basicClipTranscriptSegments(),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(result.Clips) != 2 || result.Clips[0].Title != "Best Moment" {
		t.Fatalf("unexpected repaired result: %+v", result)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected one repair retry, got %d requests", len(client.requests))
	}
	if client.requests[0].MaxTokens != 4096 || client.requests[1].MaxTokens != minClipDetectionRepairTokens {
		t.Fatalf("expected expanded repair budget, got first=%d repair=%d", client.requests[0].MaxTokens, client.requests[1].MaxTokens)
	}
	if !strings.Contains(client.requests[1].Messages[0].Content, "incomplete or truncated") {
		t.Fatalf("expected repair prompt to call out truncation, got %s", client.requests[1].Messages[0].Content)
	}
}

func TestOpenRouterClipDetectorNormalizesContinuousClipWithMultipleSegments(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{Content: clipDetectionTestResponse(t,
			clipDetectionResponseClip{
				Rank:  1,
				Title: "Bad Type",
				Type:  "continuous",
				Segments: []clipDetectionResponseSegment{
					testResponseSegment("w_0001", "w_0003", "The hook lands."),
					testResponseSegment("w_0004", "w_0006", "The payoff lands."),
				},
				Reason:            "Good hook and payoff, wrong type.",
				Confidence:        0.82,
				ViralityScore:     84,
				HookScore:         86,
				RetentionScore:    80,
				ShareabilityScore: 82,
				DurationPolicy:    "short_exception",
				ExceptionReason:   "Standalone soundbite works shorter.",
			},
			testSecondResponseClip("w_0007", "w_0009"),
		)},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments: []TranscriptSegment{
			{
				ID:           "s_0001",
				StartSeconds: 10,
				EndSeconds:   18,
				Text:         "The hook lands.",
				Words: []TranscriptWord{
					testWord("w_0001", 10, 10.3, "The"),
					testWord("w_0002", 12, 12.3, "hook"),
					testWord("w_0003", 17.7, 18, "lands."),
				},
			},
			{
				ID:           "s_0002",
				StartSeconds: 24,
				EndSeconds:   35,
				Text:         "The payoff lands.",
				Words: []TranscriptWord{
					testWord("w_0004", 24, 24.3, "The"),
					testWord("w_0005", 28, 28.3, "payoff"),
					testWord("w_0006", 34.7, 35, "lands."),
				},
			},
			{
				ID:           "s_0003",
				StartSeconds: 40,
				EndSeconds:   55,
				Text:         "The second moment lands.",
				Words: []TranscriptWord{
					testWord("w_0007", 40, 40.3, "The"),
					testWord("w_0008", 45, 45.3, "second"),
					testWord("w_0009", 54.7, 55, "lands."),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(result.Clips) != 2 || result.Clips[0].Type != "spliced" || len(result.Clips[0].Segments) != 2 {
		t.Fatalf("expected normalized spliced clip, got %+v", result)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected metadata normalization without repair retry, got %d requests", len(client.requests))
	}
}

func TestOpenRouterClipDetectorAllowsModelChosenWeakBoundaryWords(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{Content: clipDetectionTestResponse(t,
			testFirstResponseClip("w_0002", "w_0005"),
			testSecondResponseClip("w_0006", "w_0009"),
		)},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments: []TranscriptSegment{
			{
				ID:           "s_0001",
				StartSeconds: 8,
				EndSeconds:   25,
				Text:         "Okay. And This moment lands cleanly.",
				Words: []TranscriptWord{
					testWord("w_0001", 8.0, 8.2, "Okay."),
					testWord("w_0002", 10.0, 10.2, "And"),
					testWord("w_0003", 12.0, 12.2, "This"),
					testWord("w_0004", 18.0, 18.2, "moment"),
					testWord("w_0005", 24.8, 25.0, "cleanly."),
				},
			},
			{
				ID:           "s_0002",
				StartSeconds: 30,
				EndSeconds:   50,
				Text:         "The second moment lands.",
				Words: []TranscriptWord{
					testWord("w_0006", 30.0, 30.2, "The"),
					testWord("w_0007", 35.0, 35.2, "second"),
					testWord("w_0008", 42.0, 42.2, "moment"),
					testWord("w_0009", 49.8, 50.0, "lands."),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got := result.Clips[0].Segments[0].StartWordID; got != "w_0002" {
		t.Fatalf("expected model-chosen start word, got %s", got)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected weak boundary acceptance without repair retry, got %d requests", len(client.requests))
	}
}

func TestOpenRouterClipDetectorAllowsModelChosenMidSentenceBoundaries(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{Content: clipDetectionTestResponse(t,
			testFirstResponseClip("w_0002", "w_0004"),
			testSecondResponseClip("w_0006", "w_0009"),
		)},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})

	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments: []TranscriptSegment{
			{
				ID:           "s_0001",
				StartSeconds: 10,
				EndSeconds:   16,
				Text:         "The best moment starts here.",
				Words: []TranscriptWord{
					testWord("w_0001", 10.00, 10.20, "The"),
					testWord("w_0002", 10.25, 10.45, "best"),
					testWord("w_0003", 12.50, 12.75, "moment"),
					testWord("w_0004", 14.80, 15.00, "starts"),
					testWord("w_0005", 15.10, 16.00, "here."),
				},
			},
			{
				ID:           "s_0002",
				StartSeconds: 30,
				EndSeconds:   50,
				Text:         "The second moment lands.",
				Words: []TranscriptWord{
					testWord("w_0006", 30.0, 30.2, "The"),
					testWord("w_0007", 35.0, 35.2, "second"),
					testWord("w_0008", 42.0, 42.2, "moment"),
					testWord("w_0009", 49.8, 50.0, "lands."),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	first := result.Clips[0].Segments[0]
	if first.StartWordID != "w_0002" || first.EndWordID != "w_0004" {
		t.Fatalf("expected model-chosen boundaries, got %+v", first)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected mid-sentence boundary acceptance without repair retry, got %d requests", len(client.requests))
	}
}

func TestOpenRouterClipDetectorTimesOutHungModelClient(t *testing.T) {
	client := &blockingClipLLM{release: make(chan struct{})}
	defer close(client.release)
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{
		Client:  client,
		Model:   "provider/clip-model",
		Timeout: time.Millisecond,
	})
	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           basicClipTranscriptSegments(),
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(client.requests))
	}
}

func TestOpenRouterClipDetectorRejectsTranscriptWithoutWordTiming(t *testing.T) {
	client := &fakeClipLLM{}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           []TranscriptSegment{{StartSeconds: 0, EndSeconds: 50, Text: "content"}},
	})
	if err == nil || !strings.Contains(err.Error(), "word timing is required") {
		t.Fatalf("expected word timing error, got %v", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("detector should not call model without word timing, got %d requests", len(client.requests))
	}
}

func TestOpenRouterClipDetectorAllowsSingleRenderableClip(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: clipDetectionTestResponse(t, testFirstResponseClip("w_0001", "w_0005")),
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           basicClipTranscriptSegments(),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(result.Clips) != 1 || result.Clips[0].Rank != 1 {
		t.Fatalf("expected one normalized clip, got %+v", result)
	}
}

func TestOpenRouterClipDetectorSkipsClipWithMissingWordIDs(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: `{"clips":[{"rank":1,"title":"Bad Boundary","type":"continuous","segments":[{"transcript":"This lacks word IDs.","boundary_reason":"Missing IDs should not be guessed."}],"reason":"Missing IDs should not be guessed.","confidence":0.8,"virality_score":82,"hook_score":85,"retention_score":80,"shareability_score":81,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."},{"rank":2,"title":"Also Bad","type":"continuous","segments":[{"start_word_id":"w_0006","end_word_id":"w_0009","transcript":"This one is valid.","boundary_reason":"Clean boundaries."}],"reason":"Second clip.","confidence":0.75,"virality_score":76,"hook_score":78,"retention_score":74,"shareability_score":75,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."}]}`,
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           basicClipTranscriptSegments(),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(result.Clips) != 1 || result.Clips[0].Rank != 1 || result.Clips[0].Segments[0].StartWordID != "w_0006" {
		t.Fatalf("expected valid clip to survive missing ID candidate, got %+v", result)
	}
}

func TestOpenRouterClipDetectorIgnoresLegacyTimingFields(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: `{"clips":[{"rank":1,"title":"Bad","type":"continuous","segments":[{"start_word_id":"w_0001","end_word_id":"w_0005","start_seconds":10,"end_seconds":20,"transcript":"bad","boundary_reason":"Bad legacy fields."}],"reason":"bad","confidence":0.5,"virality_score":20,"hook_score":10,"retention_score":20,"shareability_score":20,"duration_policy":"other","exception_reason":""},{"rank":2,"title":"Valid","type":"continuous","segments":[{"start_word_id":"w_0006","end_word_id":"w_0009","transcript":"valid","boundary_reason":"Clean boundaries."}],"reason":"valid","confidence":0.7,"virality_score":70,"hook_score":70,"retention_score":70,"shareability_score":70,"duration_policy":"short_exception","exception_reason":"short standalone"}]}`,
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	result, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           basicClipTranscriptSegments(),
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(result.Clips) != 2 || result.Clips[0].Segments[0].StartWordID != "w_0001" {
		t.Fatalf("expected legacy fields to be ignored, got %+v", result)
	}
}

func clipDetectionTestResponse(t *testing.T, clips ...clipDetectionResponseClip) string {
	t.Helper()
	data, err := json.Marshal(clipDetectionResponse{Clips: clips})
	if err != nil {
		t.Fatalf("marshal clip detection response: %v", err)
	}
	return string(data)
}

func testResponseSegment(startWordID string, endWordID string, transcript string) clipDetectionResponseSegment {
	return clipDetectionResponseSegment{
		StartWordID:    startWordID,
		EndWordID:      endWordID,
		Transcript:     transcript,
		BoundaryReason: "The selected words start and end on a complete thought.",
	}
}

func testFirstResponseClip(startWordID string, endWordID string) clipDetectionResponseClip {
	return clipDetectionResponseClip{
		Rank:  1,
		Title: "Best Moment",
		Type:  "continuous",
		Segments: []clipDetectionResponseSegment{
			testResponseSegment(startWordID, endWordID, "The best moment starts here."),
		},
		Reason:            "Strong standalone moment.",
		Confidence:        0.8,
		ViralityScore:     82,
		HookScore:         85,
		RetentionScore:    80,
		ShareabilityScore: 81,
		DurationPolicy:    "short_exception",
		ExceptionReason:   "Standalone soundbite works shorter.",
	}
}

func testSecondResponseClip(startWordID string, endWordID string) clipDetectionResponseClip {
	return clipDetectionResponseClip{
		Rank:  2,
		Title: "Second Moment",
		Type:  "continuous",
		Segments: []clipDetectionResponseSegment{
			testResponseSegment(startWordID, endWordID, "The second moment lands."),
		},
		Reason:            "Second viable clip from the same video.",
		Confidence:        0.75,
		ViralityScore:     76,
		HookScore:         78,
		RetentionScore:    74,
		ShareabilityScore: 75,
		DurationPolicy:    "short_exception",
		ExceptionReason:   "Standalone soundbite works shorter.",
	}
}

func testWord(id string, start float64, end float64, text string) TranscriptWord {
	return TranscriptWord{
		ID:           id,
		StartSeconds: start,
		EndSeconds:   end,
		Text:         text,
	}
}

func hasClipBoundaryOption(options []clipDetectionBoundaryOptionInput, wordID string) bool {
	for _, option := range options {
		if option.WordID == wordID {
			return true
		}
	}
	return false
}

func forcedSplitBackgroundTranscriptSegments() []TranscriptSegment {
	return []TranscriptSegment{
		{
			ID:           "s_0001",
			StartSeconds: 300,
			EndSeconds:   308,
			Text:         "there are times where the game audio and the music still sound like they might have playing in background while you doomscroll on stream.",
			Words: []TranscriptWord{
				testWord("w_0941", 300.00, 300.20, "there"),
				testWord("w_0942", 300.30, 300.50, "are"),
				testWord("w_0943", 300.60, 300.80, "times"),
				testWord("w_0944", 300.90, 301.10, "where"),
				testWord("w_0945", 301.20, 301.40, "the"),
				testWord("w_0946", 301.50, 301.70, "game"),
				testWord("w_0947", 301.80, 302.00, "audio"),
				testWord("w_0948", 302.10, 302.30, "and"),
				testWord("w_0949", 302.40, 302.60, "the"),
				testWord("w_0950", 302.70, 302.90, "music"),
				testWord("w_0951", 303.00, 303.20, "still"),
				testWord("w_0952", 303.30, 303.50, "sound"),
				testWord("w_0953", 303.60, 303.80, "like"),
				testWord("w_0954", 303.90, 304.10, "they"),
				testWord("w_0955", 304.20, 304.40, "might"),
				testWord("w_0956", 304.50, 304.70, "have"),
				testWord("w_0957", 304.80, 305.00, "playing"),
				testWord("w_0958", 305.10, 305.30, "in"),
				testWord("w_0959", 305.40, 305.60, "background"),
				testWord("w_0960", 305.70, 305.90, "while"),
				testWord("w_0961", 306.00, 306.20, "you"),
				testWord("w_0962", 306.30, 306.50, "doomscroll"),
				testWord("w_0963", 306.60, 306.80, "on"),
				testWord("w_0964", 306.90, 307.20, "stream."),
			},
		},
		{
			ID:           "s_0002",
			StartSeconds: 310,
			EndSeconds:   314,
			Text:         "Another clean moment lands.",
			Words: []TranscriptWord{
				testWord("w_0965", 310.00, 310.30, "Another"),
				testWord("w_0966", 311.00, 311.30, "clean"),
				testWord("w_0967", 312.00, 312.30, "moment"),
				testWord("w_0968", 313.60, 314.00, "lands."),
			},
		},
	}
}

func oversizedClipTranscriptSegments(segmentCount int) []TranscriptSegment {
	segments := make([]TranscriptSegment, 0, segmentCount)
	wordIndex := 1
	for segmentIndex := 0; segmentIndex < segmentCount; segmentIndex++ {
		start := float64(segmentIndex * 8)
		words := []TranscriptWord{
			testWord(fmt.Sprintf("w_%04d", wordIndex), start, start+1.0, "Moment"),
			testWord(fmt.Sprintf("w_%04d", wordIndex+1), start+1.1, start+2.1, "number"),
			testWord(fmt.Sprintf("w_%04d", wordIndex+2), start+2.2, start+3.2, fmt.Sprintf("%03d", segmentIndex+1)),
			testWord(fmt.Sprintf("w_%04d", wordIndex+3), start+3.3, start+4.3, "lands"),
			testWord(fmt.Sprintf("w_%04d", wordIndex+4), start+4.4, start+6.4, "cleanly."),
		}
		wordIndex += len(words)
		segments = append(segments, TranscriptSegment{
			ID:           fmt.Sprintf("s_%04d", segmentIndex+1),
			StartSeconds: start,
			EndSeconds:   start + 6.4,
			Text:         strings.Repeat(fmt.Sprintf("Segment %03d has a strong standalone clip moment with useful context. ", segmentIndex+1), 24),
			Words:        words,
		})
	}
	return segments
}

func basicClipTranscriptSegments() []TranscriptSegment {
	return []TranscriptSegment{
		{
			ID:           "s_0001",
			StartSeconds: 10,
			EndSeconds:   30,
			Text:         "The best moment starts here.",
			Words: []TranscriptWord{
				testWord("w_0001", 10.0, 10.3, "The"),
				testWord("w_0002", 14.0, 14.3, "best"),
				testWord("w_0003", 18.0, 18.3, "moment"),
				testWord("w_0004", 24.0, 24.3, "starts"),
				testWord("w_0005", 29.7, 30.0, "here."),
			},
		},
		{
			ID:           "s_0002",
			StartSeconds: 32,
			EndSeconds:   48,
			Text:         "The second moment lands.",
			Words: []TranscriptWord{
				testWord("w_0006", 32.0, 32.3, "The"),
				testWord("w_0007", 36.0, 36.3, "second"),
				testWord("w_0008", 42.0, 42.3, "moment"),
				testWord("w_0009", 47.7, 48.0, "lands."),
			},
		},
	}
}

func renderPaddingSpliceTranscriptSegments(secondStart float64) []TranscriptSegment {
	return []TranscriptSegment{
		{
			ID:           "s_0001",
			StartSeconds: 10,
			EndSeconds:   secondStart + 1.70,
			Text:         "The setup lands. The payoff lands.",
			Words: []TranscriptWord{
				testWord("w_0001", 10.00, 10.20, "The"),
				testWord("w_0002", 10.40, 10.60, "setup"),
				testWord("w_0003", 10.80, 11.00, "lands."),
				testWord("w_0004", secondStart, secondStart+0.20, "The"),
				testWord("w_0005", secondStart+0.60, secondStart+0.80, "payoff"),
				testWord("w_0006", secondStart+1.50, secondStart+1.70, "lands."),
			},
		},
		{
			ID:           "s_0002",
			StartSeconds: 20,
			EndSeconds:   25,
			Text:         "The second moment lands.",
			Words: []TranscriptWord{
				testWord("w_0007", 20.0, 20.3, "The"),
				testWord("w_0008", 22.0, 22.3, "second"),
				testWord("w_0009", 24.7, 25.0, "lands."),
			},
		},
	}
}

func testClipTranscriptSegments() []TranscriptSegment {
	return []TranscriptSegment{
		{
			ID:           "s_0001",
			StartSeconds: 10,
			EndSeconds:   12,
			Text:         "The important part starts here.",
			Words: []TranscriptWord{
				testWord("w_0001", 10.0, 10.2, "The"),
				testWord("w_0002", 10.3, 10.6, "important"),
				testWord("w_0003", 10.7, 11.0, "part"),
				testWord("w_0004", 11.1, 11.5, "starts"),
				testWord("w_0005", 11.6, 12.0, "here."),
			},
		},
		{
			ID:           "s_0002",
			StartSeconds: 12,
			EndSeconds:   18,
			Text:         "The setup gets sharper.",
			Words: []TranscriptWord{
				testWord("w_0006", 12.0, 12.3, "The"),
				testWord("w_0007", 13.0, 13.4, "setup"),
				testWord("w_0008", 15.0, 15.3, "gets"),
				testWord("w_0009", 17.6, 18.0, "sharper."),
			},
		},
		{
			ID:           "s_0003",
			StartSeconds: 21,
			EndSeconds:   27,
			Text:         "Payoff lands cleanly.",
			Words: []TranscriptWord{
				testWord("w_0010", 21.0, 21.3, "Payoff"),
				testWord("w_0011", 23.0, 23.3, "lands"),
				testWord("w_0012", 25.0, 25.3, "so"),
				testWord("w_0013", 26.6, 27.0, "cleanly."),
			},
		},
		{
			ID:           "s_0004",
			StartSeconds: 30,
			EndSeconds:   42,
			Text:         "A longer version with setup.",
			Words: []TranscriptWord{
				testWord("w_0014", 30.0, 30.3, "A"),
				testWord("w_0015", 32.0, 32.3, "longer"),
				testWord("w_0016", 35.0, 35.3, "version"),
				testWord("w_0017", 41.6, 42.0, "setup."),
			},
		},
		{
			ID:           "s_0005",
			StartSeconds: 42,
			EndSeconds:   55,
			Text:         "The fuller context lands cleanly.",
			Words: []TranscriptWord{
				testWord("w_0018", 42.0, 42.3, "The"),
				testWord("w_0019", 45.0, 45.3, "fuller"),
				testWord("w_0020", 50.0, 50.3, "context"),
				testWord("w_0021", 54.6, 55.0, "cleanly."),
			},
		},
	}
}
