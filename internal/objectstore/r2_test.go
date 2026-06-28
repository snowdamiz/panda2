package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
