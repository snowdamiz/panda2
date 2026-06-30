package youtube

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type clipThumbnailSample struct {
	SegmentIndex      int
	SourceSeconds     float64
	ClipOffsetSeconds float64
	Reason            string
	Transcript        string
}

type clipThumbnailSwitchPair struct {
	Before          clipThumbnailSample
	After           clipThumbnailSample
	BoundarySeconds float64
}

func (s *Service) extractClipThumbnails(ctx context.Context, tools ToolPaths, sourcePath string, decision ClipDecision, transcriptTimeline []ClipCompositionTranscriptSegment, tempDir string) ([]ClipThumbnail, error) {
	samples := clipThumbnailSamples(decision, transcriptTimeline, s.thumbnailMaxCount)
	if len(samples) == 0 {
		return nil, fmt.Errorf("clip thumbnail sampling produced no sample times")
	}
	thumbDir := filepath.Join(tempDir, "thumbnails")
	if err := os.MkdirAll(thumbDir, 0o700); err != nil {
		return nil, fmt.Errorf("create clip thumbnail dir: %w", err)
	}
	thumbnails := make([]ClipThumbnail, 0, len(samples))
	for index, sample := range samples {
		id := fmt.Sprintf("thumb_%02d", index+1)
		path := filepath.Join(thumbDir, id+".jpg")
		if err := s.extractClipThumbnail(ctx, tools, sourcePath, sample.SourceSeconds, path); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read clip thumbnail: %w", err)
		}
		width, height, err := imageDimensions(data)
		if err != nil {
			return nil, err
		}
		thumbnails = append(thumbnails, ClipThumbnail{
			ID:                  id,
			SourceSeconds:       sample.SourceSeconds,
			ClipSegmentIndex:    sample.SegmentIndex,
			ClipOffsetSeconds:   sample.ClipOffsetSeconds,
			SampleReason:        sample.Reason,
			Width:               width,
			Height:              height,
			MIMEType:            "image/jpeg",
			Data:                data,
			TranscriptNearFrame: strings.TrimSpace(sample.Transcript),
		})
	}
	return thumbnails, nil
}

func clipThumbnailSamples(decision ClipDecision, transcriptTimeline []ClipCompositionTranscriptSegment, maxCount int) []clipThumbnailSample {
	if maxCount <= 0 {
		maxCount = 12
	}
	strategic := clipStrategicThumbnailSamples(decision)
	switchPairs := clipThumbnailSwitchPairs(decision, transcriptTimeline)
	samples := selectClipThumbnailSamples(strategic, switchPairs, len(decision.Segments), maxCount)
	sortClipThumbnailSamples(samples)
	return samples
}

func clipStrategicThumbnailSamples(decision ClipDecision) []clipThumbnailSample {
	samples := make([]clipThumbnailSample, 0)
	for segmentIndex, segment := range decision.Segments {
		duration := segment.EndSeconds - segment.StartSeconds
		if duration <= 0 {
			continue
		}
		type strategicOffset struct {
			offset float64
			reason string
		}
		offsets := []strategicOffset{
			{offset: 1, reason: "strategic_start"},
			{offset: duration / 2, reason: "strategic_midpoint"},
		}
		if len(decision.Segments) == 1 {
			offsets = []strategicOffset{
				{offset: 1, reason: "strategic_start"},
				{offset: duration / 2, reason: "strategic_midpoint"},
				{offset: duration - 1, reason: "strategic_end"},
			}
			if duration > 30 {
				offsets = []strategicOffset{
					{offset: 1, reason: "strategic_start"},
					{offset: duration / 4, reason: "strategic_first_quarter"},
					{offset: duration / 2, reason: "strategic_midpoint"},
					{offset: duration * 3 / 4, reason: "strategic_third_quarter"},
					{offset: duration - 1, reason: "strategic_end"},
				}
			}
		}
		for _, offset := range offsets {
			if sample, ok := newClipThumbnailSample(decision, segmentIndex, segment.StartSeconds+offset.offset, offset.reason, segment.Transcript); ok {
				samples = append(samples, sample)
			}
		}
	}
	return samples
}

