package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/objectstore"
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
  out=""
  previous=""
  for arg in "$@"; do
    if [ "$previous" = "--output" ]; then
      out="$arg"
    fi
    previous="$arg"
  done
  if [ -z "$out" ]; then
    echo "missing output template" >&2
    exit 1
  fi
  out="$(printf '%s' "$out" | sed 's/%(ext)s/m4a/g')"
  printf 'fake audio bytes' > "$out"
  ;;
esac`)
	ffmpegPath := writeShellExecutable(t, "ffmpeg", `last=""
for arg in "$@"; do
  last="$arg"
done
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

func TestYouTubeYTDLPDownloadArgsIncludeRequestHardening(t *testing.T) {
	args := youtubeYTDLPDownloadArgs("--format", "best", "https://www.youtube.com/watch?v=test")
	requireArgValue(t, args, "--user-agent", youtubeYTDLPUserAgent)
	requireArgValue(t, args, "--referer", "https://www.youtube.com/")
	requireArgValue(t, args, "--add-headers", "Accept-Language:en-US,en;q=0.9")
	requireArgValue(t, args, "--extractor-args", "youtube:player_client="+youtubeYTDLPPlayerClients)
	requireArgValue(t, args, "--retries", "3")
	requireArgValue(t, args, "--fragment-retries", "3")
	requireArgValue(t, args, "--retry-sleep", "exp=1:20")
	requireArgValue(t, args, "--extractor-retries", "3")
	requireArg(t, args, "--no-cache-dir")
	requireArg(t, args, "--no-progress")
	requireArgValue(t, args, "--format", "best")
}

func TestExtractAudioChunksReportsYTDLPFailureWithoutPipeNoise(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `case " $* " in
*" --format bestaudio[ext=m4a]/bestaudio/best "*)
  echo "ERROR: unable to download video data: HTTP Error 403: Forbidden" >&2
  exit 1
  ;;
*)
  echo "unexpected yt-dlp args: $*" >&2
  exit 1
  ;;
esac`)
	ffmpegPath := writeShellExecutable(t, "ffmpeg", `echo "ffmpeg should not run after yt-dlp download failure" >&2
exit 1`)
	service := NewService(Config{
		APIKey:         "lemon-key",
		YTDLPPath:      ytdlpPath,
		FFmpegPath:     ffmpegPath,
		ProcessTimeout: time.Second,
	})

	_, err := service.extractAudioChunks(context.Background(), ToolPaths{YTDLPPath: ytdlpPath, FFmpegPath: ffmpegPath}, "https://www.youtube.com/watch?v=blocked", t.TempDir())
	if err == nil {
		t.Fatal("expected audio extraction error")
	}
	message := err.Error()
	if !strings.Contains(message, "HTTP Error 403") {
		t.Fatalf("expected yt-dlp 403 in error, got %v", err)
	}
	if strings.Contains(message, "ffmpeg") || strings.Contains(message, "pipe:0") {
		t.Fatalf("yt-dlp download failure should not include ffmpeg pipe noise, got %v", err)
	}
}

