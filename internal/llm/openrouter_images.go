package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultImageModel    = "google/gemini-3.1-flash-image"
	defaultImageMaxBytes = int64(8 * 1024 * 1024)
)

type ImageProviderStatus string

const (
	ImageProviderStatusSuccess       ImageProviderStatus = "success"
	ImageProviderStatusPolicyBlocked ImageProviderStatus = "policy_blocked"
	ImageProviderStatusInvalid       ImageProviderStatus = "invalid_request"
	ImageProviderStatusRateLimited   ImageProviderStatus = "rate_limited"
	ImageProviderStatusUnavailable   ImageProviderStatus = "unavailable"
	ImageProviderStatusError         ImageProviderStatus = "error"
)

var (
	ErrImageNotConfigured       = errors.New("openrouter image generation is not configured")
	ErrImageUnsupportedArgument = errors.New("unsupported image generation argument")
	ErrImageInvalidResponse     = errors.New("openrouter image response is invalid")
	ErrImageTooLarge            = errors.New("generated image exceeds the configured size limit")
)

type OpenRouterImageClient struct {
	apiKey         string
	baseURL        string
	model          string
	appURL         string
	appTitle       string
	client         *http.Client
	maxRetries     int
	retryDelay     time.Duration
	maxBytes       int64
	circuitBreaker circuitBreaker

	capabilityMu    sync.Mutex
	capabilityByKey map[string]imageSupportedParameters
}

type OpenRouterImageConfig struct {
	APIKey                         string
	BaseURL                        string
	Model                          string
	AppURL                         string
	AppTitle                       string
	Timeout                        time.Duration
	MaxRetries                     int
	RetryDelay                     time.Duration
	MaxBytes                       int64
	CircuitBreakerFailureThreshold int
	CircuitBreakerCooldown         time.Duration
}

type ImageGenerationRequest struct {
	Model                 string
	Prompt                string
	InputReferences       []ImageInputReference
	AspectRatio           string
	Resolution            string
	Size                  string
	Quality               string
	OutputFormat          string
	TransparentBackground bool
	Count                 int
}

type ImageAnalysisRequest struct {
	Model           string
	Prompt          string
	InputReferences []ImageInputReference
	MaxTokens       int
}

type ImageInputReference struct {
	URL string
}

type ImageGenerationResponse struct {
	Model  string
	Images []GeneratedImage
	Usage  ImageUsage
}

type ImageAnalysisResponse struct {
	Model   string
	Content string
	Usage   ImageUsage
}

type GeneratedImage struct {
	Bytes    []byte
	MIMEType string
}

type ImageUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
	CostMicros       int64
}

type ImageModel struct {
	ID                  string
	Name                string
	SupportedParameters map[string]ImageCapabilityDescriptor
	SupportsStreaming   bool
	EndpointsPath       string
}

type ImageModelEndpoint struct {
	ProviderName             string
	ProviderSlug             string
	ProviderTag              string
	SupportedParameters      map[string]ImageCapabilityDescriptor
	AllowedPassthroughParams []string
	SupportsStreaming        bool
	Pricing                  []ImagePricingLine
}

type ImageCapabilityDescriptor struct {
	Type   string
	Values []string
	Min    int
	Max    int
}

type ImagePricingLine struct {
	Billable string
	Unit     string
	CostUSD  float64
	Variant  string
}

type ImageGenerationError struct {
	Status     ImageProviderStatus
	StatusCode int
	Code       string
	Message    string
	Err        error
}

func (e ImageGenerationError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Status)
}

func (e ImageGenerationError) Unwrap() error {
	return e.Err
}

