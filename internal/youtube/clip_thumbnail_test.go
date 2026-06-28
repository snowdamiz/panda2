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
