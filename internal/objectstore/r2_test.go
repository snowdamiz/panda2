package objectstore

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestR2UploadSignsPutAndReturnsPublicURL(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotHash string
	var gotType string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		gotHash = r.Header.Get("X-Amz-Content-Sha256")
		gotType = r.Header.Get("Content-Type")
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBody = string(data)
		w.Header().Set("ETag", `"etag-1"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewR2Client(R2Config{
		Endpoint:        server.URL,
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		Bucket:          "bucket",
		PublicBaseURL:   "https://cdn.example.test",
		Prefix:          "clips",
		Now: func() time.Time {
			return time.Date(2026, 6, 28, 12, 13, 14, 0, time.UTC)
		},
	})
	result, err := client.Upload(context.Background(), UploadRequest{
		Key:         "guild-1/request-1/clip one.mp4",
		ContentType: "video/mp4",
		Body:        []byte("clip-bytes"),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if gotPath != "/bucket/clips/guild-1/request-1/clip%20one.mp4" {
		t.Fatalf("unexpected upload path %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=access/20260628/auto/s3/aws4_request") {
		t.Fatalf("unexpected authorization header %q", gotAuth)
	}
	if gotHash == "" {
		t.Fatal("expected payload hash header")
	}
	if gotType != "video/mp4" || gotBody != "clip-bytes" {
		t.Fatalf("unexpected upload type/body type=%q body=%q", gotType, gotBody)
	}
	if result.Key != "clips/guild-1/request-1/clip one.mp4" || result.URL != "https://cdn.example.test/clips/guild-1/request-1/clip%20one.mp4" || result.SizeBytes != int64(len("clip-bytes")) || result.ETag != "etag-1" {
		t.Fatalf("unexpected upload result: %+v", result)
	}
}

func newSigningR2Client() *R2Client {
	return NewR2Client(R2Config{
		Endpoint:        "https://account.r2.cloudflarestorage.com",
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		Bucket:          "bucket",
		PublicBaseURL:   "https://cdn.example.test",
		Prefix:          "clips",
		Now: func() time.Time {
			return time.Date(2026, 6, 28, 12, 13, 14, 0, time.UTC)
		},
	})
}

func TestR2PresignGetURL(t *testing.T) {
	client := newSigningR2Client()
	signed, err := client.PresignGetURL("clips/guild-1/request-1/clip one.mp4", 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignGetURL: %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	if parsed.Host != "account.r2.cloudflarestorage.com" {
		t.Fatalf("presigned URL should target the S3 endpoint host, got %q", parsed.Host)
	}
	if parsed.EscapedPath() != "/bucket/clips/guild-1/request-1/clip%20one.mp4" {
		t.Fatalf("unexpected presigned path %q", parsed.EscapedPath())
	}
	query := parsed.Query()
	if query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		t.Fatalf("unexpected algorithm %q", query.Get("X-Amz-Algorithm"))
	}
	if query.Get("X-Amz-Credential") != "access/20260628/auto/s3/aws4_request" {
		t.Fatalf("unexpected credential %q", query.Get("X-Amz-Credential"))
	}
	if query.Get("X-Amz-Expires") != "300" {
		t.Fatalf("unexpected expires %q", query.Get("X-Amz-Expires"))
	}
	if query.Get("X-Amz-SignedHeaders") != "host" {
		t.Fatalf("unexpected signed headers %q", query.Get("X-Amz-SignedHeaders"))
	}
	sig := query.Get("X-Amz-Signature")
	if len(sig) != 64 {
		t.Fatalf("expected 64-char hex signature, got %q", sig)
	}
	if _, err := hex.DecodeString(sig); err != nil {
		t.Fatalf("signature is not hex: %v", err)
	}
	// Deterministic for a fixed clock.
	again, err := client.PresignGetURL("clips/guild-1/request-1/clip one.mp4", 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignGetURL (again): %v", err)
	}
	if again != signed {
		t.Fatal("presigned URL should be deterministic for a fixed clock")
	}
}

func TestR2PresignDownloadURLAddsDisposition(t *testing.T) {
	client := newSigningR2Client()
	signed, err := client.PresignDownloadURL("clips/clip.mp4", 5*time.Minute, "my-clip.mp4")
	if err != nil {
		t.Fatalf("PresignDownloadURL: %v", err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	disposition := parsed.Query().Get("response-content-disposition")
	if disposition != `attachment; filename="my-clip.mp4"` {
		t.Fatalf("unexpected content disposition %q", disposition)
	}
	if len(parsed.Query().Get("X-Amz-Signature")) != 64 {
		t.Fatal("expected a signature covering the disposition override")
	}
}

func TestR2PresignClampsTTL(t *testing.T) {
	client := newSigningR2Client()
	signed, err := client.PresignGetURL("clips/clip.mp4", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PresignGetURL: %v", err)
	}
	parsed, _ := url.Parse(signed)
	if got := parsed.Query().Get("X-Amz-Expires"); got != "604800" {
		t.Fatalf("expected TTL clamped to 604800, got %q", got)
	}
}

func TestR2Delete(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewR2Client(R2Config{
		Endpoint:        server.URL,
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		Bucket:          "bucket",
		PublicBaseURL:   "https://cdn.example.test",
		Now: func() time.Time {
			return time.Date(2026, 6, 28, 12, 13, 14, 0, time.UTC)
		},
	})
	if err := client.Delete(context.Background(), "clips/guild-1/clip.mp4"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("expected DELETE, got %q", gotMethod)
	}
	if gotPath != "/bucket/clips/guild-1/clip.mp4" {
		t.Fatalf("unexpected delete path %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=access/20260628/auto/s3/aws4_request") {
		t.Fatalf("unexpected authorization header %q", gotAuth)
	}
}

func TestR2DeleteTreats404AsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client := NewR2Client(R2Config{
		Endpoint:        server.URL,
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		Bucket:          "bucket",
		PublicBaseURL:   "https://cdn.example.test",
	})
	if err := client.Delete(context.Background(), "clips/missing.mp4"); err != nil {
		t.Fatalf("Delete should treat 404 as success, got %v", err)
	}
}

func TestR2RequiresConfiguration(t *testing.T) {
	client := NewR2Client(R2Config{})
	if client.Configured() {
		t.Fatal("empty R2 client should not be configured")
	}
	_, err := client.Upload(context.Background(), UploadRequest{Key: "clip.mp4", Body: []byte("clip")})
	if err == nil || !strings.Contains(err.Error(), ErrNotConfigured.Error()) {
		t.Fatalf("expected not configured error, got %v", err)
	}
}
