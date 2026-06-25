package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/sn0w/panda2/internal/generated"
)

const maxGIFFrameBytes = 8 * 1024 * 1024

var errGIFFrameTooLarge = errors.New("extracted GIF frame is too large")

type GIFFrameExtractor interface {
	ExtractGIFFrame(ctx context.Context, reference generated.ImageReference) (generated.File, error)
}

type FFmpegProvider interface {
	EnsureFFmpeg(ctx context.Context) (string, error)
}

type FFmpegGIFFrameExtractor struct {
	ffmpeg   FFmpegProvider
	maxBytes int64
}

func NewFFmpegGIFFrameExtractor(ffmpeg FFmpegProvider) *FFmpegGIFFrameExtractor {
	return &FFmpegGIFFrameExtractor{
		ffmpeg:   ffmpeg,
		maxBytes: maxGIFFrameBytes,
	}
}

func (e *FFmpegGIFFrameExtractor) ExtractGIFFrame(ctx context.Context, reference generated.ImageReference) (generated.File, error) {
	if e == nil || e.ffmpeg == nil {
		return generated.File{}, errors.New("ffmpeg is not configured")
	}
	rawURL := strings.TrimSpace(reference.URL)
	if rawURL == "" {
		return generated.File{}, errors.New("media reference URL is empty")
	}
	ffmpegPath, err := e.ffmpeg.EnsureFFmpeg(ctx)
	if err != nil {
		return generated.File{}, fmt.Errorf("ensure ffmpeg: %w", err)
	}
	limit := e.maxBytes
	if limit <= 0 {
		limit = maxGIFFrameBytes
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, gifFrameFFmpegArgs(rawURL)...)
	var stderr limitedTextBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return generated.File{}, fmt.Errorf("open ffmpeg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return generated.File{}, fmt.Errorf("start ffmpeg: %w", err)
	}
	data, readErr := readProcessOutputAtMost(stdout, limit)
	if readErr != nil {
		if errors.Is(readErr, errGIFFrameTooLarge) && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		if waitErr != nil && !errors.Is(readErr, errGIFFrameTooLarge) {
			return generated.File{}, fmt.Errorf("read ffmpeg output: %w; ffmpeg failed: %v", readErr, waitErr)
		}
		return generated.File{}, readErr
	}
	if err := cmd.Wait(); err != nil {
		return generated.File{}, fmt.Errorf("ffmpeg frame extraction failed: %w", err)
	}
	if len(data) == 0 {
		return generated.File{}, errors.New("ffmpeg produced an empty frame")
	}
	return generated.File{
		Filename: gifFrameFilename(reference),
		MIMEType: "image/png",
		Data:     data,
		AltText:  "Extracted media frame",
	}, nil
}

func gifFrameFFmpegArgs(input string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_on_network_error", "1",
		"-reconnect_delay_max", "5",
		"-i", input,
		"-map", "0:v:0",
		"-frames:v", "1",
		"-an",
		"-sn",
		"-dn",
		"-f", "image2pipe",
		"-c:v", "png",
		"pipe:1",
	}
}

func readProcessOutputAtMost(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = maxGIFFrameBytes
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errGIFFrameTooLarge
	}
	return data, nil
}

func imageDataURL(mimeType string, data []byte) string {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func gifFrameFilename(reference generated.ImageReference) string {
	filename := strings.TrimSpace(reference.Filename)
	if filename == "" {
		filename = strings.TrimSpace(reference.ID)
	}
	filename = strings.Trim(filename, ".")
	if filename == "" {
		return "gif-frame.png"
	}
	lower := strings.ToLower(filename)
	for _, extension := range []string{".gif", ".gifv", ".mp4", ".m4v", ".webm", ".mov"} {
		if strings.HasSuffix(lower, extension) {
			filename = strings.TrimSuffix(filename, filename[len(filename)-len(extension):])
			break
		}
	}
	filename = strings.Trim(filename, ".")
	if filename == "" {
		return "gif-frame.png"
	}
	return filename + "-frame.png"
}

type limitedTextBuffer struct {
	builder strings.Builder
	limit   int
}

func (b *limitedTextBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		b.limit = 4096
	}
	remaining := b.limit - b.builder.Len()
	if remaining > 0 {
		text := string(data)
		if len(text) > remaining {
			text = text[:remaining]
		}
		b.builder.WriteString(text)
	}
	return len(data), nil
}

func (b *limitedTextBuffer) String() string {
	return strings.TrimSpace(b.builder.String())
}
