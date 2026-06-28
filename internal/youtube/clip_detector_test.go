package youtube

import (
	"context"
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

type blockingClipLLM struct {
	requests []llm.ChatRequest
	release  chan struct{}
}

func (f *blockingClipLLM) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.requests = append(f.requests, request)
	<-f.release
	return llm.ChatResponse{}, nil
}

func TestOpenRouterClipDetectorUsesConfiguredModelAndJSONSchemaPrompt(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: `{"clips":[{"rank":1,"title":"Best Moment","type":"spliced","segments":[{"start_segment_index":0,"end_segment_index":1,"start_seconds":11.2,"end_seconds":17.4,"transcript":"important part starts here"},{"start_segment_index":2,"end_segment_index":2,"start_seconds":21.3,"end_seconds":26.7,"transcript":"Then the payoff lands."}],"reason":"Strong hook and payoff without the pause","confidence":0.8,"virality_score":82,"hook_score":88,"retention_score":76,"shareability_score":83,"duration_policy":"requested_duration","exception_reason":""},{"rank":2,"title":"Full Context","type":"continuous","segments":[{"start_segment_index":3,"end_segment_index":4,"start_seconds":30.5,"end_seconds":54.5,"transcript":"A longer version with setup."}],"reason":"Longer version with setup","confidence":0.72,"virality_score":75,"hook_score":72,"retention_score":74,"shareability_score":78,"duration_policy":"requested_duration","exception_reason":""}]}`,
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
		Segments: []TranscriptSegment{
			{StartSeconds: 10, EndSeconds: 12, Text: "The important part starts here."},
			{StartSeconds: 12, EndSeconds: 18, Text: "The setup gets sharper."},
			{StartSeconds: 21, EndSeconds: 27, Text: "Then the payoff lands."},
			{StartSeconds: 30, EndSeconds: 42, Text: "A longer version with setup."},
			{StartSeconds: 42, EndSeconds: 55, Text: "The fuller context lands cleanly."},
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(decision.Clips) != 2 || decision.Clips[0].Title != "Best Moment" || decision.Clips[0].Type != "spliced" || len(decision.Clips[0].Segments) != 2 || decision.Clips[0].Segments[0].StartSeconds != 10 || decision.Clips[0].Segments[0].EndSeconds != 18 || decision.Clips[0].Segments[1].EndSeconds != 27 {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if decision.Clips[0].Segments[0].Transcript != "The important part starts here. The setup gets sharper." {
		t.Fatalf("expected aligned transcript text, got %q", decision.Clips[0].Segments[0].Transcript)
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
	for _, want := range []string{`supplied strict JSON schema`, "atomic cut units", "start_segment_index", "Do not invent interior timestamps", "Every clip must make sense when watched alone", "prepositions"} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("expected schema prompt to contain %s, got %s", want, systemPrompt)
		}
	}
	schema := string(request.ResponseFormat.JSONSchema.Schema)
	for _, want := range []string{`"clips"`, `"segments"`, `"start_segment_index"`, `"end_segment_index"`, `"virality_score"`, `"duration_policy"`, `"additionalProperties":false`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("expected response schema to contain %s, got %s", want, schema)
		}
	}
	for _, unsupported := range []string{`"exclusiveMinimum"`, `"minimum"`, `"maximum"`, `"minItems"`, `"maxItems"`, `"minLength"`, `"maxLength"`} {
		if strings.Contains(schema, unsupported) {
			t.Fatalf("clip detection schema should avoid provider-fragile keyword %s: %s", unsupported, schema)
		}
	}
	if strings.Contains(schema, `"total_duration_seconds"`) {
		t.Fatalf("schema should not include derived total duration: %s", schema)
	}
	if len(request.Tools) != 0 {
		t.Fatalf("clip detection should not expose tools, got %+v", request.Tools)
	}
	if !strings.Contains(systemPrompt, "at least 2") || !strings.Contains(systemPrompt, "max_clips") || !strings.Contains(request.Messages[1].Content, "clip the key explanation") || !strings.Contains(request.Messages[1].Content, "transcript_segments") || !strings.Contains(request.Messages[1].Content, `"index":0`) || !strings.Contains(request.Messages[1].Content, `"min_clips":2`) || !strings.Contains(request.Messages[1].Content, `"max_clips":12`) {
		t.Fatalf("expected structured detector input, got %s", request.Messages[1].Content)
	}
}