func NewOpenRouterImageClient(cfg OpenRouterImageConfig) *OpenRouterImageClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	retryDelay := cfg.RetryDelay
	if retryDelay == 0 {
		retryDelay = 250 * time.Millisecond
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultImageModel
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultImageMaxBytes
	}
	circuitCooldown := cfg.CircuitBreakerCooldown
	if circuitCooldown == 0 {
		circuitCooldown = 30 * time.Second
	}
	failureThreshold := cfg.CircuitBreakerFailureThreshold
	if failureThreshold == 0 {
		failureThreshold = 5
	}
	return &OpenRouterImageClient{
		apiKey:          strings.TrimSpace(cfg.APIKey),
		baseURL:         baseURL,
		model:           model,
		appURL:          strings.TrimSpace(cfg.AppURL),
		appTitle:        strings.TrimSpace(cfg.AppTitle),
		client:          &http.Client{Timeout: timeout},
		maxRetries:      cfg.MaxRetries,
		retryDelay:      retryDelay,
		maxBytes:        maxBytes,
		capabilityByKey: map[string]imageSupportedParameters{},
		circuitBreaker: circuitBreaker{
			failureThreshold: failureThreshold,
			cooldown:         circuitCooldown,
		},
	}
}

func (c *OpenRouterImageClient) Configured() bool {
	return c != nil && strings.TrimSpace(c.apiKey) != "" && strings.TrimSpace(c.model) != ""
}

func (c *OpenRouterImageClient) Generate(ctx context.Context, request ImageGenerationRequest) (ImageGenerationResponse, error) {
	if c == nil || strings.TrimSpace(c.apiKey) == "" {
		return ImageGenerationResponse{}, imageError(ImageProviderStatusUnavailable, ErrImageNotConfigured)
	}
	model := firstNonEmpty(request.Model, c.model)
	request.Model = model
	if err := c.validateImageRequest(ctx, request); err != nil {
		return ImageGenerationResponse{}, err
	}
	payload := imageCreateRequest{
		Model:           model,
		Prompt:          strings.TrimSpace(request.Prompt),
		InputReferences: imageInputReferences(request.InputReferences),
		AspectRatio:     strings.TrimSpace(request.AspectRatio),
		Resolution:      strings.TrimSpace(request.Resolution),
		Size:            strings.TrimSpace(request.Size),
		Quality:         strings.TrimSpace(request.Quality),
		OutputFormat:    strings.TrimSpace(strings.ToLower(request.OutputFormat)),
	}
	if request.Count > 1 {
		payload.N = request.Count
	}
	if request.TransparentBackground {
		payload.Background = "transparent"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ImageGenerationResponse{}, err
	}

	data, err := c.post(ctx, "/images", body, c.imageResponseReadLimit())
	if err != nil {
		return ImageGenerationResponse{}, classifyImageGenerationError(err)
	}
	var decoded imageCreateResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return ImageGenerationResponse{}, imageError(ImageProviderStatusError, fmt.Errorf("%w: %v", ErrImageInvalidResponse, err))
	}
	if len(decoded.Data) == 0 {
		return ImageGenerationResponse{}, imageError(ImageProviderStatusError, fmt.Errorf("%w: missing data", ErrImageInvalidResponse))
	}
	images := make([]GeneratedImage, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		image, err := c.decodeImage(item.B64JSON, payload.OutputFormat)
		if err != nil {
			return ImageGenerationResponse{}, err
		}
		images = append(images, image)
	}
	return ImageGenerationResponse{
		Model:  model,
		Images: images,
		Usage:  imageUsage(decoded.Usage),
	}, nil
}

func (c *OpenRouterImageClient) Analyze(ctx context.Context, request ImageAnalysisRequest) (ImageAnalysisResponse, error) {
	if c == nil || strings.TrimSpace(c.apiKey) == "" {
		return ImageAnalysisResponse{}, imageError(ImageProviderStatusUnavailable, ErrImageNotConfigured)
	}
	model := firstNonEmpty(request.Model, c.model)
	request.Model = model
	if err := c.validateImageAnalysisRequest(request); err != nil {
		return ImageAnalysisResponse{}, err
	}
	payload := imageChatRequest{
		Model: model,
		Messages: []imageChatMessage{{
			Role:    "user",
			Content: imageChatContent(strings.TrimSpace(request.Prompt), request.InputReferences),
		}},
		MaxTokens: request.MaxTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ImageAnalysisResponse{}, err
	}

	data, err := c.post(ctx, "/chat/completions", body, 2<<20)
	if err != nil {
		return ImageAnalysisResponse{}, classifyImageGenerationError(err)
	}
	var decoded imageChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return ImageAnalysisResponse{}, imageError(ImageProviderStatusError, fmt.Errorf("%w: %v", ErrImageInvalidResponse, err))
	}
	if len(decoded.Choices) == 0 {
		return ImageAnalysisResponse{}, imageError(ImageProviderStatusError, fmt.Errorf("%w: missing choices", ErrImageInvalidResponse))
	}
	content, err := imageChatContentText(decoded.Choices[0].Message.Content)
	if err != nil {
		return ImageAnalysisResponse{}, imageError(ImageProviderStatusError, err)
	}
	return ImageAnalysisResponse{
		Model:   firstNonEmpty(decoded.Model, model),
		Content: content,
		Usage:   imageUsage(decoded.Usage),
	}, nil
}

