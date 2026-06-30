package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultSidecarDownloadTimeout = 3 * time.Minute
	defaultYTDLPSidecarMaxAge     = 24 * time.Hour
	maxSidecarDownloadBytes       = 220 << 20
	ffmpegStaticReleaseTag        = "b6.1.1"
)

type ToolPaths struct {
	YTDLPPath  string
	FFmpegPath string
}

type SidecarConfig struct {
	Dir        string
	YTDLPPath  string
	FFmpegPath string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

type SidecarManager struct {
	dir        string
	ytdlpPath  string
	ffmpegPath string
	client     *http.Client
	logger     *slog.Logger

	mu    sync.Mutex
	tools ToolPaths
}

func NewSidecarManager(config SidecarConfig) *SidecarManager {
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultSidecarDownloadTimeout}
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &SidecarManager{
		dir:        strings.TrimSpace(config.Dir),
		ytdlpPath:  strings.TrimSpace(config.YTDLPPath),
		ffmpegPath: strings.TrimSpace(config.FFmpegPath),
		client:     client,
		logger:     logger,
	}
}

func (m *SidecarManager) Ensure(ctx context.Context) (ToolPaths, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.toolsAvailable(time.Now()) {
		return m.tools, nil
	}
	dir := strings.TrimSpace(m.dir)
	if dir == "" {
		return ToolPaths{}, errors.New("music sidecar dir is not configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ToolPaths{}, fmt.Errorf("create music sidecar dir: %w", err)
	}
	ytdlpPath, err := m.ensureTool(ctx, toolSpecYTDLP(), m.ytdlpPath)
	if err != nil {
		return ToolPaths{}, err
	}
	ffmpegPath, err := m.ensureTool(ctx, toolSpecFFmpeg(), m.ffmpegPath)
	if err != nil {
		return ToolPaths{}, err
	}
	m.tools = ToolPaths{YTDLPPath: ytdlpPath, FFmpegPath: ffmpegPath}
	return m.tools, nil
}

func (m *SidecarManager) EnsureFFmpeg(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if path, ok := executablePath(m.tools.FFmpegPath); ok {
		return path, nil
	}
	if path, ok := executablePath(m.ffmpegPath); ok {
		m.tools.FFmpegPath = path
		return path, nil
	}
	dir := strings.TrimSpace(m.dir)
	if dir == "" {
		return "", errors.New("music sidecar dir is not configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create music sidecar dir: %w", err)
	}
	ffmpegPath, err := m.ensureTool(ctx, toolSpecFFmpeg(), m.ffmpegPath)
	if err != nil {
		return "", err
	}
	m.tools.FFmpegPath = ffmpegPath
	return ffmpegPath, nil
}

func (m *SidecarManager) ensureTool(ctx context.Context, spec sidecarToolSpec, configuredPath string) (string, error) {
	if path, ok := executablePath(configuredPath); ok {
		return path, nil
	}
	target := filepath.Join(m.dir, sidecarExecutableName(spec.name))
	if path, ok := executablePath(target); ok {
		if !sidecarToolNeedsRefresh(path, spec.refreshAfter, time.Now()) {
			return path, nil
		}
		m.logger.Info("refreshing music sidecar", slog.String("tool", spec.name), slog.String("target", target))
	} else {
		m.logger.Info("provisioning music sidecar", slog.String("tool", spec.name), slog.String("target", target))
	}
	assetURL, err := spec.assetURL()
	if err != nil {
		return "", err
	}
	if err := m.downloadExecutable(ctx, assetURL, target); err != nil {
		return "", fmt.Errorf("provision %s sidecar: %w", spec.name, err)
	}
	return target, nil
}

func (m *SidecarManager) downloadExecutable(ctx context.Context, assetURL string, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	temp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		_ = os.Remove(tempName)
	}()
	limited := io.LimitReader(resp.Body, maxSidecarDownloadBytes+1)
	written, copyErr := io.Copy(temp, limited)
	closeErr := temp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxSidecarDownloadBytes {
		return fmt.Errorf("download exceeds %d bytes", maxSidecarDownloadBytes)
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		return err
	}
	return os.Rename(tempName, target)
}

type sidecarToolSpec struct {
	name         string
	refreshAfter time.Duration
	assetURL     func() (string, error)
}

func toolSpecYTDLP() sidecarToolSpec {
	return sidecarToolSpec{name: "yt-dlp", refreshAfter: defaultYTDLPSidecarMaxAge, assetURL: ytdlpSidecarURL}
}

func toolSpecFFmpeg() sidecarToolSpec {
	return sidecarToolSpec{name: "ffmpeg", assetURL: ffmpegSidecarURL}
}

func ytdlpSidecarURL() (string, error) {
	asset := ""
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			asset = "yt-dlp_linux"
		case "arm64":
			asset = "yt-dlp_linux_aarch64"
		}
	case "darwin":
		asset = "yt-dlp_macos"
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			asset = "yt-dlp.exe"
		case "arm64":
			asset = "yt-dlp_arm64.exe"
		case "386":
			asset = "yt-dlp_x86.exe"
		}
	}
	if asset == "" {
		return "", unsupportedSidecarError("yt-dlp")
	}
	return "https://github.com/yt-dlp/yt-dlp/releases/latest/download/" + asset, nil
}

func ffmpegSidecarURL() (string, error) {
	asset := ""
	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			asset = "ffmpeg-linux-x64"
		case "arm64":
			asset = "ffmpeg-linux-arm64"
		case "arm":
			asset = "ffmpeg-linux-arm"
		case "386":
			asset = "ffmpeg-linux-ia32"
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			asset = "ffmpeg-darwin-x64"
		case "arm64":
			asset = "ffmpeg-darwin-arm64"
		}
	case "windows":
		if runtime.GOARCH == "amd64" {
			asset = "ffmpeg-win32-x64"
		}
	}
	if asset == "" {
		return "", unsupportedSidecarError("ffmpeg")
	}
	return fmt.Sprintf("https://github.com/eugeneware/ffmpeg-static/releases/download/%s/%s", ffmpegStaticReleaseTag, asset), nil
}

func unsupportedSidecarError(tool string) error {
	return fmt.Errorf("%s sidecar is not available for %s/%s", tool, runtime.GOOS, runtime.GOARCH)
}

func sidecarExecutableName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		return name + ".exe"
	}
	return name
}

func executablePath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", false
	}
	return resolved, true
}

func (m *SidecarManager) toolsAvailable(now time.Time) bool {
	if !toolsAvailable(m.tools) {
		return false
	}
	return !m.managedToolNeedsRefresh(toolSpecYTDLP(), m.tools.YTDLPPath, now)
}

func (m *SidecarManager) managedToolNeedsRefresh(spec sidecarToolSpec, path string, now time.Time) bool {
	if spec.refreshAfter <= 0 {
		return false
	}
	target := filepath.Join(strings.TrimSpace(m.dir), sidecarExecutableName(spec.name))
	if strings.TrimSpace(path) != target {
		return false
	}
	return sidecarToolNeedsRefresh(path, spec.refreshAfter, now)
}

func sidecarToolNeedsRefresh(path string, refreshAfter time.Duration, now time.Time) bool {
	if refreshAfter <= 0 {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return !info.ModTime().After(now.Add(-refreshAfter))
}

func toolsAvailable(paths ToolPaths) bool {
	_, ytdlpOK := executablePath(paths.YTDLPPath)
	_, ffmpegOK := executablePath(paths.FFmpegPath)
	return ytdlpOK && ffmpegOK
}
