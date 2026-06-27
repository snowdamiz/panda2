package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSummarizeResolvesChunksAndTranscribesYouTubeAudio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `case " $* " in
*" --dump-json "*)
  printf '%s\n' '{"id":"video-1","title":"Deep Dive","webpage_url":"https://www.youtube.com/watch?v=video-1","uploader":"Teacher","duration":612}'
  ;;
*)
  printf 'fake media stream'
  ;;
esac`)
	ffmpegPath := writeShellExecutable(t, "ffmpeg", `last=""
for arg in "$@"; do
  last="$arg"
done
cat >/dev/null
printf 'audio one' > "$(printf "$last" 0)"
printf 'audio two' > "$(printf "$last" 1)"`)
	var uploaded []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer lemon-key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader: %v", err)
		}
		form, err := reader.ReadForm(1 << 20)
		if err != nil {
			t.Fatalf("ReadForm: %v", err)
		}
		files := form.File["file"]
		if len(files) != 1 {
			t.Fatalf("expected one audio file, got %d", len(files))
		}
		uploaded = append(uploaded, files[0].Filename)
		if got := form.Value["response_format"]; len(got) != 1 || got[0] != "verbose_json" {
			t.Fatalf("expected verbose_json response format, got %#v", got)
		}
		if got := form.Value["language"]; len(got) != 1 || got[0] != "en" {
			t.Fatalf("expected language field, got %#v", got)
		}
		index := len(uploaded)
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "Transcript chunk " + string(rune('0'+index))})
	}))
	defer server.Close()

	service := NewService(Config{
		APIKey:        "lemon-key",
		BaseURL:       server.URL + "/v1",
		YTDLPPath:     ytdlpPath,
		FFmpegPath:    ffmpegPath,
		ChunkDuration: time.Minute,
	})
	result, err := service.Summarize(context.Background(), SummaryRequest{
		Query:    "deep dive video",
		Language: "en",
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if result.Title != "Deep Dive" || result.URL != "https://www.youtube.com/watch?v=video-1" || result.Uploader != "Teacher" || result.Duration != 612*time.Second {
		t.Fatalf("unexpected metadata: %+v", result)
	}
	if result.Transcript != "Transcript chunk 1\n\nTranscript chunk 2" {
		t.Fatalf("unexpected transcript %q", result.Transcript)
	}
	if result.ChunkCount != 2 || len(uploaded) != 2 || uploaded[0] != "chunk-00000.wav" || uploaded[1] != "chunk-00001.wav" {
		t.Fatalf("unexpected chunks result=%+v uploaded=%+v", result, uploaded)
	}
}

func TestSummarizeRequiresLemonfoxKey(t *testing.T) {
	service := NewService(Config{})
	if service.Configured() {
		t.Fatal("expected unconfigured service without API key")
	}
	_, err := service.Summarize(context.Background(), SummaryRequest{Query: "video"})
	if err == nil || !strings.Contains(err.Error(), ErrNotConfigured.Error()) {
		t.Fatalf("expected not configured error, got %v", err)
	}
}

func TestParseMetadataAcceptsPlaylistEntry(t *testing.T) {
	metadata, err := parseMetadata([]byte(`{"_type":"playlist","entries":[{"title":"First","webpage_url":"https://youtu.be/1"}]}`))
	if err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if metadata.Title != "First" || metadata.WebpageURL != "https://youtu.be/1" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

func writeShellExecutable(t *testing.T, name string, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nset -eu\n" + strings.TrimSpace(body) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}