func (c *OpenRouterImageClient) ListImageModels(ctx context.Context) ([]ImageModel, error) {
	data, err := c.get(ctx, "/images/models")
	if err != nil {
		return nil, classifyImageGenerationError(err)
	}
	var decoded imageModelsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	models := make([]ImageModel, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		models = append(models, ImageModel{
			ID:                  item.ID,
			Name:                item.Name,
			SupportedParameters: convertImageParameters(item.SupportedParameters),
			SupportsStreaming:   item.SupportsStreaming,
			EndpointsPath:       item.Endpoints,
		})
	}
	return models, nil
}

func (c *OpenRouterImageClient) ListImageModelEndpoints(ctx context.Context, model string) ([]ImageModelEndpoint, error) {
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" {
		model = c.model
	}
	if model == "" {
		return nil, imageError(ImageProviderStatusUnavailable, ErrImageNotConfigured)
	}
	data, err := c.get(ctx, "/images/models/"+model+"/endpoints")
	if err != nil {
		return nil, classifyImageGenerationError(err)
	}
	var decoded imageModelEndpointsResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	endpoints := make([]ImageModelEndpoint, 0, len(decoded.Endpoints))
	for _, endpoint := range decoded.Endpoints {
		endpoints = append(endpoints, ImageModelEndpoint{
			ProviderName:             endpoint.ProviderName,
			ProviderSlug:             endpoint.ProviderSlug,
			ProviderTag:              endpoint.ProviderTag,
			SupportedParameters:      convertImageParameters(endpoint.SupportedParameters),
			AllowedPassthroughParams: append([]string(nil), endpoint.AllowedPassthroughParams...),
			SupportsStreaming:        endpoint.SupportsStreaming,
			Pricing:                  convertImagePricing(endpoint.Pricing),
		})
	}
	return endpoints, nil
}

func (c *OpenRouterImageClient) validateImageRequest(ctx context.Context, request ImageGenerationRequest) error {
	if strings.TrimSpace(request.Model) == "" {
		return imageError(ImageProviderStatusUnavailable, ErrImageNotConfigured)
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: prompt is required", ErrImageUnsupportedArgument))
	}
	if request.Count < 0 {
		return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: count must be positive", ErrImageUnsupportedArgument))
	}
	if request.Count > 1 {
		return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: count is capped at 1", ErrImageUnsupportedArgument))
	}
	for _, reference := range request.InputReferences {
		if !validImageReferenceURL(reference.URL) {
			return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: input reference URL must be HTTPS, HTTP, or an image data URL", ErrImageUnsupportedArgument))
		}
	}
	format := strings.ToLower(strings.TrimSpace(request.OutputFormat))
	if format != "" && !isAllowedImageFormat(format) {
		return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: output_format must be png, jpeg, or webp", ErrImageUnsupportedArgument))
	}
	if request.TransparentBackground && format == "jpeg" {
		return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: transparent background requires png or webp output", ErrImageUnsupportedArgument))
	}
	parameters := requestedImageParameters(request)
	if len(parameters) == 0 {
		return nil
	}
	capabilities, err := c.supportedParameters(ctx, request.Model)
	if err != nil {
		return err
	}
	for name, value := range parameters {
		descriptor, ok := capabilities[name]
		if !ok {
			return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: %s is not supported by %s", ErrImageUnsupportedArgument, name, request.Model))
		}
		if !descriptor.accepts(value) {
			return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: %s=%s is not supported by %s", ErrImageUnsupportedArgument, name, value, request.Model))
		}
	}
	return nil
}

