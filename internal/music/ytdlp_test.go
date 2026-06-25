package music

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestYTDLPResolveKeepsPublicURLAndDirectStreamURL(t *testing.T) {
	ytdlpPath := writeTestExecutable(t, "yt-dlp", `{
		"id": "track-1",
		"title": "Test Track",
		"webpage_url": "https://www.youtube.com/watch?v=track-1",
		"url": "https://rr.example.test/videoplayback?expire=1",
		"uploader": "Uploader",
		"duration": 123,
		"http_headers": {
			"User-Agent": "yt-dlp-test",
			"Referer": "https://www.youtube.com/"
		}
	}`)
	ffmpegPath := writeTestExecutable(t, "ffmpeg", "")
	client := NewYTDLP(YTDLPConfig{
		YTDLPPath:     ytdlpPath,
		FFmpegPath:    ffmpegPath,
		LookupTimeout: time.Second,
	})

	track, err := client.Resolve(context.Background(), "test track")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if track.URL != "https://www.youtube.com/watch?v=track-1" {
		t.Fatalf("expected public URL for embeds/actions, got %q", track.URL)
	}
	if track.StreamURL != "https://rr.example.test/videoplayback?expire=1" {
		t.Fatalf("expected direct stream URL, got %q", track.StreamURL)
	}
	if track.StreamHeaders["User-Agent"] != "yt-dlp-test" || track.StreamHeaders["Referer"] != "https://www.youtube.com/" {
		t.Fatalf("expected stream headers from yt-dlp metadata, got %+v", track.StreamHeaders)
	}
}

func TestFFmpegOpusArgsUseDirectHTTPHeadersAndFastEncode(t *testing.T) {
	args := ffmpegOpusArgs("https://media.example.test/audio", "128k", map[string]string{
		"User-Agent": "Panda",
		"Referer":    "https://example.test/watch",
		"Accept":     "audio/*\nignored",
	}, true)
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"-reconnect\x001",
		"-reconnect_streamed\x001",
		"-reconnect_on_network_error\x001",
		"-user_agent\x00Panda",
		"-referer\x00https://example.test/watch",
		"-compression_level\x001",
		"-frame_duration\x0020",
		"-i\x00https://media.example.test/audio",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected ffmpeg args to contain %q, got %+v", want, args)
		}
	}
	if strings.Contains(joined, "\nignored") || strings.Contains(joined, "\rignored") {
		t.Fatalf("expected sanitized header args, got %+v", args)
	}
}

func TestShouldStreamDirectOnlyForTrueDirectSources(t *testing.T) {
	if shouldStreamDirect(Track{
		URL:       "https://www.youtube.com/watch?v=track-1",
		Query:     "test track",
		StreamURL: "https://rr1---sn.example.googlevideo.com/videoplayback?expire=1",
	}) {
		t.Fatal("resolved tracks with public lookup URLs should stream through yt-dlp at playback time")
	}
	if !shouldStreamDirect(Track{
		URL:       "https://media.example.test/audio.opus",
		StreamURL: "https://media.example.test/audio.opus",
	}) {
		t.Fatal("matching direct media URL should stream directly")
	}
	if !shouldStreamDirect(Track{
		StreamURL: "https://media.example.test/audio.opus",
	}) {
		t.Fatal("track with only a direct stream URL should stream directly")
	}
}

func writeTestExecutable(t *testing.T, name string, jsonOutput string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, name+".cmd")
		body := "@echo off\r\n"
		if strings.TrimSpace(jsonOutput) != "" {
			body += "echo " + strings.ReplaceAll(strings.TrimSpace(jsonOutput), `"`, `\"`) + "\r\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("write executable: %v", err)
		}
		return path
	}
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\n"
	if strings.TrimSpace(jsonOutput) != "" {
		body += "cat <<'JSON'\n" + strings.TrimSpace(jsonOutput) + "\nJSON\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}
