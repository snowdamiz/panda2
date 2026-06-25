package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenRouterImageGenerateSendsExpectedRequest(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	var sawCapabilities atomic.Bool
	var sawImages atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/models/google/gemini-3.1-flash-image/endpoints":
			sawCapabilities.Store(true)
			if r.Method != http.MethodGet {
				t.Fatalf("expected capability GET, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("unexpected auth header %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "google/gemini-3.1-flash-image",
				"endpoints": []map[string]any{{
					"provider_name": "Google",
					"supported_parameters": map[string]any{
						"aspect_ratio":  map[string]any{"type": "enum", "values": []string{"1:1", "16:9"}},
						"resolution":    map[string]any{"type": "enum", "values": []string{"512", "1K"}},
						"output_format": map[string]any{"type": "enum", "values": []string{"png", "jpeg"}},
					},
				}},
			})
		case "/images":
			sawImages.Store(true)
			if r.Method != http.MethodPost {
				t.Fatalf("expected image POST, got %s", r.Method)
			}
			if got := r.Header.Get("HTTP-Referer"); got != "https://example.test" {
				t.Fatalf("unexpected referer %q", got)
			}
			var payload imageCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if payload.Model != "google/gemini-3.1-flash-image" || payload.Prompt != "pixel panda icon" {
				t.Fatalf("unexpected image payload: %+v", payload)
			}
			if payload.AspectRatio != "1:1" || payload.Resolution != "1K" || payload.OutputFormat != "png" || payload.N != 0 {
				t.Fatalf("unexpected image settings: %+v", payload)
			}
			if len(payload.InputReferences) != 1 || payload.InputReferences[0].Type != "image_url" || payload.InputReferences[0].ImageURL.URL != "https://cdn.example.test/ref.png" {
				t.Fatalf("unexpected input references: %+v", payload.InputReferences)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(pngBytes)}},
				"usage": map[string]any{
					"prompt_tokens":     5,
					"completion_tokens": 2,
					"total_tokens":      7,
					"cost":              0.012345,
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewOpenRouterImageClient(OpenRouterImageConfig{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		AppURL:   "https://example.test",
		AppTitle: "Panda Tests",
	})
	response, err := client.Generate(context.Background(), ImageGenerationRequest{
		Prompt: "pixel panda icon",
		InputReferences: []ImageInputReference{{
			URL: "https://cdn.example.test/ref.png",
		}},
		AspectRatio:  "1:1",
		Resolution:   "1K",
		OutputFormat: "png",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sawCapabilities.Load() || !sawImages.Load() {
		t.Fatalf("expected capabilities and image endpoints to be called")
	}
	if len(response.Images) != 1 || string(response.Images[0].Bytes) != string(pngBytes) || response.Images[0].MIMEType != "image/png" {
		t.Fatalf("unexpected generated image: %+v", response.Images)
	}
	if response.Usage.PromptTokens != 5 || response.Usage.TotalTokens != 7 || response.Usage.CostMicros != 12345 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestOpenRouterImageAnalyzeSendsMultimodalChatRequest(t *testing.T) {
	var sawChat atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		sawChat.Store(true)
		if r.Method != http.MethodPost {
			t.Fatalf("expected chat POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		if got := r.Header.Get("X-OpenRouter-Title"); got != "Panda Tests" {
			t.Fatalf("unexpected title header %q", got)
		}
		var payload imageChatRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Model != "google/gemini-3.1-flash-image" || payload.MaxTokens != 600 {
			t.Fatalf("unexpected chat payload settings: %+v", payload)
		}
		if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" {
			t.Fatalf("unexpected chat messages: %+v", payload.Messages)
		}
		parts := payload.Messages[0].Content
		if len(parts) != 2 || parts[0].Type != "text" || !strings.Contains(parts[0].Text, "What is in this image?") {
			t.Fatalf("unexpected multimodal text part: %+v", parts)
		}
		if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://cdn.example.test/ref.png" {
			t.Fatalf("unexpected multimodal image part: %+v", parts[1])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "gen-vision",
			"model": "google/gemini-3.1-flash-image",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": "The image shows a panda icon."},
			}},
			"usage": map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 8,
				"total_tokens":      20,
				"cost":              0.000321,
			},
		})
	}))
	defer server.Close()

	client := NewOpenRouterImageClient(OpenRouterImageConfig{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		AppTitle: "Panda Tests",
	})
	response, err := client.Analyze(context.Background(), ImageAnalysisRequest{
		Prompt: "What is in this image?",
		InputReferences: []ImageInputReference{{
			URL: "https://cdn.example.test/ref.png",
		}},
		MaxTokens: 600,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !sawChat.Load() {
		t.Fatalf("expected chat endpoint to be called")
	}
	if response.Content != "The image shows a panda icon." || response.Usage.TotalTokens != 20 || response.Usage.CostMicros != 321 {
		t.Fatalf("unexpected analysis response: %+v", response)
	}
}

func TestOpenRouterImageGenerateClassifiesProviderPolicyErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "policy_blocked", "message": "blocked by safety policy"},
		})
	}))
	defer server.Close()

	client := NewOpenRouterImageClient(OpenRouterImageConfig{APIKey: "test-key", BaseURL: server.URL})
	_, err := client.Generate(context.Background(), ImageGenerationRequest{Prompt: "unsafe prompt"})
	var imageErr ImageGenerationError
	if !errors.As(err, &imageErr) || imageErr.Status != ImageProviderStatusPolicyBlocked {
		t.Fatalf("expected policy blocked image error, got %#v", err)
	}
	if !stringsContain(imageErr.Message, "safety policy") {
		t.Fatalf("expected safe policy message, got %q", imageErr.Message)
	}
}