func TestOpenRouterClipDetectorUsesDefaultViralPresetWithoutInstructions(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: `{"clips":[{"rank":1,"title":"Best Moment","type":"continuous","segments":[{"start_segment_index":0,"end_segment_index":0,"start_seconds":10,"end_seconds":30,"transcript":"The best moment starts here."}],"reason":"Strong standalone moment","confidence":0.8,"virality_score":82,"hook_score":85,"retention_score":80,"shareability_score":81,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."},{"rank":2,"title":"Second Moment","type":"continuous","segments":[{"start_segment_index":1,"end_segment_index":1,"start_seconds":32,"end_seconds":48,"transcript":"Another strong moment follows."}],"reason":"Second viable clip from the same video.","confidence":0.75,"virality_score":76,"hook_score":78,"retention_score":74,"shareability_score":75,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."}]}`,
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
		Segments: []TranscriptSegment{
			{StartSeconds: 10, EndSeconds: 30, Text: "The best moment starts here."},
			{StartSeconds: 32, EndSeconds: 48, Text: "Another strong moment follows."},
		},
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
		Segments: []TranscriptSegment{{
			StartSeconds: 0,
			EndSeconds:   50,
			Text:         "The best moment starts here.",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("expected empty response error, got %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one structured-output request, got %d request(s)", len(client.requests))
	}
	if client.requests[0].ResponseFormat == nil || client.requests[0].ResponseFormat.Type != "json_schema" {
		t.Fatalf("expected json schema request, got %+v", client.requests[0].ResponseFormat)
	}
}

func TestOpenRouterClipDetectorExpandsRepairBudgetForTruncatedJSON(t *testing.T) {
	client := &fakeClipLLM{
		responses: []llm.ChatResponse{
			{Content: `{"clips":[{"rank":1,"title":"Cut off"`, FinishReason: "length"},
			{Content: `{"clips":[{"rank":1,"title":"Best Moment","type":"continuous","segments":[{"start_segment_index":0,"end_segment_index":0,"start_seconds":10,"end_seconds":25,"transcript":"The best moment starts here."}],"reason":"Strong standalone moment.","confidence":0.82,"virality_score":84,"hook_score":86,"retention_score":80,"shareability_score":82,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."},{"rank":2,"title":"Second Moment","type":"continuous","segments":[{"start_segment_index":1,"end_segment_index":1,"start_seconds":30,"end_seconds":50,"transcript":"The second moment lands."}],"reason":"Second viable standalone moment.","confidence":0.76,"virality_score":77,"hook_score":78,"retention_score":76,"shareability_score":75,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."}]}`},
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
		Segments: []TranscriptSegment{
			{StartSeconds: 10, EndSeconds: 25, Text: "The best moment starts here."},
			{StartSeconds: 30, EndSeconds: 50, Text: "The second moment lands."},
		},
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
		Segments:           []TranscriptSegment{{StartSeconds: 0, EndSeconds: 50, Text: "content"}},
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(client.requests))
	}
}

func TestOpenRouterClipDetectorRejectsSingleClip(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: `{"clips":[{"rank":1,"title":"Only Moment","type":"continuous","segments":[{"start_segment_index":0,"end_segment_index":0,"start_seconds":10,"end_seconds":30,"transcript":"The only moment."}],"reason":"Only one clip returned.","confidence":0.8,"virality_score":82,"hook_score":85,"retention_score":80,"shareability_score":81,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."}]}`,
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           []TranscriptSegment{{StartSeconds: 0, EndSeconds: 50, Text: "content"}},
	})
	if err == nil || !strings.Contains(err.Error(), "minimum is 2") {
		t.Fatalf("expected minimum clips error, got %v", err)
	}
}

func TestOpenRouterClipDetectorRejectsMissingTranscriptIndexes(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: `{"clips":[{"rank":1,"title":"Bad Boundary","type":"continuous","segments":[{"start_seconds":10,"end_seconds":20,"transcript":"This lacks indexes."}],"reason":"Missing indexes should not be guessed.","confidence":0.8,"virality_score":82,"hook_score":85,"retention_score":80,"shareability_score":81,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."},{"rank":2,"title":"Also Bad","type":"continuous","segments":[{"start_seconds":22,"end_seconds":32,"transcript":"This also lacks indexes."}],"reason":"Missing indexes should not be guessed.","confidence":0.75,"virality_score":76,"hook_score":78,"retention_score":74,"shareability_score":75,"duration_policy":"short_exception","exception_reason":"Standalone soundbite works shorter."}]}`,
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments: []TranscriptSegment{
			{StartSeconds: 10, EndSeconds: 20, Text: "This lacks indexes."},
			{StartSeconds: 22, EndSeconds: 32, Text: "This also lacks indexes."},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing transcript segment indexes") {
		t.Fatalf("expected missing index error, got %v", err)
	}
}

func TestOpenRouterClipDetectorRejectsInvalidJSONOutput(t *testing.T) {
	client := &fakeClipLLM{
		response: llm.ChatResponse{
			Content: `{"clips":[{"rank":1,"title":"Bad","type":"continuous","segments":[{"start_segment_index":1,"end_segment_index":0,"start_seconds":40,"end_seconds":20,"transcript":"bad"}],"reason":"bad","confidence":0.5,"virality_score":20,"hook_score":10,"retention_score":20,"shareability_score":20,"duration_policy":"other","exception_reason":""},{"rank":2,"title":"Valid","type":"continuous","segments":[{"start_segment_index":0,"end_segment_index":0,"start_seconds":10,"end_seconds":20,"transcript":"valid"}],"reason":"valid","confidence":0.7,"virality_score":70,"hook_score":70,"retention_score":70,"shareability_score":70,"duration_policy":"short_exception","exception_reason":"short standalone"}]}`,
		},
	}
	detector := NewOpenRouterClipDetector(ClipDetectorConfig{Client: client, Model: "provider/clip-model"})
	_, err := detector.Detect(context.Background(), ClipDetectionRequest{
		Duration:           time.Minute,
		Instructions:       "clip something",
		MinDurationSeconds: 5,
		MaxDurationSeconds: 30,
		Segments:           []TranscriptSegment{{StartSeconds: 0, EndSeconds: 25, Text: "valid"}, {StartSeconds: 30, EndSeconds: 50, Text: "bad"}},
	})
	if err == nil || !strings.Contains(err.Error(), "end_segment_index") {
		t.Fatalf("expected invalid JSON output error, got %v", err)
	}
}