func TestClipTranscribesDetectsRendersAndUploads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `case " $* " in
*" --dump-json "*)
  printf '%s\n' '{"id":"video-1","title":"Deep Dive","webpage_url":"https://www.youtube.com/watch?v=video-1","uploader":"Teacher","duration":420}'
  ;;
*" --format bestvideo"*)
  out=""
  previous=""
  for arg in "$@"; do
    if [ "$previous" = "--output" ]; then
      out="$arg"
    fi
    previous="$arg"
  done
  if [ -z "$out" ]; then
    echo "missing output template" >&2
    exit 1
  fi
  out="$(printf '%s' "$out" | sed 's/%(ext)s/mp4/g')"
  printf 'source video bytes' > "$out"
  ;;
*" --format bestaudio[ext=m4a]/bestaudio/best "*)
  out=""
  previous=""
  for arg in "$@"; do
    if [ "$previous" = "--output" ]; then
      out="$arg"
    fi
    previous="$arg"
  done
  if [ -z "$out" ]; then
    echo "missing output template" >&2
    exit 1
  fi
  out="$(printf '%s' "$out" | sed 's/%(ext)s/m4a/g')"
  printf 'fake audio bytes' > "$out"
  ;;
*)
  printf 'fake media stream'
  ;;
esac`)
	ffmpegPath := writeShellExecutable(t, "ffmpeg", `last=""
for arg in "$@"; do
  last="$arg"
done
case " $* " in
*" -filters "*)
  echo ' ... subtitles         V->V       Render text subtitles onto input video using the libass library.'
  ;;
*" -f segment "*)
  printf 'audio one' > "$(printf "$last" 0)"
  printf 'audio two' > "$(printf "$last" 1)"
  ;;
*" -f concat "*)
  printf 'spliced clip bytes' > "$last"
  ;;
*" -frames:v 1 "*)
  for path in "$@"; do
    case "$path" in
    *.jpg)
      if base64 --decode > "$path" 2>/dev/null <<'B64'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=
B64
      then
        :
      else
        base64 -D > "$path" <<'B64'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=
B64
      fi
      ;;
    esac
  done
  ;;
*" +faststart "*)
  cat >/dev/null
  printf 'clip bytes' > "$last"
  ;;
*)
  echo "unexpected ffmpeg args: $*" >&2
  exit 1
  ;;
esac`)
	transcriptionCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transcriptionCount++
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader: %v", err)
		}
		form, err := reader.ReadForm(1 << 20)
		if err != nil {
			t.Fatalf("ReadForm: %v", err)
		}
		if got := form.Value["language"]; len(got) != 1 || got[0] != "en" {
			t.Fatalf("expected language field, got %#v", got)
		}
		switch transcriptionCount {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "Intro",
				"segments": []map[string]any{
					{"start": 1.0, "end": 3.0, "text": "Intro", "words": []map[string]any{
						{"start": 1.0, "end": 1.6, "word": "Intro"},
					}},
					{"start": 10.0, "end": 14.0, "text": "Setup", "words": []map[string]any{
						{"start": 10.0, "end": 10.5, "word": "Setup"},
					}},
				},
			})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "Best part begins. Best part lands.",
				"segments": []map[string]any{
					{"start": 3.0, "end": 6.0, "text": "Best part begins.", "words": []map[string]any{
						{"start": 3.0, "end": 3.4, "word": "Best"},
						{"start": 3.45, "end": 3.9, "word": "part"},
						{"start": 5.6, "end": 6.0, "word": "begins."},
					}},
					{"start": 9.0, "end": 12.0, "text": "Best part lands.", "words": []map[string]any{
						{"start": 9.0, "end": 9.4, "word": "Best"},
						{"start": 9.45, "end": 9.9, "word": "part"},
						{"start": 11.6, "end": 12.0, "word": "lands."},
					}},
				},
			})
		default:
			t.Fatalf("unexpected transcription count %d", transcriptionCount)
		}
	}))
	defer server.Close()
	detector := &fakeClipDetector{
		configured: true,
		result: ClipDetectionResult{
			Clips: []ClipDecision{
				{
					Rank:  1,
					Title: "Best Moment",
					Type:  "spliced",
					Segments: []ClipDecisionSegment{
						{StartWordID: "w_0003", EndWordID: "w_0005", StartSeconds: 63, EndSeconds: 66, SpeechStartSeconds: 63, SpeechEndSeconds: 66, Transcript: "Best part begins."},
						{StartWordID: "w_0006", EndWordID: "w_0008", StartSeconds: 69, EndSeconds: 72, SpeechStartSeconds: 69, SpeechEndSeconds: 72, Transcript: "Best part lands."},
					},
					Reason:            "This segment contains the requested moment with dead air removed.",
					Confidence:        0.9,
					ViralityScore:     86,
					HookScore:         88,
					RetentionScore:    80,
					ShareabilityScore: 84,
					DurationPolicy:    "requested_duration",
					ExceptionReason:   "",
				},
				{
					Rank:  2,
					Title: "Setup Payoff",
					Type:  "continuous",
					Segments: []ClipDecisionSegment{
						{StartWordID: "w_0003", EndWordID: "w_0008", StartSeconds: 63, EndSeconds: 72, SpeechStartSeconds: 63, SpeechEndSeconds: 72, Transcript: "Best part begins. Best part lands."},
					},
					Reason:            "A fuller version with setup and payoff.",
					Confidence:        0.82,
					ViralityScore:     78,
					HookScore:         76,
					RetentionScore:    79,
					ShareabilityScore: 77,
					DurationPolicy:    "requested_duration",
					ExceptionReason:   "",
				},
			},
		},
	}
	planner := &fakeClipPlanner{configured: true}
	uploader := &fakeClipUploader{configured: true, baseURL: "https://cdn.example.test/clips"}
	service := NewService(Config{
		APIKey:          "lemon-key",
		BaseURL:         server.URL + "/v1",
		YTDLPPath:       ytdlpPath,
		FFmpegPath:      ffmpegPath,
		ChunkDuration:   time.Minute,
		ClipDetector:    detector,
		ClipPlanner:     planner,
		ClipUploader:    uploader,
		ClipMinDuration: 5 * time.Second,
		ClipMaxDuration: 30 * time.Second,
		ClipMaxBytes:    1 << 20,
		CaptionFontPath: testCaptionFontPath(t),
	})

	var progress []string
	result, err := service.Clip(context.Background(), ClipRequest{
		Query:               "deep dive video",
		Language:            "en",
		AspectRatio:         "9:16",
		LayoutInstructions:  "keep the full frame readable",
		CaptionInstructions: "random styled captions",
		GuildID:             "guild 1",
		RequestID:           "request 1",
		Progress: func(update ClipProgress) {
			progress = append(progress, update.Status)
		},
	})
	if err != nil {
		t.Fatalf("Clip: %v", err)
	}
	expectedProgress := []string{
		"Searching",
		"Transcribing",
		"Building clips",
		"Rendering",
		"Planning layout 1/2",
		"Rendering 1/2",
		"Uploading 1/2",
		"Planning layout 2/2",
		"Rendering 2/2",
		"Uploading 2/2",
	}
	if strings.Join(progress, "|") != strings.Join(expectedProgress, "|") {
		t.Fatalf("unexpected clip progress: got %+v want %+v", progress, expectedProgress)
	}
	if len(detector.requests) != 1 {
		t.Fatalf("expected one detector request, got %d", len(detector.requests))
	}
	if len(planner.requests) != 2 {
		t.Fatalf("expected two composition planner requests, got %d", len(planner.requests))
	}
	if planner.requests[0].RequestedAspect != "9:16" || planner.requests[0].LayoutInstructions != "keep the full frame readable" || planner.requests[0].CaptionMode != "auto" || planner.requests[0].CaptionInstructions != "random styled captions" || len(planner.requests[0].Thumbnails) == 0 {
		t.Fatalf("unexpected composition request: %+v", planner.requests[0])
	}
	if len(planner.requests[0].TranscriptTimeline) == 0 || len(planner.requests[0].TranscriptTimeline[0].Words) == 0 || planner.requests[0].TranscriptTimeline[0].Words[0].ID != "w_0003" || planner.requests[0].TranscriptTimeline[0].Words[0].StartSeconds != 63 {
		t.Fatalf("expected word timing in composition request: %+v", planner.requests[0].TranscriptTimeline)
	}
	detection := detector.requests[0]
	if detection.Title != "Deep Dive" || detection.URL != "https://www.youtube.com/watch?v=video-1" || !strings.Contains(detection.Instructions, "viral short-form clips") {
		t.Fatalf("unexpected detector request: %+v", detection)
	}
	if len(detection.Segments) != 4 || detection.Segments[2].StartSeconds != 63 || detection.Segments[2].EndSeconds != 66 || detection.Segments[3].StartSeconds != 69 || detection.Segments[3].EndSeconds != 72 {
		t.Fatalf("expected second chunk segments to be offset, got %+v", detection.Segments)
	}
	if len(uploader.requests) != 4 {
		t.Fatalf("expected video and thumbnail uploads for two clips, got %d", len(uploader.requests))
	}
	upload := uploader.requests[0]
	if upload.Key != "guild-1/request-1/01-best-moment.mp4" || upload.ContentType != "video/mp4" || string(upload.Body) != "spliced clip bytes" {
		t.Fatalf("unexpected upload request: %+v body=%q", upload, string(upload.Body))
	}
	thumbnailUpload := uploader.requests[1]
	if thumbnailUpload.Key != "guild-1/request-1/01-best-moment.jpg" || thumbnailUpload.ContentType != "image/jpeg" || len(thumbnailUpload.Body) == 0 {
		t.Fatalf("unexpected thumbnail upload request: %+v", thumbnailUpload)
	}
	secondUpload := uploader.requests[2]
	if secondUpload.Key != "guild-1/request-1/02-setup-payoff.mp4" || string(secondUpload.Body) != "clip bytes" {
		t.Fatalf("unexpected second upload request: %+v body=%q", secondUpload, string(secondUpload.Body))
	}
	secondThumbnailUpload := uploader.requests[3]
	if secondThumbnailUpload.Key != "guild-1/request-1/02-setup-payoff.jpg" || secondThumbnailUpload.ContentType != "image/jpeg" || len(secondThumbnailUpload.Body) == 0 {
		t.Fatalf("unexpected second thumbnail upload request: %+v", secondThumbnailUpload)
	}
	if len(result.Clips) != 2 || result.Clips[0].WatchURL != "https://cdn.example.test/clips/guild-1/request-1/01-best-moment.mp4" || result.Clips[0].ThumbnailURL != "https://cdn.example.test/clips/guild-1/request-1/01-best-moment.jpg" || result.Clips[0].Type != "spliced" || len(result.Clips[0].Segments) != 2 || result.Clips[0].SourceStartSeconds != 63 || result.Clips[0].SourceEndSeconds != 72 || result.Clips[0].Duration != 6*time.Second || result.TranscriptSegmentCount != 4 {
		t.Fatalf("unexpected clip result: %+v", result)
	}
	if result.Clips[0].AspectRatio != "9:16" || result.Clips[0].LayoutMode != "full_frame" || result.Clips[0].CompositionConfidence != 0.8 {
		t.Fatalf("expected composition metadata on rendered clip, got %+v", result.Clips[0])
	}
	if !result.Clips[0].CaptionRendered || result.Clips[0].CaptionMode != "burned_in" || result.Clips[0].CaptionTimingQuality == "" || result.Clips[0].CaptionReason == "" {
		t.Fatalf("expected caption metadata on rendered clip, got %+v", result.Clips[0])
	}
	if result.Clips[0].CaptionStyleSource != clipCaptionStyleSourceCreativeMix || result.Clips[0].CaptionAnimation != clipCaptionAnimationPop || result.Clips[0].CaptionFontFamily != clipCaptionFontDefault || result.Clips[0].CaptionFontColor != "cyan" || result.Clips[0].CaptionBorderThickness != clipCaptionBorderThick {
		t.Fatalf("expected caption style metadata on rendered clip, got %+v", result.Clips[0])
	}
}