func (c *OpenRouterImageClient) validateImageAnalysisRequest(request ImageAnalysisRequest) error {
	if strings.TrimSpace(request.Model) == "" {
		return imageError(ImageProviderStatusUnavailable, ErrImageNotConfigured)
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: prompt is required", ErrImageUnsupportedArgument))
	}
	if len(request.InputReferences) == 0 {
		return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: at least one image reference is required", ErrImageUnsupportedArgument))
	}
	for _, reference := range request.InputReferences {
		if !validImageReferenceURL(reference.URL) {
			return imageError(ImageProviderStatusInvalid, fmt.Errorf("%w: input reference URL must be HTTPS, HTTP, or an image data URL", ErrImageUnsupportedArgument))
		}
	}
	return nil
}

func requestedImageParameters(request ImageGenerationRequest) map[string]string {
	result := map[string]string{}
	if value := strings.TrimSpace(request.AspectRatio); value != "" {
		result["aspect_ratio"] = value
	}
	if value := strings.TrimSpace(request.Resolution); value != "" {
		result["resolution"] = value
	}
	if value := strings.TrimSpace(request.Size); value != "" {
		result["size"] = value
	}
	if value := strings.TrimSpace(request.Quality); value != "" {
		result["quality"] = value
	}
	if value := strings.ToLower(strings.TrimSpace(request.OutputFormat)); value != "" {
		result["output_format"] = value
	}
	if request.TransparentBackground {
		result["background"] = "transparent"
	}
	return result
}

func imageInputReferences(references []ImageInputReference) []imageCreateInputReference {
	if len(references) == 0 {
		return nil
	}
	payloads := make([]imageCreateInputReference, 0, len(references))
	for _, reference := range references {
		rawURL := strings.TrimSpace(reference.URL)
		if rawURL == "" {
			continue
		}
		payloads = append(payloads, imageCreateInputReference{
			Type:     "image_url",
			ImageURL: imageCreateImageURL{URL: rawURL},
		})
	}
	return payloads
}

func imageChatContent(prompt string, references []ImageInputReference) []imageChatContentPart {
	parts := []imageChatContentPart{{Type: "text", Text: strings.TrimSpace(prompt)}}
	for _, reference := range references {
		rawURL := strings.TrimSpace(reference.URL)
		if rawURL == "" {
			continue
		}
		parts = append(parts, imageChatContentPart{
			Type:     "image_url",
			ImageURL: &imageCreateImageURL{URL: rawURL},
		})
	}
	return parts
}

func imageChatContentText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", fmt.Errorf("%w: missing message content", ErrImageInvalidResponse)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return "", fmt.Errorf("%w: empty message content", ErrImageInvalidResponse)
		}
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("%w: invalid message content", ErrImageInvalidResponse)
	}
	var builder strings.Builder
	for _, part := range parts {
		if strings.EqualFold(strings.TrimSpace(part.Type), "text") || part.Type == "" {
			if value := strings.TrimSpace(part.Text); value != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(value)
			}
		}
	}
	text = strings.TrimSpace(builder.String())
	if text == "" {
		return "", fmt.Errorf("%w: empty message content", ErrImageInvalidResponse)
	}
	return text, nil
}

func validImageReferenceURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(rawURL), "data:image/") {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") || strings.EqualFold(parsed.Scheme, "http")
}