func TestOpenRouterImageGenerateRejectsInvalidBase64(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": "%%%"}},
		})
	}))
	defer server.Close()

	client := NewOpenRouterImageClient(OpenRouterImageConfig{APIKey: "test-key", BaseURL: server.URL})
	_, err := client.Generate(context.Background(), ImageGenerationRequest{Prompt: "pixel icon"})
	var imageErr ImageGenerationError
	if !errors.As(err, &imageErr) || imageErr.Status != ImageProviderStatusError || !errors.Is(err, ErrImageInvalidResponse) {
		t.Fatalf("expected invalid response image error, got %#v", err)
	}
}

func TestOpenRouterImageGenerateRejectsUnsupportedParametersBeforeSpend(t *testing.T) {
	var imageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/models/google/gemini-3.1-flash-image/endpoints":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"endpoints": []map[string]any{{
					"supported_parameters": map[string]any{
						"aspect_ratio": map[string]any{"type": "enum", "values": []string{"1:1"}},
					},
				}},
			})
		case "/images":
			imageCalls.Add(1)
			t.Fatalf("image endpoint should not be called for unsupported parameters")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewOpenRouterImageClient(OpenRouterImageConfig{APIKey: "test-key", BaseURL: server.URL})
	_, err := client.Generate(context.Background(), ImageGenerationRequest{Prompt: "poster", AspectRatio: "9:16"})
	var imageErr ImageGenerationError
	if !errors.As(err, &imageErr) || imageErr.Status != ImageProviderStatusInvalid {
		t.Fatalf("expected invalid image setting error, got %#v", err)
	}
	if imageCalls.Load() != 0 {
		t.Fatalf("expected no image generation calls, got %d", imageCalls.Load())
	}
}

func TestOpenRouterImageGenerateRequiresConfig(t *testing.T) {
	client := NewOpenRouterImageClient(OpenRouterImageConfig{BaseURL: "http://127.0.0.1"})
	_, err := client.Generate(context.Background(), ImageGenerationRequest{Prompt: "icon"})
	var imageErr ImageGenerationError
	if !errors.As(err, &imageErr) || imageErr.Status != ImageProviderStatusUnavailable || !errors.Is(err, ErrImageNotConfigured) {
		t.Fatalf("expected not configured image error, got %#v", err)
	}
}

func TestOpenRouterImageGenerateRejectsInvalidInputReferenceURL(t *testing.T) {
	client := NewOpenRouterImageClient(OpenRouterImageConfig{APIKey: "test-key", BaseURL: "http://127.0.0.1"})
	_, err := client.Generate(context.Background(), ImageGenerationRequest{
		Prompt:          "pixel icon",
		InputReferences: []ImageInputReference{{URL: "ftp://example.test/image.png"}},
	})
	var imageErr ImageGenerationError
	if !errors.As(err, &imageErr) || imageErr.Status != ImageProviderStatusInvalid {
		t.Fatalf("expected invalid input reference URL error, got %#v", err)
	}
}

func TestOpenRouterImageAnalyzeRejectsInvalidInputReferenceURL(t *testing.T) {
	client := NewOpenRouterImageClient(OpenRouterImageConfig{APIKey: "test-key", BaseURL: "http://127.0.0.1"})
	_, err := client.Analyze(context.Background(), ImageAnalysisRequest{
		Prompt:          "What is this?",
		InputReferences: []ImageInputReference{{URL: "ftp://example.test/image.png"}},
	})
	var imageErr ImageGenerationError
	if !errors.As(err, &imageErr) || imageErr.Status != ImageProviderStatusInvalid {
		t.Fatalf("expected invalid input reference URL error, got %#v", err)
	}
}

func stringsContain(value, fragment string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(fragment))
}
