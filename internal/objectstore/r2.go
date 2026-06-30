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
	"sort"
	"strconv"
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

// SigningConfigured reports whether the client can sign requests (presign GET,
// delete) against the private S3 endpoint. Unlike Configured it does not require
// a public base URL, so it works even when the bucket has no public access.
func (c *R2Client) SigningConfigured() bool {
	return c != nil &&
		c.endpoint != "" &&
		c.accessKeyID != "" &&
		c.secretAccessKey != "" &&
		c.bucket != "" &&
		c.client != nil
}

const r2MaxPresignTTL = 7 * 24 * time.Hour

// PresignGetURL returns a SigV4 query-string presigned GET URL for the given
// fully-prefixed object key (the value stored as RenderedClip.ObjectKey). The
// URL targets the private S3 endpoint, not the public CDN base URL.
func (c *R2Client) PresignGetURL(key string, ttl time.Duration) (string, error) {
	return c.presignGet(key, ttl, "")
}

// PresignDownloadURL is like PresignGetURL but adds a Content-Disposition
// override so the browser downloads the object under the given filename instead
// of playing it inline.
func (c *R2Client) PresignDownloadURL(key string, ttl time.Duration, filename string) (string, error) {
	disposition := ""
	if name := strings.TrimSpace(filename); name != "" {
		disposition = `attachment; filename="` + name + `"`
	} else {
		disposition = "attachment"
	}
	return c.presignGet(key, ttl, disposition)
}

func (c *R2Client) presignGet(key string, ttl time.Duration, contentDisposition string) (string, error) {
	if !c.SigningConfigured() {
		return "", ErrNotConfigured
	}
	key = strings.Trim(strings.TrimSpace(key), "/")
	if key == "" {
		return "", fmt.Errorf("object key is required")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	if ttl > r2MaxPresignTTL {
		ttl = r2MaxPresignTTL
	}
	endpoint, err := url.Parse(c.endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return "", fmt.Errorf("invalid r2 endpoint")
	}
	objectURL := *endpoint
	objectURL.Path = joinURLPath(endpoint.Path, c.bucket, key)
	objectURL.RawQuery = ""
	objectURL.Fragment = ""

	now := c.now().UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	credentialScope := strings.Join([]string{date, r2Region, r2Service, "aws4_request"}, "/")
	credential := c.accessKeyID + "/" + credentialScope

	// Canonical query parameters must be URI-encoded and sorted by key. All the
	// X-Amz-* keys precede a lowercase "response-content-disposition" override.
	params := []string{
		"X-Amz-Algorithm=AWS4-HMAC-SHA256",
		"X-Amz-Credential=" + awsURIEncode(credential, true),
		"X-Amz-Date=" + amzDate,
		"X-Amz-Expires=" + strconv.Itoa(int(ttl/time.Second)),
		"X-Amz-SignedHeaders=host",
	}
	if contentDisposition != "" {
		params = append(params, "response-content-disposition="+awsURIEncode(contentDisposition, true))
	}
	sort.Strings(params)
	canonicalQuery := strings.Join(params, "&")

	escapedPath := objectURL.EscapedPath()
	canonicalHeaders := "host:" + objectURL.Host + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		escapedPath,
		canonicalQuery,
		canonicalHeaders,
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.secretAccessKey, date), stringToSign))

	return objectURL.Scheme + "://" + objectURL.Host + escapedPath + "?" + canonicalQuery + "&X-Amz-Signature=" + signature, nil
}

// Delete removes the object at the given fully-prefixed key. A missing object is
// treated as success.
func (c *R2Client) Delete(ctx context.Context, key string) error {
	if !c.SigningConfigured() {
		return ErrNotConfigured
	}
	key = strings.Trim(strings.TrimSpace(key), "/")
	if key == "" {
		return fmt.Errorf("object key is required")
	}
	endpoint, err := url.Parse(c.endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return fmt.Errorf("invalid r2 endpoint")
	}
	objectURL := *endpoint
	objectURL.Path = joinURLPath(endpoint.Path, c.bucket, key)
	objectURL.RawQuery = ""
	objectURL.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, objectURL.String(), nil)
	if err != nil {
		return err
	}
	payloadHash := sha256Hex([]byte(""))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	now := c.now().UTC()
	req.Header.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	req.Header.Set("Authorization", c.authorizationHeader(req, payloadHash, now))

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("r2 delete failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
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

// awsURIEncode percent-encodes a value per the AWS SigV4 canonical rules
// (RFC 3986): unreserved characters pass through, everything else is encoded.
// When encodeSlash is false, '/' is left intact (used for path components).
func awsURIEncode(value string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.' || ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !encodeSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
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
