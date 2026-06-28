package youtube

import (
	"context"
	"encoding/json"
	"io"
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
{"id":"abc123","title":"First Result","uploader":"Creator","thumbnails":[{"url":"https://i.ytimg.com/vi/abc123/default.jpg","width":120,"height":90},{"url":"https://i.ytimg.com/vi/abc123/hqdefault.jpg","width":480,"height":360}],"duration":124,"upload_date":"20260620"}
{"id":"def456","title":"Second Result","webpage_url":"https://www.youtube.com/watch?v=def456","uploader":"Other"}
`), 3)
	if len(candidates) != 2 {
		t.Fatalf("expected two candidates, got %+v", candidates)
	}
	if candidates[0].Title != "First Result" ||
		candidates[0].URL != "https://www.youtube.com/watch?v=abc123" ||
		candidates[0].ThumbnailURL != "https://i.ytimg.com/vi/abc123/hqdefault.jpg" ||
		candidates[0].Duration != 124*time.Second ||
		candidates[0].UploadDate.Format("2006-01-02") != "2026-06-20" {
		t.Fatalf("unexpected first candidate: %+v", candidates[0])
	}
	if candidates[1].URL != "https://www.youtube.com/watch?v=def456" {
		t.Fatalf("unexpected second candidate: %+v", candidates[1])
	}
}

func TestSearchSupportsDateSortedFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `case "$*" in
*"--dateafter 20260101"*"--datebefore 20260201"*"ytsearch10:creator channel"*)
  cat <<'JSON'
{"id":"middle","title":"Middle Upload","webpage_url":"https://www.youtube.com/watch?v=middle","uploader":"Creator","upload_date":"20260120"}
{"id":"outside","title":"Outside Upload","webpage_url":"https://www.youtube.com/watch?v=outside","uploader":"Creator","upload_date":"20251231"}
{"id":"oldest","title":"Oldest Upload","webpage_url":"https://www.youtube.com/watch?v=oldest","uploader":"Creator","upload_date":"20260102"}
{"id":"newest","title":"Newest Upload","webpage_url":"https://www.youtube.com/watch?v=newest","uploader":"Creator","upload_date":"20260130"}
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
		HTTPClient:    failingHTTPClient(),
		LookupTimeout: 5 * time.Second,
	})

	candidates, err := service.Search(context.Background(), SearchRequest{
		Query:      "creator channel",
		Limit:      3,
		SortBy:     "upload_date",
		DateAfter:  "2026-01-01",
		DateBefore: "20260201",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected search result limit to remain three, got %+v", candidates)
	}
	if candidates[0].Title != "Newest Upload" || candidates[0].UploadDate.Format("2006-01-02") != "2026-01-30" {
		t.Fatalf("unexpected newest candidate: %+v", candidates[0])
	}
	if candidates[2].Title != "Oldest Upload" {
		t.Fatalf("expected outside date candidate to be filtered out, got %+v", candidates)
	}
}

