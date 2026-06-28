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

func TestParseSearchCandidatesBuildsWatchURLsAndThumbnails(t *testing.T) {
	candidates := parseSearchCandidates([]byte(`
{"id":"abc123","title":"First Result","uploader":"Creator","thumbnails":[{"url":"https://i.ytimg.com/vi/abc123/default.jpg","width":120,"height":90},{"url":"https://i.ytimg.com/vi/abc123/hqdefault.jpg","width":480,"height":360}],"duration":124}
{"id":"def456","title":"Second Result","webpage_url":"https://www.youtube.com/watch?v=def456","uploader":"Other"}
`), 3)
	if len(candidates) != 2 {
		t.Fatalf("expected two candidates, got %+v", candidates)
	}
	if candidates[0].Title != "First Result" ||
		candidates[0].URL != "https://www.youtube.com/watch?v=abc123" ||
		candidates[0].ThumbnailURL != "https://i.ytimg.com/vi/abc123/hqdefault.jpg" ||
		candidates[0].Duration != 124*time.Second {
		t.Fatalf("unexpected first candidate: %+v", candidates[0])
	}
	if candidates[1].URL != "https://www.youtube.com/watch?v=def456" {
		t.Fatalf("unexpected second candidate: %+v", candidates[1])
	}
}

func TestSearchEnrichesFlatCandidatesWithThumbnails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `case "$*" in
*"ytsearch2:rare video"*)
  cat <<'JSON'
{"id":"abc123","title":"Flat First"}
{"id":"def456","title":"Flat Second","webpage_url":"https://www.youtube.com/watch?v=def456","uploader":"Second Creator","thumbnail":"https://img.example.test/def.jpg","duration":42}
JSON
  ;;
*"abc123"*)
  cat <<'JSON'
{"id":"abc123","title":"Full First","webpage_url":"https://www.youtube.com/watch?v=abc123","uploader":"Full Creator","duration":88,"thumbnails":[{"url":"https://img.example.test/abc-small.jpg","width":120,"height":90},{"url":"https://img.example.test/abc-large.jpg","width":480,"height":360}]}
JSON
  ;;
*)
  echo "unexpected yt-dlp args: $*" >&2
  exit 1
  ;;
esac`)
	ffmpegPath := writeShellExecutable(t, "ffmpeg", ":")
	service := NewService(Config{
		YTDLPPath:     ytdlpPath,
		FFmpegPath:    ffmpegPath,
		LookupTimeout: 5 * time.Second,
	})

	candidates, err := service.Search(context.Background(), SearchRequest{Query: "rare video", Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected two candidates, got %+v", candidates)
	}
	first := candidates[0]
	if first.Title != "Flat First" ||
		first.URL != "https://www.youtube.com/watch?v=abc123" ||
		first.Uploader != "Full Creator" ||
		first.ThumbnailURL != "https://img.example.test/abc-large.jpg" ||
		first.Duration != 88*time.Second {
		t.Fatalf("unexpected enriched first candidate: %+v", first)
	}
	second := candidates[1]
	if second.ThumbnailURL != "https://img.example.test/def.jpg" || second.Uploader != "Second Creator" || second.Duration != 42*time.Second {
		t.Fatalf("unexpected second candidate: %+v", second)
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