func TestClipSkipsCandidateWhenCompositionPlanningFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ytdlpPath := writeShellExecutable(t, "yt-dlp", `case " $* " in
*" --dump-json "*)
  printf '%s\n' '{"id":"video-1","title":"Deep Dive","webpage_url":"https://www.youtube.com/watch?v=video-1","uploader":"Teacher","duration":420}'
  ;;
*" --format bestvideo"*)
  out=""
  previous=""
  for arg in "$@"; do
    if [ "$previous" = "--output" ]; then
      out="$arg"
    fi
    previous="$arg"
  done
  out="$(printf '%s' "$out" | sed 's/%(ext)s/mp4/g')"
  printf 'source video bytes' > "$out"
  ;;
*" --format bestaudio[ext=m4a]/bestaudio/best "*)
  out=""
  previous=""
  for arg in "$@"; do
    if [ "$previous" = "--output" ]; then
      out="$arg"
    fi
    previous="$arg"
  done
  out="$(printf '%s' "$out" | sed 's/%(ext)s/m4a/g')"
  printf 'fake audio bytes' > "$out"
  ;;
*)
  printf 'fake media stream'
  ;;
esac`)
	ffmpegPath := writeShellExecutable(t, "ffmpeg", `last=""
for arg in "$@"; do
  last="$arg"
done
case " $* " in
*" -filters "*)
  echo ' ... subtitles         V->V       Render text subtitles onto input video using the libass library.'
  ;;
*" -f segment "*)
  printf 'audio one' > "$(printf "$last" 0)"
  printf 'audio two' > "$(printf "$last" 1)"
  ;;
*" -frames:v 1 "*)
  for path in "$@"; do
    case "$path" in
    *.jpg)
      if base64 --decode > "$path" 2>/dev/null <<'B64'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=
B64
      then
        :
      else
        base64 -D > "$path" <<'B64'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=
B64
      fi
      ;;
    esac
  done
  ;;
*" +faststart "*)
  cat >/dev/null
  printf 'clip bytes' > "$last"
  ;;
*)
  echo "unexpected ffmpeg args: $*" >&2
  exit 1
  ;;
esac`)
	transcriptionCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transcriptionCount++
		switch transcriptionCount {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "Intro",
				"segments": []map[string]any{
					{"start": 1.0, "end": 3.0, "text": "Intro", "words": []map[string]any{
						{"start": 1.0, "end": 1.6, "word": "Intro"},
					}},
				},
			})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "Best part begins. Best part lands.",
				"segments": []map[string]any{
					{"start": 3.0, "end": 6.0, "text": "Best part begins.", "words": []map[string]any{
						{"start": 3.0, "end": 3.4, "word": "Best"},
						{"start": 3.45, "end": 3.9, "word": "part"},
						{"start": 5.6, "end": 6.0, "word": "begins."},
					}},
					{"start": 9.0, "end": 12.0, "text": "Best part lands.", "words": []map[string]any{
						{"start": 9.0, "end": 9.4, "word": "Best"},
						{"start": 9.45, "end": 9.9, "word": "part"},
						{"start": 11.6, "end": 12.0, "word": "lands."},
					}},
				},
			})
		default:
			t.Fatalf("unexpected transcription count %d", transcriptionCount)
		}
	}))
	defer server.Close()
	detector := &fakeClipDetector{
		configured: true,
		result: ClipDetectionResult{Clips: []ClipDecision{
			{
				Rank:  1,
				Title: "Broken Layout Candidate",
				Type:  "continuous",
				Segments: []ClipDecisionSegment{{
					StartWordID: "w_0002", EndWordID: "w_0004", StartSeconds: 63, EndSeconds: 66, SpeechStartSeconds: 63, SpeechEndSeconds: 66, Transcript: "Best part begins.",
				}},
				Reason: "First candidate should be skipped after composition failure.", Confidence: 0.9, ViralityScore: 86, HookScore: 88, RetentionScore: 80, ShareabilityScore: 84, DurationPolicy: "requested_duration",
			},
			{
				Rank:  2,
				Title: "Setup Payoff",
				Type:  "continuous",
				Segments: []ClipDecisionSegment{{
					StartWordID: "w_0005", EndWordID: "w_0007", StartSeconds: 69, EndSeconds: 72, SpeechStartSeconds: 69, SpeechEndSeconds: 72, Transcript: "Best part lands.",
				}},
				Reason: "Second candidate should still render.", Confidence: 0.82, ViralityScore: 78, HookScore: 76, RetentionScore: 79, ShareabilityScore: 77, DurationPolicy: "requested_duration",
			},
		}},
	}
	planner := &fakeClipPlanner{
		configured: true,
		errs:       []error{errors.New("clip composition response failed validation after repair: clip composition segment 0: stacked_regions layout requires two or three regions")},
	}
	uploader := &fakeClipUploader{configured: true, baseURL: "https://cdn.example.test/clips"}
	service := NewService(Config{
		APIKey:          "lemon-key",
		BaseURL:         server.URL + "/v1",
		YTDLPPath:       ytdlpPath,
		FFmpegPath:      ffmpegPath,
		ChunkDuration:   time.Minute,
		ClipDetector:    detector,
		ClipPlanner:     planner,
		ClipUploader:    uploader,
		ClipMinDuration: 2 * time.Second,
		ClipMaxDuration: 30 * time.Second,
		ClipMaxBytes:    1 << 20,
		CaptionFontPath: testCaptionFontPath(t),
	})

	result, err := service.Clip(context.Background(), ClipRequest{
		Query:       "deep dive video",
		Language:    "en",
		AspectRatio: "9:16",
		GuildID:     "guild 1",
		RequestID:   "request 1",
	})
	if err != nil {
		t.Fatalf("Clip: %v", err)
	}
	if len(planner.requests) != 2 {
		t.Fatalf("expected planner to try the second candidate after the first failed, got %d request(s)", len(planner.requests))
	}
	if len(result.Clips) != 1 {
		t.Fatalf("expected one rendered clip after skipping first candidate, got %+v", result.Clips)
	}
	if result.Clips[0].Rank != 2 || result.Clips[0].Title != "Setup Payoff" {
		t.Fatalf("expected second candidate to render, got %+v", result.Clips[0])
	}
	if result.Clips[0].ObjectKey != "guild-1/request-1/01-setup-payoff.mp4" || len(uploader.requests) != 2 {
		t.Fatalf("expected rendered fallback candidate to upload as first output, clip=%+v uploads=%+v", result.Clips[0], uploader.requests)
	}
}