func TestSearchChannelUploadsUsesVideosTab(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `case "$*" in
*"ytsearch5:Orangie Builds"*)
  cat <<'JSON'
{"id":"old-relevance-result","title":"Old Search Result","webpage_url":"https://www.youtube.com/watch?v=old","uploader":"Orangie Builds","channel":"Orangie Builds","channel_url":"https://www.youtube.com/@orangiebuilds","uploader_id":"@orangiebuilds"}
JSON
  ;;
*"--extractor-args youtubetab:approximate_date"*"--playlist-end 3"*"https://www.youtube.com/@orangiebuilds/videos"*)
  cat <<'JSON'
{"id":"latest","title":"I Launched My First App. Here's How.","webpage_url":"https://www.youtube.com/watch?v=latest","playlist_uploader":"Orangie Builds","thumbnail":"https://i.ytimg.com/vi/latest/hqdefault.jpg","duration":616,"upload_date":"20260627"}
{"id":"previous","title":"These People Created These Video Games with AI","webpage_url":"https://www.youtube.com/watch?v=previous","playlist_uploader":"Orangie Builds","duration":972,"upload_date":"20260617"}
{"id":"third","title":"How To Make a Video Game with AI (NO Coding experience)","webpage_url":"https://www.youtube.com/watch?v=third","playlist_uploader":"Orangie Builds","duration":516,"upload_date":"20260614"}
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
		HTTPClient:    failingHTTPClient(),
		LookupTimeout: 5 * time.Second,
	})

	candidates, err := service.Search(context.Background(), SearchRequest{
		Query:  "Orangie Builds",
		Limit:  3,
		Source: "channel_uploads",
		SortBy: "upload_date",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("expected three channel upload candidates, got %+v", candidates)
	}
	first := candidates[0]
	if first.Title != "I Launched My First App. Here's How." ||
		first.Uploader != "Orangie Builds" ||
		first.UploadDate.Format("2006-01-02") != "2026-06-27" ||
		first.ThumbnailURL == "" {
		t.Fatalf("unexpected first channel upload candidate: %+v", first)
	}
	if candidates[1].Title != "These People Created These Video Games with AI" {
		t.Fatalf("expected uploads tab order, got %+v", candidates)
	}
}

func TestSearchChannelUploadsUsesFeedBeforeYTDLP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `echo "yt-dlp should not be called for feed-backed channel uploads" >&2
exit 1`)
	ffmpegPath := writeShellExecutable(t, "ffmpeg", ":")
	requests := []string{}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.String())
		switch {
		case request.URL.Host == "www.youtube.com" && request.URL.Path == "/results":
			return stringResponse(request, http.StatusOK, `"channelId":"UCuIJ_WzfOUmGUwJyPSKTMvg","title":{"simpleText":"Orangie Builds"}`), nil
		case request.URL.Host == "www.youtube.com" && request.URL.Path == "/feeds/videos.xml":
			return stringResponse(request, http.StatusOK, `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/" xmlns="http://www.w3.org/2005/Atom">
 <title>Orangie Builds</title>
 <author><name>Orangie Builds</name></author>
 <entry>
  <yt:videoId>latest</yt:videoId>
  <title>How I Built My First App in 19 Days</title>
  <link rel="alternate" href="https://www.youtube.com/watch?v=latest"/>
  <author><name>Orangie Builds</name></author>
  <published>2026-06-27T20:57:17+00:00</published>
  <media:group>
   <media:title>How I Built My First App in 19 Days</media:title>
   <media:thumbnail url="https://i3.ytimg.com/vi/latest/hqdefault.jpg" width="480" height="360"/>
  </media:group>
 </entry>
 <entry>
  <yt:videoId>previous</yt:videoId>
  <title>These People Created These Video Games with AI</title>
  <link rel="alternate" href="https://www.youtube.com/watch?v=previous"/>
  <author><name>Orangie Builds</name></author>
  <published>2026-06-17T16:00:00+00:00</published>
  <media:group><media:thumbnail url="https://i3.ytimg.com/vi/previous/hqdefault.jpg"/></media:group>
 </entry>
</feed>`), nil
		default:
			return stringResponse(request, http.StatusNotFound, "not found"), nil
		}
	})}
	service := NewService(Config{
		YTDLPPath:     ytdlpPath,
		FFmpegPath:    ffmpegPath,
		HTTPClient:    httpClient,
		LookupTimeout: 5 * time.Second,
	})

	candidates, err := service.Search(context.Background(), SearchRequest{
		Query:  "orangie builds channel",
		Limit:  3,
		Source: "channel_uploads",
		SortBy: "upload_date",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected feed candidates, got %+v", candidates)
	}
	first := candidates[0]
	if first.Title != "How I Built My First App in 19 Days" ||
		first.URL != "https://www.youtube.com/watch?v=latest" ||
		first.Uploader != "Orangie Builds" ||
		first.ThumbnailURL == "" ||
		first.UploadDate.Format("2006-01-02") != "2026-06-27" {
		t.Fatalf("unexpected feed-backed first candidate: %+v", first)
	}
	if len(requests) != 2 ||
		!strings.Contains(requests[0], "/results?search_query=orangie+builds+channel") ||
		!strings.Contains(requests[1], "/feeds/videos.xml?channel_id=UCuIJ_WzfOUmGUwJyPSKTMvg") {
		t.Fatalf("unexpected feed lookup requests: %+v", requests)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func stringResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func failingHTTPClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return stringResponse(request, http.StatusServiceUnavailable, "unavailable"), nil
	})}
}
