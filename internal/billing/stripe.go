package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultStripeAPIBaseURL = "https://api.stripe.com"

type StripeClient interface {
	CreateCheckoutSession(ctx context.Context, request StripeCheckoutRequest) (StripeCheckoutSession, error)
	CreatePortalSession(ctx context.Context, request StripePortalRequest) (StripePortalSession, error)
}

type StripeCheckoutRequest struct {
	Mode                 string
	PriceID              string
	CustomerID           string
	CustomerEmail        string
	ClientReferenceID    string
	SuccessURL           string
	CancelURL            string
	Metadata             map[string]string
	SubscriptionMetadata map[string]string
}

type StripeCheckoutSession struct {
	ID         string
	URL        string
	CustomerID string
}

type StripePortalRequest struct {
	CustomerID string
	ReturnURL  string
}

type StripePortalSession struct {
	ID  string
	URL string
}

type HTTPStripeClient struct {
	secretKey string
	baseURL   string
	client    *http.Client
}

func NewHTTPStripeClient(secretKey, baseURL string) *HTTPStripeClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultStripeAPIBaseURL
	}
	return &HTTPStripeClient{
		secretKey: strings.TrimSpace(secretKey),
		baseURL:   baseURL,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *HTTPStripeClient) CreateCheckoutSession(ctx context.Context, request StripeCheckoutRequest) (StripeCheckoutSession, error) {
	values := url.Values{}
	values.Set("mode", strings.TrimSpace(request.Mode))
	values.Set("line_items[0][price]", strings.TrimSpace(request.PriceID))
	values.Set("line_items[0][quantity]", "1")
	values.Set("success_url", strings.TrimSpace(request.SuccessURL))
	values.Set("cancel_url", strings.TrimSpace(request.CancelURL))
	values.Set("client_reference_id", strings.TrimSpace(request.ClientReferenceID))
	if customerID := strings.TrimSpace(request.CustomerID); customerID != "" {
		values.Set("customer", customerID)
	} else if email := strings.TrimSpace(request.CustomerEmail); email != "" {
		values.Set("customer_email", email)
	}
	if request.Mode == "payment" && strings.TrimSpace(request.CustomerID) == "" {
		values.Set("customer_creation", "always")
	}
	for key, value := range request.Metadata {
		addStripeMetadata(values, "metadata", key, value)
	}
	for key, value := range request.SubscriptionMetadata {
		addStripeMetadata(values, "subscription_data[metadata]", key, value)
	}

	var response struct {
		ID       string `json:"id"`
		URL      string `json:"url"`
		Customer string `json:"customer"`
	}
	if err := c.postForm(ctx, "/v1/checkout/sessions", values, &response); err != nil {
		return StripeCheckoutSession{}, err
	}
	if strings.TrimSpace(response.ID) == "" || strings.TrimSpace(response.URL) == "" {
		return StripeCheckoutSession{}, fmt.Errorf("stripe checkout session response was missing id or url")
	}
	return StripeCheckoutSession{ID: response.ID, URL: response.URL, CustomerID: response.Customer}, nil
}

func (c *HTTPStripeClient) CreatePortalSession(ctx context.Context, request StripePortalRequest) (StripePortalSession, error) {
	values := url.Values{}
	values.Set("customer", strings.TrimSpace(request.CustomerID))
	values.Set("return_url", strings.TrimSpace(request.ReturnURL))

	var response struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := c.postForm(ctx, "/v1/billing_portal/sessions", values, &response); err != nil {
		return StripePortalSession{}, err
	}
	if strings.TrimSpace(response.ID) == "" || strings.TrimSpace(response.URL) == "" {
		return StripePortalSession{}, fmt.Errorf("stripe portal session response was missing id or url")
	}
	return StripePortalSession{ID: response.ID, URL: response.URL}, nil
}

func (c *HTTPStripeClient) postForm(ctx context.Context, path string, values url.Values, target any) error {
	if c == nil || strings.TrimSpace(c.secretKey) == "" {
		return ErrStripeNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewBufferString(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.secretKey, "")
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return stripeAPIError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode stripe response: %w", err)
	}
	return nil
}

func addStripeMetadata(values url.Values, prefix, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	values.Set(prefix+"["+key+"]", value)
}

func stripeAPIError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error.Message) != "" {
		if payload.Error.Type != "" {
			return fmt.Errorf("stripe api error (%d %s): %s", status, payload.Error.Type, payload.Error.Message)
		}
		return fmt.Errorf("stripe api error (%d): %s", status, payload.Error.Message)
	}
	return fmt.Errorf("stripe api error (%d): %s", status, strings.TrimSpace(string(body)))
}