func (c *OpenRouterImageClient) supportedParameters(ctx context.Context, model string) (imageSupportedParameters, error) {
	c.capabilityMu.Lock()
	if cached, ok := c.capabilityByKey[model]; ok {
		c.capabilityMu.Unlock()
		return cached, nil
	}
	c.capabilityMu.Unlock()

	endpoints, err := c.ListImageModelEndpoints(ctx, model)
	if err != nil {
		return nil, err
	}
	merged := imageSupportedParameters{}
	for _, endpoint := range endpoints {
		for name, descriptor := range endpoint.SupportedParameters {
			merged[name] = mergeImageDescriptor(merged[name], descriptor)
		}
	}
	if len(merged) == 0 {
		return nil, imageError(ImageProviderStatusUnavailable, fmt.Errorf("no image capabilities found for %s", model))
	}
	c.capabilityMu.Lock()
	c.capabilityByKey[model] = merged
	c.capabilityMu.Unlock()
	return merged, nil
}

func (c *OpenRouterImageClient) decodeImage(rawBase64, outputFormat string) (GeneratedImage, error) {
	rawBase64 = strings.TrimSpace(rawBase64)
	if rawBase64 == "" {
		return GeneratedImage{}, imageError(ImageProviderStatusError, fmt.Errorf("%w: missing b64_json", ErrImageInvalidResponse))
	}
	if strings.Contains(rawBase64, ",") && strings.HasPrefix(strings.ToLower(rawBase64), "data:") {
		_, rawBase64, _ = strings.Cut(rawBase64, ",")
	}
	if int64(base64.StdEncoding.DecodedLen(len(rawBase64))) > c.maxBytes {
		return GeneratedImage{}, imageError(ImageProviderStatusError, ErrImageTooLarge)
	}
	data, err := base64.StdEncoding.DecodeString(rawBase64)
	if err != nil {
		return GeneratedImage{}, imageError(ImageProviderStatusError, fmt.Errorf("%w: invalid base64", ErrImageInvalidResponse))
	}
	if int64(len(data)) > c.maxBytes {
		return GeneratedImage{}, imageError(ImageProviderStatusError, ErrImageTooLarge)
	}
	mimeType := mimeTypeForImage(data, outputFormat)
	if !isAllowedImageMIME(mimeType) {
		return GeneratedImage{}, imageError(ImageProviderStatusError, fmt.Errorf("%w: unsupported image MIME type %s", ErrImageInvalidResponse, mimeType))
	}
	return GeneratedImage{Bytes: data, MIMEType: mimeType}, nil
}

func (c *OpenRouterImageClient) imageResponseReadLimit() int64 {
	limit := c.maxBytes*2 + (1 << 20)
	if limit < 2<<20 {
		return 2 << 20
	}
	return limit
}

func (c *OpenRouterImageClient) post(ctx context.Context, path string, body []byte, readLimit int64) ([]byte, error) {
	if err := c.circuitBreaker.allow(); err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || attempt == c.maxRetries {
				c.circuitBreaker.recordFailure()
				return nil, err
			}
			if waitErr := c.waitBeforeRetry(ctx); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		data, readErr := readLimited(resp.Body, readLimit)
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.circuitBreaker.recordSuccess()
			return data, nil
		}
		lastErr = parseOpenRouterError(resp.StatusCode, data)
		if !retryableStatus(resp.StatusCode) || attempt == c.maxRetries {
			if retryableStatus(resp.StatusCode) {
				c.circuitBreaker.recordFailure()
			}
			return nil, lastErr
		}
		if waitErr := c.waitBeforeRetry(ctx); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, lastErr
}

func (c *OpenRouterImageClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body, 2<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseOpenRouterError(resp.StatusCode, data)
	}
	return data, nil
}

func (c *OpenRouterImageClient) setHeaders(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.appURL != "" {
		req.Header.Set("HTTP-Referer", c.appURL)
	}
	if c.appTitle != "" {
		req.Header.Set("X-OpenRouter-Title", c.appTitle)
	}
}

func (c *OpenRouterImageClient) waitBeforeRetry(ctx context.Context) error {
	if c.retryDelay <= 0 {
		return nil
	}
	timer := time.NewTimer(c.retryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 2 << 20
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeded %d bytes", limit)
	}
	return data, nil
}

