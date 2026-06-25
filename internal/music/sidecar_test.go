package music

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSidecarManagerUsesConfiguredExecutables(t *testing.T) {
	dir := t.TempDir()
	ytdlp := executableFixture(t, dir, "yt-dlp")
	ffmpeg := executableFixture(t, dir, "ffmpeg")

	manager := NewSidecarManager(SidecarConfig{
		Dir:        filepath.Join(dir, "unused"),
		YTDLPPath:  ytdlp,
		FFmpegPath: ffmpeg,
		HTTPClient: sidecarHTTPClient("not used"),
	})
	tools, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if tools.YTDLPPath != ytdlp || tools.FFmpegPath != ffmpeg {
		t.Fatalf("expected configured tool paths, got %#v", tools)
	}
}

func TestSidecarManagerEnsuresOnlyFFmpegFromConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := executableFixture(t, dir, "ffmpeg")

	manager := NewSidecarManager(SidecarConfig{
		FFmpegPath: ffmpeg,
		HTTPClient: sidecarHTTPClient("not used"),
	})
	path, err := manager.EnsureFFmpeg(context.Background())
	if err != nil {
		t.Fatalf("EnsureFFmpeg returned error: %v", err)
	}
	if path != ffmpeg {
		t.Fatalf("expected configured ffmpeg path %q, got %q", ffmpeg, path)
	}
}

func TestSidecarManagerDownloadsMissingTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
	dir := t.TempDir()
	manager := NewSidecarManager(SidecarConfig{
		Dir:        dir,
		HTTPClient: sidecarHTTPClient("#!/bin/sh\nexit 0\n"),
	})
	tools, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	for _, path := range []string{tools.YTDLPPath, tools.FFmpegPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected sidecar %q to exist: %v", path, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("expected sidecar %q to be executable, mode=%s", path, info.Mode())
		}
	}
}

func TestSidecarURLsSupportCurrentPlatform(t *testing.T) {
	ytdlpURL, ytdlpErr := ytdlpSidecarURL()
	ffmpegURL, ffmpegErr := ffmpegSidecarURL()
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if ytdlpErr != nil {
			t.Fatalf("expected yt-dlp sidecar URL for current platform: %v", ytdlpErr)
		}
		if ffmpegErr != nil && !(runtime.GOOS == "windows" && runtime.GOARCH != "amd64") {
			t.Fatalf("expected ffmpeg sidecar URL for current platform: %v", ffmpegErr)
		}
	}
	if ytdlpErr == nil && !strings.Contains(ytdlpURL, "yt-dlp") {
		t.Fatalf("unexpected yt-dlp URL %q", ytdlpURL)
	}
	if ffmpegErr == nil && !strings.Contains(ffmpegURL, "ffmpeg") {
		t.Fatalf("unexpected ffmpeg URL %q", ffmpegURL)
	}
}

func executableFixture(t *testing.T, dir string, name string) string {
	t.Helper()
	path := filepath.Join(dir, sidecarExecutableName(name))
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable fixture: %v", err)
	}
	return path
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func sidecarHTTPClient(body string) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}
}
