package youtube

import "testing"

func TestClipThumbnailSamplesIncludeSpeakerBoundaryFrames(t *testing.T) {
	decision := ClipDecision{
		Segments: []ClipDecisionSegment{{
			StartSeconds: 10,
			EndSeconds:   42,
			Transcript:   "Left speaker makes a point. Right speaker responds. Left speaker reacts.",
		}},
	}
	timeline := []ClipCompositionTranscriptSegment{
		{ClipSegmentIndex: 0, StartSeconds: 10, EndSeconds: 18, Text: "Left speaker makes a point."},
		{ClipSegmentIndex: 0, StartSeconds: 18, EndSeconds: 30, Text: "Right speaker responds."},
		{ClipSegmentIndex: 0, StartSeconds: 30, EndSeconds: 42, Text: "Left speaker reacts."},
	}

	samples := clipThumbnailSamples(decision, timeline, 12)

	before := findThumbnailSample(samples, "possible_speaker_switch_before", 17.85)
	after := findThumbnailSample(samples, "possible_speaker_switch_after", 18.15)
	if before == nil || after == nil {
		t.Fatalf("expected before/after speaker boundary samples near 18s, got %+v", samples)
	}
	if !hasThumbnailSampleReason(samples, "strategic_start") || !hasThumbnailSampleReason(samples, "strategic_midpoint") || !hasThumbnailSampleReason(samples, "strategic_end") {
		t.Fatalf("expected strategic samples to remain alongside speaker boundary samples, got %+v", samples)
	}
}

func TestClipThumbnailSamplesPrioritizeSpeakerBoundaryPairsUnderCap(t *testing.T) {
	decision := ClipDecision{Segments: make([]ClipDecisionSegment, 0, 6)}
	timeline := make([]ClipCompositionTranscriptSegment, 0, 12)
	for index := 0; index < 6; index++ {
		start := 10 + float64(index*10)
		end := start + 10
		decision.Segments = append(decision.Segments, ClipDecisionSegment{
			StartSeconds: start,
			EndSeconds:   end,
			Transcript:   "Speaker A talks. Speaker B responds.",
		})
		timeline = append(timeline,
			ClipCompositionTranscriptSegment{ClipSegmentIndex: index, StartSeconds: start, EndSeconds: start + 5, Text: "Speaker A talks."},
			ClipCompositionTranscriptSegment{ClipSegmentIndex: index, StartSeconds: start + 5, EndSeconds: end, Text: "Speaker B responds."},
		)
	}

	samples := clipThumbnailSamples(decision, timeline, 12)

	if len(samples) != 12 {
		t.Fatalf("expected thumbnail sampling to respect cap, got %d samples: %+v", len(samples), samples)
	}
	before := findThumbnailSample(samples, "possible_speaker_switch_before", 14.85)
	after := findThumbnailSample(samples, "possible_speaker_switch_after", 15.15)
	if before == nil || after == nil {
		t.Fatalf("expected first speaker switch confirmation pair to survive under cap, got %+v", samples)
	}
	if count := countThumbnailSampleReasonPrefix(samples, "possible_speaker_switch"); count < 8 {
		t.Fatalf("expected at least four speaker switch confirmation pairs under cap, got %d switch samples in %+v", count, samples)
	}
	if !hasThumbnailSampleReason(samples, "strategic_start") {
		t.Fatalf("expected strategic context samples to remain, got %+v", samples)
	}
}

func findThumbnailSample(samples []clipThumbnailSample, reason string, sourceSeconds float64) *clipThumbnailSample {
	for index := range samples {
		if samples[index].Reason == reason && secondsNear(samples[index].SourceSeconds, sourceSeconds, 0.02) {
			return &samples[index]
		}
	}
	return nil
}

func hasThumbnailSampleReason(samples []clipThumbnailSample, reason string) bool {
	for _, sample := range samples {
		if sample.Reason == reason {
			return true
		}
	}
	return false
}

func countThumbnailSampleReasonPrefix(samples []clipThumbnailSample, prefix string) int {
	count := 0
	for _, sample := range samples {
		if len(sample.Reason) >= len(prefix) && sample.Reason[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}