type imageCreateRequest struct {
	Model           string                      `json:"model"`
	Prompt          string                      `json:"prompt"`
	InputReferences []imageCreateInputReference `json:"input_references,omitempty"`
	N               int                         `json:"n,omitempty"`
	AspectRatio     string                      `json:"aspect_ratio,omitempty"`
	Resolution      string                      `json:"resolution,omitempty"`
	Size            string                      `json:"size,omitempty"`
	Quality         string                      `json:"quality,omitempty"`
	OutputFormat    string                      `json:"output_format,omitempty"`
	Background      string                      `json:"background,omitempty"`
}

type imageCreateInputReference struct {
	Type     string              `json:"type"`
	ImageURL imageCreateImageURL `json:"image_url"`
}

type imageCreateImageURL struct {
	URL string `json:"url"`
}

type imageCreateResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
	Usage imageUsagePayload `json:"usage"`
}

type imageChatRequest struct {
	Model     string             `json:"model"`
	Messages  []imageChatMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens,omitempty"`
}

type imageChatMessage struct {
	Role    string                 `json:"role"`
	Content []imageChatContentPart `json:"content"`
}

type imageChatContentPart struct {
	Type     string               `json:"type"`
	Text     string               `json:"text,omitempty"`
	ImageURL *imageCreateImageURL `json:"image_url,omitempty"`
}

type imageChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage imageUsagePayload `json:"usage"`
}

type imageUsagePayload struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}

func imageUsage(payload imageUsagePayload) ImageUsage {
	return ImageUsage{
		PromptTokens:     payload.PromptTokens,
		CompletionTokens: payload.CompletionTokens,
		TotalTokens:      payload.TotalTokens,
		CostUSD:          payload.Cost,
		CostMicros:       int64(math.Round(payload.Cost * 1_000_000)),
	}
}

type imageModelsResponse struct {
	Data []struct {
		ID                  string                               `json:"id"`
		Name                string                               `json:"name"`
		SupportedParameters map[string]imageCapabilityDescriptor `json:"supported_parameters"`
		SupportsStreaming   bool                                 `json:"supports_streaming"`
		Endpoints           string                               `json:"endpoints"`
	} `json:"data"`
}

type imageModelEndpointsResponse struct {
	ID        string `json:"id"`
	Endpoints []struct {
		ProviderName             string                               `json:"provider_name"`
		ProviderSlug             string                               `json:"provider_slug"`
		ProviderTag              string                               `json:"provider_tag"`
		SupportedParameters      map[string]imageCapabilityDescriptor `json:"supported_parameters"`
		AllowedPassthroughParams []string                             `json:"allowed_passthrough_parameters"`
		SupportsStreaming        bool                                 `json:"supports_streaming"`
		Pricing                  []imagePricingLine                   `json:"pricing"`
	} `json:"endpoints"`
}

type imageCapabilityDescriptor struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Min    int      `json:"min"`
	Max    int      `json:"max"`
}

type imagePricingLine struct {
	Billable string  `json:"billable"`
	Unit     string  `json:"unit"`
	CostUSD  float64 `json:"cost_usd"`
	Variant  string  `json:"variant"`
}

type imageSupportedParameters map[string]ImageCapabilityDescriptor

func (d ImageCapabilityDescriptor) accepts(value string) bool {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "enum":
		for _, allowed := range d.Values {
			if strings.EqualFold(strings.TrimSpace(allowed), value) {
				return true
			}
		}
		return false
	case "range":
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
			return false
		}
		if d.Min != 0 && parsed < d.Min {
			return false
		}
		if d.Max != 0 && parsed > d.Max {
			return false
		}
		return true
	case "boolean":
		return value == "true" || value == "false" || value == "transparent"
	default:
		return true
	}
}

func convertImageParameters(raw map[string]imageCapabilityDescriptor) map[string]ImageCapabilityDescriptor {
	if len(raw) == 0 {
		return nil
	}
	converted := make(map[string]ImageCapabilityDescriptor, len(raw))
	for name, descriptor := range raw {
		converted[name] = ImageCapabilityDescriptor{
			Type:   descriptor.Type,
			Values: append([]string(nil), descriptor.Values...),
			Min:    descriptor.Min,
			Max:    descriptor.Max,
		}
	}
	return converted
}