func clipThumbnailSwitchPairs(decision ClipDecision, transcriptTimeline []ClipCompositionTranscriptSegment) []clipThumbnailSwitchPair {
	pairs := make([]clipThumbnailSwitchPair, 0)
	for segmentIndex, segment := range decision.Segments {
		turns := transcriptTimelineForClipSegment(transcriptTimeline, segmentIndex)
		for index := 1; index < len(turns); index++ {
			previous := turns[index-1]
			next := turns[index]
			boundary := next.StartSeconds
			if boundary <= segment.StartSeconds+0.1 || boundary >= segment.EndSeconds-0.1 {
				continue
			}
			nearText := strings.TrimSpace(previous.Text)
			if nearText != "" && strings.TrimSpace(next.Text) != "" {
				nearText += " / " + strings.TrimSpace(next.Text)
			} else if strings.TrimSpace(next.Text) != "" {
				nearText = strings.TrimSpace(next.Text)
			}
			before, beforeOK := newClipThumbnailSample(decision, segmentIndex, boundary-0.15, "possible_speaker_switch_before", nearText)
			after, afterOK := newClipThumbnailSample(decision, segmentIndex, boundary+0.15, "possible_speaker_switch_after", nearText)
			if beforeOK && afterOK {
				pairs = append(pairs, clipThumbnailSwitchPair{
					Before:          before,
					After:           after,
					BoundarySeconds: boundary,
				})
			}
		}
	}
	return pairs
}

func newClipThumbnailSample(decision ClipDecision, segmentIndex int, sourceSeconds float64, reason string, transcript string) (clipThumbnailSample, bool) {
	if segmentIndex < 0 || segmentIndex >= len(decision.Segments) {
		return clipThumbnailSample{}, false
	}
	segment := decision.Segments[segmentIndex]
	sourceSeconds = clampThumbnailTime(sourceSeconds, segment.StartSeconds, segment.EndSeconds)
	sample := clipThumbnailSample{
		SegmentIndex:      segmentIndex,
		SourceSeconds:     sourceSeconds,
		ClipOffsetSeconds: sourceSeconds - segment.StartSeconds,
		Reason:            reason,
		Transcript:        strings.TrimSpace(transcript),
	}
	if sample.Transcript == "" {
		sample.Transcript = segment.Transcript
	}
	return sample, true
}

func selectClipThumbnailSamples(strategic []clipThumbnailSample, switchPairs []clipThumbnailSwitchPair, segmentCount int, maxCount int) []clipThumbnailSample {
	if maxCount <= 0 {
		return nil
	}
	selected := make([]clipThumbnailSample, 0, maxCount)
	pairCount := 0
	if len(switchPairs) > 0 && maxCount >= 2 {
		pairBudget := maxCount - clipThumbnailStrategicReserve(segmentCount, maxCount)
		if pairBudget < 2 {
			pairBudget = 2
		}
		pairCount = min(len(switchPairs), pairBudget/2)
		for _, pair := range selectClipThumbnailSwitchPairs(switchPairs, pairCount) {
			addClipThumbnailSample(&selected, pair.Before, maxCount)
			addClipThumbnailSample(&selected, pair.After, maxCount)
		}
	}
	for _, sample := range strategic {
		addClipThumbnailSample(&selected, sample, maxCount)
	}
	if len(selected) < maxCount && pairCount < len(switchPairs) {
		for _, pair := range switchPairs {
			if len(selected) >= maxCount {
				break
			}
			if !clipThumbnailSampleSelected(selected, pair.Before) && len(selected)+2 <= maxCount {
				addClipThumbnailSample(&selected, pair.Before, maxCount)
				addClipThumbnailSample(&selected, pair.After, maxCount)
			}
		}
	}
	return selected
}

func clipThumbnailStrategicReserve(segmentCount int, maxCount int) int {
	if maxCount <= 2 {
		return 0
	}
	reserve := 3
	if segmentCount > 1 {
		reserve = min(segmentCount, 4)
	}
	if maxCount < 8 {
		reserve = max(1, maxCount/3)
	}
	return min(reserve, maxCount-2)
}