func TestResolveCaptionFontUsesConfiguredDefaultFont(t *testing.T) {
	path := testCaptionFontPath(t)
	service := NewService(Config{
		CaptionFontPath:   path,
		CaptionFontFamily: "Fixture Sans",
	})

	font, err := service.resolveCaptionFont(clipCaptionFontDefault)
	if err != nil {
		t.Fatalf("resolveCaptionFont: %v", err)
	}
	if font.Path != path || font.Family != "Fixture Sans" || font.Key != clipCaptionFontDefault {
		t.Fatalf("unexpected resolved font: %+v", font)
	}
}

func TestClipCompositionTranscriptTimelineUsesSelectedWordsWithPaddedBoundaries(t *testing.T) {
	timeline, err := clipCompositionTranscriptTimeline(ClipDecision{
		Segments: []ClipDecisionSegment{{
			StartWordID:  "w_selected_1",
			EndWordID:    "w_selected_2",
			StartSeconds: 9.82,
			EndSeconds:   18.28,
			Transcript:   "selected idea",
		}},
	}, []TranscriptSegment{
		{ID: "before", StartSeconds: 8, EndSeconds: 10, Text: "previous idea", Words: []TranscriptWord{
			testWord("w_before_1", 8.0, 8.4, "previous"),
			testWord("w_before_2", 9.5, 10.0, "idea"),
		}},
		{ID: "selected", StartSeconds: 10, EndSeconds: 18, Text: "selected idea", Words: []TranscriptWord{
			testWord("w_selected_1", 10.0, 10.5, "selected"),
			testWord("w_selected_2", 17.5, 18.0, "idea"),
		}},
		{ID: "after", StartSeconds: 18, EndSeconds: 20, Text: "next idea", Words: []TranscriptWord{
			testWord("w_after_1", 18.0, 18.4, "next"),
			testWord("w_after_2", 19.5, 20.0, "idea"),
		}},
	})
	if err != nil {
		t.Fatalf("clipCompositionTranscriptTimeline: %v", err)
	}

	if len(timeline) != 1 || timeline[0].ID != "selected" || timeline[0].Text != "selected idea" || len(timeline[0].Words) != 2 {
		t.Fatalf("expected padded clip boundary to keep indexed transcript segment only, got %+v", timeline)
	}
}