func convertImagePricing(raw []imagePricingLine) []ImagePricingLine {
	if len(raw) == 0 {
		return nil
	}
	converted := make([]ImagePricingLine, 0, len(raw))
	for _, line := range raw {
		converted = append(converted, ImagePricingLine{
			Billable: line.Billable,
			Unit:     line.Unit,
			CostUSD:  line.CostUSD,
			Variant:  line.Variant,
		})
	}
	return converted
}

func mergeImageDescriptor(left, right ImageCapabilityDescriptor) ImageCapabilityDescriptor {
	if left.Type == "" {
		return right
	}
	if strings.EqualFold(left.Type, "enum") && strings.EqualFold(right.Type, "enum") {
		values := normalizeStringList(append(append([]string(nil), left.Values...), right.Values...))
		left.Values = values
		return left
	}
	if strings.EqualFold(left.Type, "range") && strings.EqualFold(right.Type, "range") {
		if left.Min == 0 || right.Min < left.Min {
			left.Min = right.Min
		}
		if right.Max > left.Max {
			left.Max = right.Max
		}
		return left
	}
	return left
}

func isAllowedImageFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png", "jpeg", "webp":
		return true
	default:
		return false
	}
}

func isAllowedImageMIME(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func mimeTypeForImage(data []byte, outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return http.DetectContentType(data)
}

func imageError(status ImageProviderStatus, err error) ImageGenerationError {
	return ImageGenerationError{Status: status, Message: safeImageErrorMessage(status), Err: err}
}

func classifyImageGenerationError(err error) error {
	if err == nil {
		return nil
	}
	var imageErr ImageGenerationError
	if errors.As(err, &imageErr) {
		return imageErr
	}
	var openRouterErr Error
	if errors.As(err, &openRouterErr) {
		status := ImageProviderStatusError
		switch {
		case imagePolicyBlocked(openRouterErr):
			status = ImageProviderStatusPolicyBlocked
		case openRouterErr.StatusCode == http.StatusBadRequest:
			status = ImageProviderStatusInvalid
		case openRouterErr.StatusCode == http.StatusTooManyRequests:
			status = ImageProviderStatusRateLimited
		case openRouterErr.StatusCode == http.StatusUnauthorized ||
			openRouterErr.StatusCode == http.StatusForbidden ||
			openRouterErr.StatusCode == http.StatusPaymentRequired ||
			openRouterErr.StatusCode == http.StatusNotFound:
			status = ImageProviderStatusUnavailable
		case openRouterErr.StatusCode >= 500:
			status = ImageProviderStatusUnavailable
		}
		return ImageGenerationError{
			Status:     status,
			StatusCode: openRouterErr.StatusCode,
			Code:       openRouterErr.Code,
			Message:    safeImageErrorMessage(status),
			Err:        err,
		}
	}
	if errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrImageNotConfigured) || errors.Is(err, ErrCircuitOpen) {
		return imageError(ImageProviderStatusUnavailable, err)
	}
	if errors.Is(err, ErrImageUnsupportedArgument) {
		return imageError(ImageProviderStatusInvalid, err)
	}
	if errors.Is(err, ErrImageTooLarge) || errors.Is(err, ErrImageInvalidResponse) {
		return imageError(ImageProviderStatusError, err)
	}
	return imageError(ImageProviderStatusError, err)
}

func imagePolicyBlocked(err Error) bool {
	text := strings.ToLower(err.Code + " " + err.Message)
	for _, marker := range []string{"policy", "safety", "moderation", "blocked", "prohibited"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func safeImageErrorMessage(status ImageProviderStatus) string {
	switch status {
	case ImageProviderStatusPolicyBlocked:
		return "I could not generate that image because the request was blocked by the image provider's safety policy. Try a safer revision."
	case ImageProviderStatusInvalid:
		return "I could not generate that image because one of the requested image settings is unsupported. Try a different aspect ratio or resolution."
	case ImageProviderStatusRateLimited:
		return "Image generation is rate limited right now. Please try again shortly."
	case ImageProviderStatusUnavailable:
		return "Image generation is not available right now. Please try again later."
	default:
		return "I could not generate that image. Please try again later."
	}
}
