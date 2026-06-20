package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBraveBaseURL = "https://api.search.brave.com/res/v1"
	defaultTimeout      = 8 * time.Second
	maxQueryChars       = 400
	maxQueryWords       = 50
	maxWebResults       = 20
	maxOffset           = 9
)

var ErrNotConfigured = errors.New("brave search api key is not configured")

type Searcher interface {
	Search(ctx context.Context, request Request) (Response, error)
}

type Config struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

type Client struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type Request struct {
	Query         string
	Count         int
	Offset        int
	Country       string
	SearchLang    string
	UILang        string
	SafeSearch    string
	Freshness     string
	ExtraSnippets bool
}

type Response struct {
	Provider             string
	Query                string
	AlteredQuery         string
	MoreResultsAvailable bool
	Results              []Result
}

type Result struct {
	Title         string
	URL           string
	Description   string
	Age           string
	PageAge       string
	Language      string
	Source        string
	ExtraSnippets []string
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func NewBraveClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBraveBaseURL
	}
	return &Client{
		apiKey:  strings.TrimSpace(cfg.APIKey),
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *Client) Search(ctx context.Context, request Request) (Response, error) {
	if c == nil || c.apiKey == "" {
		return Response{}, ErrNotConfigured
	}
	request, err := normalizeRequest(request)
	if err != nil {
		return Response{}, err
	}

	endpoint, err := url.Parse(c.baseURL + "/web/search")
	if err != nil {
		return Response{}, err
	}
	query := endpoint.Query()
	query.Set("q", request.Query)
	query.Set("count", fmt.Sprint(request.Count))
	query.Set("offset", fmt.Sprint(request.Offset))
	query.Set("country", request.Country)
	query.Set("search_lang", request.SearchLang)
	query.Set("ui_lang", request.UILang)
	query.Set("safesearch", request.SafeSearch)
	query.Set("result_filter", "web")
	if request.Freshness != "" {
		query.Set("freshness", request.Freshness)
	}
	if request.ExtraSnippets {
		query.Set("extra_snippets", "true")
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, parseError(resp.StatusCode, data)
	}

	var decoded braveResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Response{}, err
	}
	results := make([]Result, 0, len(decoded.Web.Results))
	for _, result := range decoded.Web.Results {
		results = append(results, Result{
			Title:         strings.TrimSpace(result.Title),
			URL:           strings.TrimSpace(result.URL),
			Description:   strings.TrimSpace(result.Description),
			Age:           strings.TrimSpace(result.Age),
			PageAge:       strings.TrimSpace(result.PageAge),
			Language:      strings.TrimSpace(result.Language),
			Source:        firstNonEmpty(result.Profile.LongName, result.Profile.Name),
			ExtraSnippets: cleanStringList(result.ExtraSnippets),
		})
	}
	return Response{
		Provider:             "brave_search",
		Query:                firstNonEmpty(decoded.Query.Original, request.Query),
		AlteredQuery:         strings.TrimSpace(decoded.Query.Altered),
		MoreResultsAvailable: decoded.Query.MoreResultsAvailable,
		Results:              results,
	}, nil
}

func normalizeRequest(request Request) (Request, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		return Request{}, fmt.Errorf("query is required")
	}
	if len(request.Query) > maxQueryChars || len(strings.Fields(request.Query)) > maxQueryWords {
		return Request{}, fmt.Errorf("query must be at most %d characters and %d words", maxQueryChars, maxQueryWords)
	}
	if request.Count <= 0 {
		request.Count = 5
	}
	if request.Count > maxWebResults {
		request.Count = maxWebResults
	}
	if request.Offset < 0 {
		request.Offset = 0
	}
	if request.Offset > maxOffset {
		request.Offset = maxOffset
	}
	request.Country = strings.ToUpper(strings.TrimSpace(request.Country))
	if request.Country == "" {
		request.Country = "US"
	}
	request.SearchLang = strings.ToLower(strings.TrimSpace(request.SearchLang))
	if request.SearchLang == "" {
		request.SearchLang = "en"
	}
	request.UILang = strings.TrimSpace(request.UILang)
	if request.UILang == "" {
		request.UILang = "en-US"
	}
	request.SafeSearch = strings.ToLower(strings.TrimSpace(request.SafeSearch))
	switch request.SafeSearch {
	case "", "moderate":
		request.SafeSearch = "moderate"
	case "off", "strict":
	default:
		return Request{}, fmt.Errorf("safesearch must be off, moderate, or strict")
	}
	request.Freshness = strings.TrimSpace(request.Freshness)
	return request, nil
}

func parseError(statusCode int, data []byte) error {
	var decoded struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err == nil {
		message := firstNonEmpty(decoded.Error.Message, decoded.Error.Detail)
		if message != "" {
			return Error{StatusCode: statusCode, Code: strings.TrimSpace(fmt.Sprint(decoded.Error.Code)), Message: message}
		}
	}
	message := strings.TrimSpace(string(data))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return Error{StatusCode: statusCode, Message: message}
}

func (e Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("brave search error %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("brave search error %d: %s", e.StatusCode, e.Message)
}

type braveResponse struct {
	Query struct {
		Original             string `json:"original"`
		Altered              string `json:"altered"`
		MoreResultsAvailable bool   `json:"more_results_available"`
	} `json:"query"`
	Web struct {
		Results []braveResult `json:"results"`
	} `json:"web"`
}

type braveResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Description   string   `json:"description"`
	Age           string   `json:"age"`
	PageAge       string   `json:"page_age"`
	Language      string   `json:"language"`
	ExtraSnippets []string `json:"extra_snippets"`
	Profile       struct {
		Name     string `json:"name"`
		LongName string `json:"long_name"`
	} `json:"profile"`
}

func cleanStringList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func firstNonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}