func TestClipRejectsInvalidDetectorTimestamps(t *testing.T) {
	err := validateClipDecision(ClipDecision{
		Rank:  1,
		Title: "Bad Clip",
		Type:  "continuous",
		Segments: []ClipDecisionSegment{{
			StartSeconds: 20,
			EndSeconds:   10,
			Transcript:   "bad",
		}},
		Reason:            "bad",
		Confidence:        0.5,
		ViralityScore:     10,
		HookScore:         10,
		RetentionScore:    10,
		ShareabilityScore: 10,
		DurationPolicy:    "other",
		ExceptionReason:   "",
	}, time.Minute, 5*time.Second, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "after clip segment start") {
		t.Fatalf("expected invalid timestamp error, got %v", err)
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

func TestValidateCaptionRendererCachesSuccessfulProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixtures are unix-only")
	}
	ffmpegPath := writeShellExecutable(t, "ffmpeg", `case " $* " in
*" -filters "*)
  count_file="$0.count"
  count=$(cat "$count_file" 2>/dev/null || printf 0)
  count=$((count + 1))
  printf '%s' "$count" > "$count_file"
  echo ' ... subtitles         V->V       Render text subtitles onto input video using the libass library.'
  ;;
*)
  echo "unexpected ffmpeg args: $*" >&2
  exit 1
  ;;
esac`)
	captionRendererSupportCache.Delete(ffmpegPath)

	for range 2 {
		if err := validateCaptionRenderer(context.Background(), ToolPaths{FFmpegPath: ffmpegPath}); err != nil {
			t.Fatalf("validateCaptionRenderer: %v", err)
		}
	}
	data, err := os.ReadFile(ffmpegPath + ".count")
	if err != nil {
		t.Fatalf("read ffmpeg count: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("expected one caption renderer probe, got %q", string(data))
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

func requireArg(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, arg := range args {
		if arg == flag {
			return
		}
	}
	t.Fatalf("expected args to include %q, got %#v", flag, args)
}

func requireArgValue(t *testing.T, args []string, flag string, value string) {
	t.Helper()
	for index, arg := range args {
		if arg == flag && index+1 < len(args) && args[index+1] == value {
			return
		}
	}
	t.Fatalf("expected args to include %q %q, got %#v", flag, value, args)
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

type fakeClipDetector struct {
	configured bool
	requests   []ClipDetectionRequest
	result     ClipDetectionResult
	err        error
}

func (f *fakeClipDetector) Configured() bool {
	return f.configured
}

func (f *fakeClipDetector) Detect(_ context.Context, request ClipDetectionRequest) (ClipDetectionResult, error) {
	f.requests = append(f.requests, request)
	return f.result, f.err
}

type fakeClipPlanner struct {
	configured bool
	requests   []ClipCompositionRequest
	results    []ClipCompositionResult
	errs       []error
	err        error
}

func (f *fakeClipPlanner) Configured() bool {
	return f.configured
}

func (f *fakeClipPlanner) Plan(_ context.Context, request ClipCompositionRequest) (ClipCompositionResult, error) {
	f.requests = append(f.requests, request)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return ClipCompositionResult{}, err
		}
	}
	if f.err != nil {
		return ClipCompositionResult{}, f.err
	}
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result, nil
	}
	aspect := strings.TrimSpace(request.RequestedAspect)
	if aspect == "" || aspect == "auto" {
		aspect = "9:16"
	}
	plans := make([]ClipFrameRenderPlan, 0, len(request.Clip.Segments))
	for index, segment := range request.Clip.Segments {
		plans = append(plans, ClipFrameRenderPlan{
			AppliesToSegmentIndex: index,
			SourceStartSeconds:    segment.StartSeconds,
			SourceEndSeconds:      segment.EndSeconds,
			Regions: []ClipRenderRegion{{
				Role:       "full_frame",
				SourceRect: ClipRect{X: 0, Y: 0, W: 1000, H: 1000},
				OutputRect: ClipRect{X: 0, Y: 0, W: 1000, H: 1000},
				Fit:        "contain",
				ZIndex:     0,
			}},
		})
	}
	return ClipCompositionResult{
		AspectRatio: aspect,
		LayoutMode:  "full_frame",
		Plans:       plans,
		CaptionPlan: fakeCaptionPlanForRequest(request),
		Confidence:  0.8,
		Reason:      "Preserve the full source frame for this test fixture.",
	}, nil
}

func fakeCaptionPlanForRequest(request ClipCompositionRequest) *ClipCaptionPlan {
	if strings.TrimSpace(request.CaptionMode) == clipCaptionRequestOff {
		return applyTestCaptionStyle(&ClipCaptionPlan{
			Mode:          clipCaptionPlanModeDisabled,
			StylePreset:   clipCaptionStyleNone,
			TimingQuality: clipCaptionTimingNone,
			Confidence:    1,
			Reason:        "Captions were disabled by request.",
		})
	}
	for _, segment := range request.TranscriptTimeline {
		if len(segment.Words) == 0 {
			continue
		}
		end := len(segment.Words) - 1
		if end > 3 {
			end = 3
		}
		return applyFakeCaptionStyleForRequest(&ClipCaptionPlan{
			Mode:          clipCaptionPlanModeBurnedIn,
			StylePreset:   clipCaptionStyleOpusBold,
			TimingQuality: clipCaptionTimingWord,
			Regions: []ClipCaptionRegion{{
				ID:              "bottom_global",
				OutputRect:      ClipRect{X: 80, Y: 760, W: 840, H: 160},
				HorizontalAlign: clipCaptionAlignCenter,
				VerticalAlign:   clipCaptionAlignMiddle,
				MaxLines:        2,
				ZIndex:          20,
			}},
			Cues: []ClipCaptionCue{{
				CaptionRegionID: "bottom_global",
				WordIDs:         []string{segment.Words[0].ID, segment.Words[end].ID},
				EmphasisWordIDs: []string{segment.Words[0].ID},
			}},
			Confidence: 0.85,
			Reason:     "Bottom captions stay clear of the main frame in this fixture.",
		}, request)
	}
	for _, segment := range request.TranscriptTimeline {
		if strings.TrimSpace(segment.ID) == "" {
			continue
		}
		return applyFakeCaptionStyleForRequest(&ClipCaptionPlan{
			Mode:          clipCaptionPlanModeBurnedIn,
			StylePreset:   clipCaptionStyleOpusBold,
			TimingQuality: clipCaptionTimingSegment,
			Regions: []ClipCaptionRegion{{
				ID:              "bottom_global",
				OutputRect:      ClipRect{X: 80, Y: 760, W: 840, H: 160},
				HorizontalAlign: clipCaptionAlignCenter,
				VerticalAlign:   clipCaptionAlignMiddle,
				MaxLines:        2,
				ZIndex:          20,
			}},
			Cues: []ClipCaptionCue{{
				CaptionRegionID:  "bottom_global",
				SourceSegmentIDs: []string{segment.ID},
			}},
			Confidence: 0.7,
			Reason:     "Segment-timed captions are explicit because no words were available in this fixture.",
		}, request)
	}
	return nil
}

func applyFakeCaptionStyleForRequest(plan *ClipCaptionPlan, request ClipCompositionRequest) *ClipCaptionPlan {
	plan = applyTestCaptionStyle(plan)
	if strings.TrimSpace(request.CaptionInstructions) == "" {
		return plan
	}
	plan.StyleSource = clipCaptionStyleSourceCreativeMix
	plan.FontColor = "cyan"
	plan.HighlightColor = "orange"
	plan.BorderColor = "purple"
	plan.BackgroundColor = "transparent"
	plan.BackgroundOpacity = 0
	return plan
}

func testCaptionFontPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caption.ttf")
	if err := os.WriteFile(path, []byte("fake font bytes"), 0o600); err != nil {
		t.Fatalf("write test caption font: %v", err)
	}
	return path
}

type fakeClipUploader struct {
	configured bool
	requests   []objectstore.UploadRequest
	url        string
	baseURL    string
	err        error
}

func (f *fakeClipUploader) Configured() bool {
	return f.configured
}

func (f *fakeClipUploader) Upload(_ context.Context, request objectstore.UploadRequest) (objectstore.UploadResult, error) {
	f.requests = append(f.requests, request)
	url := f.url
	if url == "" {
		url = strings.TrimRight(f.baseURL, "/") + "/" + strings.TrimLeft(request.Key, "/")
	}
	return objectstore.UploadResult{
		Key:       request.Key,
		URL:       url,
		SizeBytes: int64(len(request.Body)),
	}, f.err
}
