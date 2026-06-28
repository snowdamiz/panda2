package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	defaultR2Timeout = 2 * time.Minute
	r2Region         = "auto"
	r2Service        = "s3"
)

var ErrNotConfigured = errors.New("r2 object storage is not configured")

type UploadRequest struct {
	Key         string
	ContentType string
	Body        []byte
}

type UploadResult struct {
	Key       string
	URL       string
	SizeBytes int64
	ETag      string
}

type R2Config struct {
	AccountID       string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	PublicBaseURL   string
	Prefix          string
	HTTPClient      *http.Client
	Timeout         time.Duration
	Now             func() time.Time
}

type R2Client struct {
	endpoint        string
	accessKeyID     string
	secretAccessKey string
	bucket          string
	publicBaseURL   string
	prefix          string
	client          *http.Client
	now             func() time.Time
}

func NewR2Client(config R2Config) *R2Client {
	endpoint := strings.TrimRight(strings.TrimSpace(config.Endpoint), "/")
	if endpoint == "" {
		accountID := strings.TrimSpace(config.AccountID)
		if accountID != "" {
			endpoint = "https://" + accountID + ".r2.cloudflarestorage.com"
		}
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultR2Timeout
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &R2Client{
		endpoint:        endpoint,
		accessKeyID:     strings.TrimSpace(config.AccessKeyID),
		secretAccessKey: strings.TrimSpace(config.SecretAccessKey),
		bucket:          strings.Trim(strings.TrimSpace(config.Bucket), "/"),
		publicBaseURL:   strings.TrimRight(strings.TrimSpace(config.PublicBaseURL), "/"),
		prefix:          strings.Trim(strings.TrimSpace(config.Prefix), "/"),
		client:          client,
		now:             now,
	}
}

func (c *R2Client) Configured() bool {
	return c != nil &&
		c.endpoint != "" &&
		c.accessKeyID != "" &&
		c.secretAccessKey != "" &&
		c.bucket != "" &&
		c.publicBaseURL != "" &&
		c.client != nil
}

func (c *R2Client) Upload(ctx context.Context, request UploadRequest) (UploadResult, error) {
	if !c.Configured() {
		return UploadResult{}, ErrNotConfigured
	}
	key := prefixedObjectKey(c.prefix, request.Key)
	if key == "" {
		return UploadResult{}, fmt.Errorf("object key is required")
	}
	if len(request.Body) == 0 {
		return UploadResult{}, fmt.Errorf("object body is empty")
	}
	endpoint, err := url.Parse(c.endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return UploadResult{}, fmt.Errorf("invalid r2 endpoint")
	}
	objectURL := *endpoint
	objectURL.Path = joinURLPath(endpoint.Path, c.bucket, key)
	objectURL.RawQuery = ""
	objectURL.Fragment = ""

	body := append([]byte(nil), request.Body...)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL.String(), bytes.NewReader(body))
	if err != nil {
		return UploadResult{}, err
	}
	req.ContentLength = int64(len(body))
	contentType := strings.TrimSpace(request.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	payloadHash := sha256Hex(body)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	now := c.now().UTC()
	req.Header.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	req.Header.Set("Authorization", c.authorizationHeader(req, payloadHash, now))

	resp, err := c.client.Do(req)
	if err != nil {
		return UploadResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return UploadResult{}, fmt.Errorf("r2 upload failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return UploadResult{
		Key:       key,
		URL:       c.publicURL(key),
		SizeBytes: int64(len(body)),
		ETag:      strings.Trim(resp.Header.Get("ETag"), `"`),
	}, nil
}

func (c *R2Client) authorizationHeader(req *http.Request, payloadHash string, now time.Time) string {
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	credentialScope := strings.Join([]string{date, r2Region, r2Service, "aws4_request"}, "/")
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		"",
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.secretAccessKey, date), stringToSign))
	return "AWS4-HMAC-SHA256 Credential=" + c.accessKeyID + "/" + credentialScope +
		", SignedHeaders=" + signedHeaders +
		", Signature=" + signature
}

func (c *R2Client) publicURL(key string) string {
	base := strings.TrimRight(c.publicBaseURL, "/")
	if base == "" || key == "" {
		return ""
	}
	return base + "/" + escapeObjectKey(key)
}

func prefixedObjectKey(prefix, key string) string {
	key = strings.Trim(strings.TrimSpace(key), "/")
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	switch {
	case prefix == "":
		return key
	case key == "":
		return prefix
	default:
		return prefix + "/" + key
	}
}

func joinURLPath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, "/")
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return "/"
	}
	return "/" + path.Join(cleaned...)
}

func escapeObjectKey(key string) string {
	segments := strings.Split(strings.Trim(key, "/"), "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func signingKey(secretAccessKey, date string) []byte {
	key := hmacSHA256([]byte("AWS4"+secretAccessKey), date)
	key = hmacSHA256(key, r2Region)
	key = hmacSHA256(key, r2Service)
	return hmacSHA256(key, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}