func selectClipThumbnailSwitchPairs(pairs []clipThumbnailSwitchPair, maxPairs int) []clipThumbnailSwitchPair {
	if maxPairs <= 0 {
		return nil
	}
	if len(pairs) <= maxPairs {
		return append([]clipThumbnailSwitchPair(nil), pairs...)
	}
	if maxPairs == 1 {
		return []clipThumbnailSwitchPair{pairs[0]}
	}
	selected := make([]clipThumbnailSwitchPair, 0, maxPairs)
	used := make(map[int]bool, maxPairs)
	for index := 0; index < maxPairs; index++ {
		pairIndex := index * (len(pairs) - 1) / (maxPairs - 1)
		for used[pairIndex] && pairIndex < len(pairs)-1 {
			pairIndex++
		}
		for used[pairIndex] && pairIndex > 0 {
			pairIndex--
		}
		used[pairIndex] = true
		selected = append(selected, pairs[pairIndex])
	}
	return selected
}

func addClipThumbnailSample(samples *[]clipThumbnailSample, sample clipThumbnailSample, maxCount int) bool {
	for index, existing := range *samples {
		if existing.SegmentIndex == sample.SegmentIndex && secondsNear(existing.SourceSeconds, sample.SourceSeconds, 0.25) {
			if clipThumbnailSamplePriority(sample.Reason) > clipThumbnailSamplePriority(existing.Reason) {
				(*samples)[index] = sample
			}
			return true
		}
	}
	if len(*samples) >= maxCount {
		return false
	}
	*samples = append(*samples, sample)
	return true
}

func clipThumbnailSampleSelected(samples []clipThumbnailSample, sample clipThumbnailSample) bool {
	for _, existing := range samples {
		if existing.SegmentIndex == sample.SegmentIndex && secondsNear(existing.SourceSeconds, sample.SourceSeconds, 0.25) {
			return true
		}
	}
	return false
}

func clipThumbnailSamplePriority(reason string) int {
	if strings.HasPrefix(reason, "possible_speaker_switch") {
		return 3
	}
	switch reason {
	case "strategic_start", "strategic_end":
		return 2
	default:
		return 1
	}
}

func transcriptTimelineForClipSegment(timeline []ClipCompositionTranscriptSegment, segmentIndex int) []ClipCompositionTranscriptSegment {
	filtered := make([]ClipCompositionTranscriptSegment, 0)
	for _, segment := range timeline {
		if segment.ClipSegmentIndex == segmentIndex {
			filtered = append(filtered, segment)
		}
	}
	return filtered
}

func sortClipThumbnailSamples(samples []clipThumbnailSample) {
	for i := 1; i < len(samples); i++ {
		current := samples[i]
		j := i - 1
		for j >= 0 && (samples[j].SegmentIndex > current.SegmentIndex || (samples[j].SegmentIndex == current.SegmentIndex && samples[j].SourceSeconds > current.SourceSeconds)) {
			samples[j+1] = samples[j]
			j--
		}
		samples[j+1] = current
	}
}

func secondsNear(a float64, b float64, tolerance float64) bool {
	delta := a - b
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

func clampThumbnailTime(value float64, start float64, end float64) float64 {
	if end <= start {
		return start
	}
	minimum := start + 0.05
	maximum := end - 0.05
	if maximum < minimum {
		return start + (end-start)/2
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func (s *Service) extractClipThumbnail(ctx context.Context, tools ToolPaths, sourcePath string, sourceSeconds float64, outputPath string) error {
	processCtx, cancel := context.WithTimeout(ctx, s.processTimeout)
	defer cancel()
	ffmpegCmd := exec.CommandContext(processCtx, tools.FFmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-ss", formatClipSeconds(sourceSeconds),
		"-i", sourcePath,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", s.thumbnailMaxEdge, s.thumbnailMaxEdge),
		"-q:v", "4",
		outputPath,
	)
	var ffmpegErr limitedBuffer
	ffmpegCmd.Stderr = &ffmpegErr
	if err := ffmpegCmd.Run(); err != nil {
		if processCtx.Err() != nil {
			return fmt.Errorf("youtube clip thumbnail extraction failed: %w", processCtx.Err())
		}
		return fmt.Errorf("youtube clip thumbnail extraction failed: ffmpeg: %v %s", err, strings.TrimSpace(ffmpegErr.String()))
	}
	return nil
}

func imageDimensions(data []byte) (int, int, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("decode clip thumbnail dimensions: %w", err)
	}
	return config.Width, config.Height, nil
}
