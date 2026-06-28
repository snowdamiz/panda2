package youtube

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maxClipCaptionRegions  = 4
	maxCaptionWordsPerLine = 4
	captionStyleScaleX     = 92

	clipCaptionFontDefault             = "default"
	clipCaptionFontInter               = "inter"
	clipCaptionFontArial               = "arial"
	clipCaptionFontArialNarrow         = "arial_narrow"
	clipCaptionFontDejaVuSansCondensed = "dejavu_sans_condensed"

	clipCaptionBorderNone       = "none"
	clipCaptionBorderThin       = "thin"
	clipCaptionBorderMedium     = "medium"
	clipCaptionBorderThick      = "thick"
	clipCaptionBorderExtraThick = "extra_thick"

	clipCaptionStyleSourceNone          = "none"
	clipCaptionStyleSourceOpusDefault   = "opus_default"
	clipCaptionStyleSourceUserSpecified = "user_specified"
	clipCaptionStyleSourceCreativeMix   = "creative_mix"
	clipCaptionStyleSourceClipPalette   = "clip_palette"

	opusDefaultCaptionFontFamily        = clipCaptionFontDefault
	opusDefaultCaptionFontColor         = "white"
	opusDefaultCaptionHighlightColor    = "yellow"
	opusDefaultCaptionBorderColor       = "black"
	opusDefaultCaptionBorderThickness   = clipCaptionBorderThick
	opusDefaultCaptionBackgroundColor   = "transparent"
	opusDefaultCaptionBackgroundOpacity = 0

	linuxDefaultCaptionFontPath    = "/usr/share/fonts/truetype/dejavu/DejaVuSansCondensed-Bold.ttf"
	linuxDefaultCaptionFontFamily  = "DejaVu Sans Condensed"
	darwinDefaultCaptionFontPath   = "/System/Library/Fonts/Supplemental/Arial Bold.ttf"
	darwinDefaultCaptionFontFamily = "Arial"
)

type clipCaptionResolvedFont struct {
	Key    string
	Family string
	Path   string
}

type clipCaptionFontSpec struct {
	Key    string
	Family string
	Paths  []string
}

type clipCaptionASSStyle struct {
	Font            clipCaptionResolvedFont
	PrimaryColor    string
	HighlightColor  string
	BorderColor     string
	BackColor       string
	BorderStyle     int
	BorderThickness int
	ShadowSize      int
	Uppercase       bool
}

type clipCaptionReferences struct {
	segmentsByID  map[string]ClipCompositionTranscriptSegment
	wordsByID     map[string]TranscriptWord
	wordOrder     []TranscriptWord
	wordIndexByID map[string]int
}

func normalizeClipCaptionMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return clipCaptionRequestAuto, nil
	}
	switch value {
	case clipCaptionRequestAuto, clipCaptionRequestOn, clipCaptionRequestOff:
		return value, nil
	default:
		return "", fmt.Errorf("captions must be auto, on, or off")
	}
}

func normalizedCaptionModeForPrompt(value string) string {
	mode, err := normalizeClipCaptionMode(value)
	if err != nil {
		return clipCaptionRequestAuto
	}
	return mode
}

func newClipCaptionReferences(timeline []ClipCompositionTranscriptSegment) clipCaptionReferences {
	refs := clipCaptionReferences{
		segmentsByID:  map[string]ClipCompositionTranscriptSegment{},
		wordsByID:     map[string]TranscriptWord{},
		wordIndexByID: map[string]int{},
	}
	for _, segment := range timeline {
		id := strings.TrimSpace(segment.ID)
		if id != "" {
			refs.segmentsByID[id] = segment
		}
		for _, word := range segment.Words {
			wordID := strings.TrimSpace(word.ID)
			if wordID == "" {
				continue
			}
			if _, exists := refs.wordsByID[wordID]; exists {
				continue
			}
			refs.wordIndexByID[wordID] = len(refs.wordOrder)
			refs.wordOrder = append(refs.wordOrder, word)
			refs.wordsByID[wordID] = word
		}
	}
	sort.SliceStable(refs.wordOrder, func(i, j int) bool {
		return refs.wordOrder[i].StartSeconds < refs.wordOrder[j].StartSeconds
	})
	for index, word := range refs.wordOrder {
		refs.wordIndexByID[word.ID] = index
	}
	return refs
}

func validateClipCaptionPlan(result ClipCompositionResult, request ClipCompositionRequest) error {
	if result.CaptionPlan == nil {
		return fmt.Errorf("caption_plan is required")
	}
	mode, err := normalizeClipCaptionMode(request.CaptionMode)
	if err != nil {
		return err
	}
	plan := result.CaptionPlan
	if invalidCaptionConfidence(plan.Confidence) {
		return fmt.Errorf("caption_plan confidence must be between 0 and 1")
	}
	if strings.TrimSpace(plan.Reason) == "" {
		return fmt.Errorf("caption_plan reason is required")
	}
	if err := validateClipCaptionStyle(*plan, request); err != nil {
		return err
	}
	if mode == clipCaptionRequestOff {
		return validateDisabledCaptionPlan(*plan)
	}
	if strings.TrimSpace(plan.Mode) != clipCaptionPlanModeBurnedIn {
		return fmt.Errorf("caption_plan mode must be burned_in when captions are auto or on")
	}
	if strings.TrimSpace(plan.StylePreset) != clipCaptionStyleOpusBold {
		return fmt.Errorf("caption_plan style_preset must be opus_bold for burned-in captions")
	}
	if strings.TrimSpace(request.CaptionInstructions) == "" && strings.TrimSpace(plan.StyleSource) != clipCaptionStyleSourceOpusDefault {
		return fmt.Errorf("caption_plan style_source must be opus_default when no caption style instructions are provided")
	}
	switch strings.TrimSpace(plan.TimingQuality) {
	case clipCaptionTimingWord, clipCaptionTimingSegment:
	default:
		return fmt.Errorf("caption_plan timing_quality must be word or segment for burned-in captions")
	}
	refs := newClipCaptionReferences(request.TranscriptTimeline)
	if len(refs.segmentsByID) == 0 {
		return fmt.Errorf("caption_plan requires source transcript segment references")
	}
	if plan.TimingQuality == clipCaptionTimingWord && len(refs.wordsByID) == 0 {
		return fmt.Errorf("caption_plan timing_quality word requires transcript word IDs")
	}
	regions, err := validateClipCaptionRegions(*plan, maxVideoRegionZIndex(result.Plans))
	if err != nil {
		return err
	}
	if len(plan.Cues) == 0 {
		return fmt.Errorf("caption_plan requires at least one cue")
	}
	return validateClipCaptionCues(*plan, request, refs, regions, result.Plans)
}

func validateDisabledCaptionPlan(plan ClipCaptionPlan) error {
	if strings.TrimSpace(plan.Mode) != clipCaptionPlanModeDisabled {
		return fmt.Errorf("caption_plan mode must be disabled when captions are off")
	}
	if strings.TrimSpace(plan.StylePreset) != clipCaptionStyleNone {
		return fmt.Errorf("disabled caption_plan style_preset must be none")
	}
	if strings.TrimSpace(plan.TimingQuality) != clipCaptionTimingNone {
		return fmt.Errorf("disabled caption_plan timing_quality must be none")
	}
	if len(plan.Regions) != 0 || len(plan.Cues) != 0 {
		return fmt.Errorf("disabled caption_plan must not include regions or cues")
	}
	return nil
}

func validateClipCaptionStyle(plan ClipCaptionPlan, request ClipCompositionRequest) error {
	styleSource, err := normalizeCaptionStyleSource(plan.StyleSource)
	if err != nil {
		return err
	}
	if _, err := normalizeCaptionFontKey(plan.FontFamily); err != nil {
		return err
	}
	if _, err := captionASSOpaqueColor(plan.FontColor); err != nil {
		return fmt.Errorf("caption_plan font_color %w", err)
	}
	if _, err := captionASSOpaqueColor(plan.HighlightColor); err != nil {
		return fmt.Errorf("caption_plan highlight_color %w", err)
	}
	if _, err := captionASSOpaqueColor(plan.BorderColor); err != nil {
		return fmt.Errorf("caption_plan border_color %w", err)
	}
	if _, ok := normalizeCaptionBorderThickness(plan.BorderThickness); !ok {
		return fmt.Errorf("caption_plan border_thickness must be one of none, thin, medium, thick, or extra_thick")
	}
	if invalidCaptionOpacity(plan.BackgroundOpacity) {
		return fmt.Errorf("caption_plan background_opacity must be between 0 and 1")
	}
	if captionBackgroundColorSpecified(plan.BackgroundColor) && plan.BackgroundOpacity <= 0 {
		return fmt.Errorf("caption_plan background_opacity must be greater than 0 when background_color is set")
	}
	if !captionBackgroundColorSpecified(plan.BackgroundColor) && plan.BackgroundOpacity > 0 {
		return fmt.Errorf("caption_plan background_color must be a color when background_opacity is greater than 0")
	}
	if _, err := captionASSBackgroundColor(plan.BackgroundColor, plan.BackgroundOpacity); err != nil {
		return fmt.Errorf("caption_plan background_color %w", err)
	}
	switch strings.TrimSpace(plan.Mode) {
	case clipCaptionPlanModeDisabled:
		if styleSource != clipCaptionStyleSourceNone {
			return fmt.Errorf("disabled caption_plan style_source must be none")
		}
	case clipCaptionPlanModeBurnedIn:
		if styleSource == clipCaptionStyleSourceNone {
			return fmt.Errorf("burned-in caption_plan style_source must not be none")
		}
		if styleSource == clipCaptionStyleSourceOpusDefault {
			if err := validateOpusDefaultCaptionStyle(plan); err != nil {
				return err
			}
		}
		if strings.TrimSpace(request.CaptionInstructions) == "" && styleSource != clipCaptionStyleSourceOpusDefault {
			return fmt.Errorf("caption_plan style_source must be opus_default when no caption style instructions are provided")
		}
	}
	return nil
}

func validateOpusDefaultCaptionStyle(plan ClipCaptionPlan) error {
	fontFamily, _ := normalizeCaptionFontKey(plan.FontFamily)
	borderThickness, _ := normalizeCaptionBorderThickness(plan.BorderThickness)
	if fontFamily != opusDefaultCaptionFontFamily ||
		strings.TrimSpace(plan.FontColor) != opusDefaultCaptionFontColor ||
		strings.TrimSpace(plan.HighlightColor) != opusDefaultCaptionHighlightColor ||
		strings.TrimSpace(plan.BorderColor) != opusDefaultCaptionBorderColor ||
		borderThickness != opusDefaultCaptionBorderThickness ||
		strings.TrimSpace(plan.BackgroundColor) != opusDefaultCaptionBackgroundColor ||
		plan.BackgroundOpacity != opusDefaultCaptionBackgroundOpacity {
		return fmt.Errorf("caption_plan style_source opus_default requires font_family=default, font_color=white, highlight_color=yellow, border_color=black, border_thickness=thick, background_color=transparent, and background_opacity=0")
	}
	return nil
}

func validateClipCaptionRegions(plan ClipCaptionPlan, maxVideoZ int) (map[string]ClipCaptionRegion, error) {
	if len(plan.Regions) == 0 {
		return nil, fmt.Errorf("caption_plan requires at least one caption region")
	}
	if len(plan.Regions) > maxClipCaptionRegions {
		return nil, fmt.Errorf("caption_plan has too many regions")
	}
	regions := make(map[string]ClipCaptionRegion, len(plan.Regions))
	for _, region := range plan.Regions {
		id := strings.TrimSpace(region.ID)
		if id == "" {
			return nil, fmt.Errorf("caption region id is required")
		}
		if _, exists := regions[id]; exists {
			return nil, fmt.Errorf("caption region id %q is duplicated", id)
		}
		if err := validateClipRect("caption output_rect", region.OutputRect); err != nil {
			return nil, err
		}
		if region.OutputRect.W < 420 || region.OutputRect.H < 40 {
			return nil, fmt.Errorf("caption region %q is too small", id)
		}
		switch strings.TrimSpace(region.HorizontalAlign) {
		case clipCaptionAlignLeft, clipCaptionAlignCenter, clipCaptionAlignRight:
		default:
			return nil, fmt.Errorf("caption region %q has invalid horizontal_align", id)
		}
		switch strings.TrimSpace(region.VerticalAlign) {
		case clipCaptionAlignTop, clipCaptionAlignMiddle, clipCaptionAlignBottom:
		default:
			return nil, fmt.Errorf("caption region %q has invalid vertical_align", id)
		}
		if region.MaxLines < 1 || region.MaxLines > 2 {
			return nil, fmt.Errorf("caption region %q max_lines must be between 1 and 2", id)
		}
		if region.ZIndex <= maxVideoZ {
			return nil, fmt.Errorf("caption region %q z_index must render above video regions", id)
		}
		regions[id] = region
	}
	return regions, nil
}

func validateClipCaptionCues(plan ClipCaptionPlan, request ClipCompositionRequest, refs clipCaptionReferences, regions map[string]ClipCaptionRegion, renderPlans []ClipFrameRenderPlan) error {
	previousEndByRegion := map[string]float64{}
	for index, cue := range plan.Cues {
		regionID := strings.TrimSpace(cue.CaptionRegionID)
		_, ok := regions[regionID]
		if !ok {
			return fmt.Errorf("caption cue %d references missing region %q", index+1, regionID)
		}
		startSeconds, endSeconds, err := captionCueTiming(cue, plan.TimingQuality, refs)
		if err != nil {
			return fmt.Errorf("caption cue %d: %w", index+1, err)
		}
		if invalidSeconds(startSeconds) || invalidSeconds(endSeconds) || endSeconds <= startSeconds {
			return fmt.Errorf("caption cue %d has invalid source timing", index+1)
		}
		if !captionCueWithinClipSegments(startSeconds, endSeconds, request.Clip.Segments) {
			return fmt.Errorf("caption cue %d is outside clip segment timing", index+1)
		}
		if !captionCueWithinSingleRenderPlan(startSeconds, endSeconds, renderPlans) {
			return fmt.Errorf("caption cue %d crosses render plan boundaries", index+1)
		}
		if previousEnd, exists := previousEndByRegion[regionID]; exists && startSeconds < previousEnd-0.02 {
			return fmt.Errorf("caption cues overlap in region %q", regionID)
		}
		previousEndByRegion[regionID] = endSeconds
		text, _, err := captionCueText(cue, plan.TimingQuality, refs)
		if err != nil {
			return fmt.Errorf("caption cue %d: %w", index+1, err)
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("caption cue %d text is empty", index+1)
		}
		for _, wordID := range cue.EmphasisWordIDs {
			if _, ok := refs.wordsByID[strings.TrimSpace(wordID)]; !ok {
				return fmt.Errorf("caption cue %d references missing emphasis word %q", index+1, wordID)
			}
		}
	}
	return nil
}

func maxVideoRegionZIndex(plans []ClipFrameRenderPlan) int {
	maxZ := 0
	for _, plan := range plans {
		for _, region := range plan.Regions {
			if region.ZIndex > maxZ {
				maxZ = region.ZIndex
			}
		}
	}
	return maxZ
}

func captionCueWithinClipSegments(startSeconds float64, endSeconds float64, segments []ClipDecisionSegment) bool {
	for _, segment := range segments {
		if startSeconds >= segment.StartSeconds-0.05 && endSeconds <= segment.EndSeconds+0.05 {
			return true
		}
	}
	return false
}

func captionCueWithinSingleRenderPlan(startSeconds float64, endSeconds float64, plans []ClipFrameRenderPlan) bool {
	for _, plan := range plans {
		if startSeconds >= plan.SourceStartSeconds-0.02 && endSeconds <= plan.SourceEndSeconds+0.02 {
			return true
		}
	}
	return false
}

func captionCueTiming(cue ClipCaptionCue, timingQuality string, refs clipCaptionReferences) (float64, float64, error) {
	switch strings.TrimSpace(timingQuality) {
	case clipCaptionTimingWord:
		words, err := captionCueWords(cue, refs)
		if err != nil {
			return 0, 0, err
		}
		return words[0].StartSeconds, words[len(words)-1].EndSeconds, nil
	case clipCaptionTimingSegment:
		segments, err := captionCueSegments(cue, refs)
		if err != nil {
			return 0, 0, err
		}
		return segments[0].StartSeconds, segments[len(segments)-1].EndSeconds, nil
	default:
		return 0, 0, fmt.Errorf("unsupported caption timing quality %q", timingQuality)
	}
}

func captionCueText(cue ClipCaptionCue, timingQuality string, refs clipCaptionReferences) (string, []TranscriptWord, error) {
	switch strings.TrimSpace(timingQuality) {
	case clipCaptionTimingWord:
		words, err := captionCueWords(cue, refs)
		if err != nil {
			return "", nil, err
		}
		parts := make([]string, 0, len(words))
		for _, word := range words {
			if text := cleanTranscriptText(word.Text); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " "), words, nil
	case clipCaptionTimingSegment:
		segments, err := captionCueSegments(cue, refs)
		if err != nil {
			return "", nil, err
		}
		parts := make([]string, 0, len(segments))
		for _, segment := range segments {
			if text := cleanTranscriptText(segment.Text); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " "), nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported caption timing quality %q", timingQuality)
	}
}

func captionCueWords(cue ClipCaptionCue, refs clipCaptionReferences) ([]TranscriptWord, error) {
	if len(cue.WordIDs) != 2 {
		return nil, fmt.Errorf("word-timed cue requires word_ids with exactly two entries")
	}
	startID := strings.TrimSpace(cue.WordIDs[0])
	endID := strings.TrimSpace(cue.WordIDs[1])
	if startID == "" || endID == "" {
		return nil, fmt.Errorf("word-timed cue requires non-empty word_ids")
	}
	startIndex, startOK := refs.wordIndexByID[startID]
	endIndex, endOK := refs.wordIndexByID[endID]
	if !startOK || !endOK {
		return nil, fmt.Errorf("word-timed cue references missing word IDs")
	}
	if endIndex < startIndex {
		return nil, fmt.Errorf("word-timed cue word_ids end precedes start")
	}
	words := append([]TranscriptWord(nil), refs.wordOrder[startIndex:endIndex+1]...)
	if len(words) == 0 {
		return nil, fmt.Errorf("word-timed cue references no words")
	}
	return words, nil
}

func captionCueSegments(cue ClipCaptionCue, refs clipCaptionReferences) ([]ClipCompositionTranscriptSegment, error) {
	if len(cue.SourceSegmentIDs) == 0 {
		return nil, fmt.Errorf("segment-timed cue requires source_segment_ids")
	}
	segments := make([]ClipCompositionTranscriptSegment, 0, len(cue.SourceSegmentIDs))
	for _, id := range cue.SourceSegmentIDs {
		segmentID := strings.TrimSpace(id)
		segment, ok := refs.segmentsByID[segmentID]
		if !ok {
			return nil, fmt.Errorf("missing source segment %q", id)
		}
		segments = append(segments, segment)
	}
	sort.SliceStable(segments, func(i, j int) bool {
		return segments[i].StartSeconds < segments[j].StartSeconds
	})
	for index := 1; index < len(segments); index++ {
		if segments[index].StartSeconds < segments[index-1].EndSeconds-0.02 {
			return nil, fmt.Errorf("segment-timed cue source segments overlap")
		}
	}
	return segments, nil
}

func (s *Service) buildClipCaptionASSStyle(plan ClipCaptionPlan, target ClipResolution) (clipCaptionASSStyle, error) {
	font, err := s.resolveCaptionFont(plan.FontFamily)
	if err != nil {
		return clipCaptionASSStyle{}, err
	}
	primary, err := captionASSOpaqueColor(plan.FontColor)
	if err != nil {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions font_color %w", err)
	}
	highlight, err := captionASSOpaqueColor(plan.HighlightColor)
	if err != nil {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions highlight_color %w", err)
	}
	border, err := captionASSOpaqueColor(plan.BorderColor)
	if err != nil {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions border_color %w", err)
	}
	if _, ok := normalizeCaptionBorderThickness(plan.BorderThickness); !ok {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions border_thickness must be one of none, thin, medium, thick, or extra_thick")
	}
	if invalidCaptionOpacity(plan.BackgroundOpacity) {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions background_opacity must be between 0 and 1")
	}
	if captionBackgroundColorSpecified(plan.BackgroundColor) && plan.BackgroundOpacity <= 0 {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions background_opacity must be greater than 0 when background_color is set")
	}
	if !captionBackgroundColorSpecified(plan.BackgroundColor) && plan.BackgroundOpacity > 0 {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions background_color must be a color when background_opacity is greater than 0")
	}
	back, err := captionASSBackgroundColor(plan.BackgroundColor, plan.BackgroundOpacity)
	if err != nil {
		return clipCaptionASSStyle{}, fmt.Errorf("youtube clip captions background_color %w", err)
	}
	borderStyle := 1
	shadowSize := captionShadowSize(target)
	if captionBackgroundEnabled(plan.BackgroundColor, plan.BackgroundOpacity) {
		borderStyle = 3
		shadowSize = 0
	}
	return clipCaptionASSStyle{
		Font:            font,
		PrimaryColor:    primary,
		HighlightColor:  highlight,
		BorderColor:     border,
		BackColor:       back,
		BorderStyle:     borderStyle,
		BorderThickness: captionBorderSize(target, plan.BorderThickness),
		ShadowSize:      shadowSize,
		Uppercase:       strings.TrimSpace(plan.StyleSource) == clipCaptionStyleSourceOpusDefault,
	}, nil
}

func buildClipASSSubtitle(plan ClipCaptionPlan, renderPlan ClipFrameRenderPlan, target ClipResolution, refs clipCaptionReferences, style clipCaptionASSStyle) (string, error) {
	if target.Width <= 0 || target.Height <= 0 {
		return "", fmt.Errorf("youtube clip captions failed: invalid target resolution")
	}
	if strings.TrimSpace(style.Font.Family) == "" {
		return "", fmt.Errorf("youtube clip captions failed: resolved caption font family is empty")
	}
	regions := make(map[string]ClipCaptionRegion, len(plan.Regions))
	for _, region := range plan.Regions {
		regions[strings.TrimSpace(region.ID)] = region
	}
	var builder strings.Builder
	builder.WriteString("[Script Info]\n")
	builder.WriteString("ScriptType: v4.00+\n")
	builder.WriteString(fmt.Sprintf("PlayResX: %d\n", target.Width))
	builder.WriteString(fmt.Sprintf("PlayResY: %d\n", target.Height))
	builder.WriteString("ScaledBorderAndShadow: yes\n")
	builder.WriteString("WrapStyle: 0\n\n")
	builder.WriteString("[V4+ Styles]\n")
	builder.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	builder.WriteString(fmt.Sprintf("Style: PandaCaption,%s,%d,%s,%s,%s,%s,-1,0,0,0,%d,100,0,0,%d,%d,%d,5,40,40,40,1\n\n",
		escapeASSStyleField(style.Font.Family),
		captionFontSize(target),
		style.PrimaryColor,
		style.HighlightColor,
		style.BorderColor,
		style.BackColor,
		captionStyleScaleX,
		style.BorderStyle,
		style.BorderThickness,
		style.ShadowSize,
	))
	builder.WriteString("[Events]\n")
	builder.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, cue := range plan.Cues {
		sourceStart, sourceEnd, err := captionCueTiming(cue, plan.TimingQuality, refs)
		if err != nil {
			return "", fmt.Errorf("youtube clip captions failed: %w", err)
		}
		if sourceStart < renderPlan.SourceStartSeconds-0.02 || sourceEnd > renderPlan.SourceEndSeconds+0.02 {
			continue
		}
		region, ok := regions[strings.TrimSpace(cue.CaptionRegionID)]
		if !ok {
			return "", fmt.Errorf("youtube clip captions failed: cue references missing region %q", cue.CaptionRegionID)
		}
		text, words, err := captionCueText(cue, plan.TimingQuality, refs)
		if err != nil {
			return "", fmt.Errorf("youtube clip captions failed: %w", err)
		}
		eventText := captionASSEventText(text, words, cue.EmphasisWordIDs, region, target, style)
		if strings.TrimSpace(eventText) == "" {
			continue
		}
		start := math.Max(0, sourceStart-renderPlan.SourceStartSeconds)
		end := math.Max(start+0.05, sourceEnd-renderPlan.SourceStartSeconds)
		builder.WriteString(fmt.Sprintf(
			"Dialogue: 0,%s,%s,PandaCaption,,0,0,0,,%s%s%s\n",
			formatASSTime(start),
			formatASSTime(end),
			captionASSRegionOverride(region, target),
			captionASSFitOverride(eventText, region, target, style),
			eventText,
		))
	}
	return builder.String(), nil
}

func captionASSEventText(text string, words []TranscriptWord, emphasisWordIDs []string, region ClipCaptionRegion, target ClipResolution, style clipCaptionASSStyle) string {
	emphasis := map[string]struct{}{}
	for _, id := range emphasisWordIDs {
		emphasis[strings.TrimSpace(id)] = struct{}{}
	}
	maxLines := captionMaxLines(region)
	maxVisibleChars := captionMaxLineVisibleChars(region, target)
	if len(words) == 0 {
		cleaned := cleanTranscriptText(text)
		if style.Uppercase {
			cleaned = strings.ToUpper(cleaned)
		}
		return assLineBreaks(escapeASSText(cleaned), captionWordCount(cleaned), maxLines, maxVisibleChars)
	}
	tokens := make([]string, 0, len(words))
	for _, word := range words {
		wordText := cleanTranscriptText(word.Text)
		if wordText == "" {
			continue
		}
		if style.Uppercase {
			wordText = strings.ToUpper(wordText)
		}
		token := escapeASSText(wordText)
		if duration := word.EndSeconds - word.StartSeconds; duration > 0 {
			token = fmt.Sprintf("{\\k%d}%s", int(math.Round(duration*100)), token)
		}
		if _, ok := emphasis[word.ID]; ok {
			token = "{\\c" + captionASSOverrideColor(style.HighlightColor) + "}" + token + "{\\c" + captionASSOverrideColor(style.PrimaryColor) + "}"
		}
		tokens = append(tokens, token)
	}
	return assLineBreakTokens(tokens, maxLines, maxVisibleChars)
}

func assLineBreaks(text string, wordCount int, maxLines int, maxVisibleChars int) string {
	if wordCount <= 0 || maxLines <= 1 {
		return text
	}
	return assLineBreakTokens(strings.Fields(text), maxLines, maxVisibleChars)
}

func assLineBreakTokens(tokens []string, maxLines int, maxVisibleChars int) string {
	if len(tokens) == 0 {
		return ""
	}
	if maxLines <= 1 || len(tokens) <= 1 {
		return strings.Join(tokens, " ")
	}
	if maxVisibleChars <= 0 {
		maxVisibleChars = 12
	}
	lines := make([][]string, 0, maxLines)
	current := make([]string, 0, maxCaptionWordsPerLine)
	currentVisible := 0
	for _, token := range tokens {
		tokenVisible := assTokenVisibleRuneCount(token)
		nextVisible := tokenVisible
		if len(current) > 0 {
			nextVisible = currentVisible + 1 + tokenVisible
		}
		if len(current) > 0 && len(lines)+1 < maxLines && (len(current) >= maxCaptionWordsPerLine || nextVisible > maxVisibleChars) {
			lines = append(lines, current)
			current = make([]string, 0, maxCaptionWordsPerLine)
			currentVisible = 0
		}
		if len(current) > 0 {
			currentVisible++
		}
		current = append(current, token)
		currentVisible += tokenVisible
	}
	if len(current) > 0 {
		lines = append(lines, current)
	}
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, strings.Join(line, " "))
	}
	return strings.Join(parts, `\N`)
}

func captionMaxLines(region ClipCaptionRegion) int {
	if region.MaxLines < 1 {
		return 1
	}
	if region.MaxLines > 2 {
		return 2
	}
	return region.MaxLines
}

func captionMaxLineVisibleChars(region ClipCaptionRegion, target ClipResolution) int {
	rect := outputPixelRect(region.OutputRect, target)
	usableWidth := captionAvailableTextWidth(region, target, clipCaptionASSStyle{})
	if usableWidth <= 0 {
		usableWidth = rect.W
	}
	averageUppercaseGlyphWidth := float64(captionFontSize(target)) * 0.62 * float64(captionStyleScaleX) / 100
	if averageUppercaseGlyphWidth <= 0 {
		return 12
	}
	maxChars := int(math.Floor(float64(usableWidth) / averageUppercaseGlyphWidth))
	if maxChars < 8 {
		return 8
	}
	if maxChars > 13 {
		return 13
	}
	return maxChars
}

func captionASSFitOverride(eventText string, region ClipCaptionRegion, target ClipResolution, style clipCaptionASSStyle) string {
	baseFontSize := captionFontSize(target)
	maxWidth := captionAvailableTextWidth(region, target, style)
	if baseFontSize <= 0 || maxWidth <= 0 {
		return ""
	}
	estimatedWidth := captionEstimatedMaxLineWidth(eventText, baseFontSize, captionStyleScaleX)
	if estimatedWidth <= maxWidth {
		return ""
	}
	fittedFontSize := int(math.Floor(float64(baseFontSize) * float64(maxWidth) / float64(estimatedWidth)))
	minFontSize := captionMinFittedFontSize(target)
	if fittedFontSize < minFontSize {
		fittedFontSize = minFontSize
	}
	fittedWidth := captionEstimatedMaxLineWidth(eventText, fittedFontSize, captionStyleScaleX)
	if fittedWidth <= maxWidth {
		return fmt.Sprintf("{\\fs%d}", fittedFontSize)
	}
	scaleX := int(math.Floor(float64(captionStyleScaleX) * float64(maxWidth) / float64(fittedWidth)))
	if scaleX < 58 {
		scaleX = 58
	}
	if scaleX > captionStyleScaleX {
		scaleX = captionStyleScaleX
	}
	return fmt.Sprintf("{\\fs%d\\fscx%d}", fittedFontSize, scaleX)
}

func captionAvailableTextWidth(region ClipCaptionRegion, target ClipResolution, style clipCaptionASSStyle) int {
	rect := outputPixelRect(region.OutputRect, target)
	padding := captionRegionHorizontalPadding(target)
	x, _ := captionASSPosition(region, target)
	safeMargin := captionCanvasSafeMargin(target)
	leftSpace := x - safeMargin
	rightSpace := target.Width - safeMargin - x
	if leftSpace < 0 {
		leftSpace = 0
	}
	if rightSpace < 0 {
		rightSpace = 0
	}
	var canvasWidth int
	var regionWidth int
	switch strings.TrimSpace(region.HorizontalAlign) {
	case clipCaptionAlignLeft:
		canvasWidth = rightSpace
		regionWidth = rect.W - padding
	case clipCaptionAlignRight:
		canvasWidth = leftSpace
		regionWidth = rect.W - padding
	default:
		canvasWidth = 2 * captionMinInt(leftSpace, rightSpace)
		regionWidth = rect.W - 2*padding
	}
	if regionWidth <= 0 {
		regionWidth = rect.W
	}
	width := captionMinInt(regionWidth, canvasWidth)
	outlineMargin := 2 * (style.BorderThickness + style.ShadowSize + 2)
	width -= outlineMargin
	if width < 120 {
		return 120
	}
	return width
}

func captionCanvasSafeMargin(target ClipResolution) int {
	margin := int(math.Round(float64(target.Width) * 0.045))
	if margin < 24 {
		return 24
	}
	if margin > 64 {
		return 64
	}
	return margin
}

func captionMinInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func captionMinFittedFontSize(target ClipResolution) int {
	size := int(math.Round(float64(target.Height) * 0.032))
	if size < 40 {
		return 40
	}
	if size > 62 {
		return 62
	}
	return size
}

func captionEstimatedMaxLineWidth(eventText string, fontSize int, scaleX int) int {
	maxWeight := 0.0
	for _, line := range strings.Split(eventText, `\N`) {
		weight := assVisibleLineWeight(line)
		if weight > maxWeight {
			maxWeight = weight
		}
	}
	return int(math.Ceil(maxWeight * float64(fontSize) * float64(scaleX) / 100))
}

func assVisibleLineWeight(text string) float64 {
	weight := 0.0
	inOverride := false
	for _, r := range text {
		switch r {
		case '{':
			inOverride = true
		case '}':
			inOverride = false
		default:
			if inOverride {
				continue
			}
			weight += assVisibleRuneWeight(r)
		}
	}
	return weight
}

func assVisibleRuneWeight(r rune) float64 {
	switch {
	case unicode.IsSpace(r):
		return 0.32
	case strings.ContainsRune(".,:;!|'`", r):
		return 0.22
	case strings.ContainsRune("MWmw", r):
		return 0.95
	case strings.ContainsRune("Iil1", r):
		return 0.34
	case unicode.IsUpper(r):
		return 0.68
	case unicode.IsDigit(r):
		return 0.56
	default:
		return 0.58
	}
}

func assTokenVisibleRuneCount(token string) int {
	visible := 0
	inOverride := false
	for _, r := range token {
		switch r {
		case '{':
			inOverride = true
		case '}':
			inOverride = false
		default:
			if !inOverride {
				visible++
			}
		}
	}
	return visible
}

func captionASSRegionOverride(region ClipCaptionRegion, target ClipResolution) string {
	x, y := captionASSPosition(region, target)
	return fmt.Sprintf("{\\an%d\\pos(%d,%d)\\q2}",
		captionASSAlignment(region),
		x,
		y,
	)
}

func captionASSPosition(region ClipCaptionRegion, target ClipResolution) (int, int) {
	rect := outputPixelRect(region.OutputRect, target)
	padding := captionRegionHorizontalPadding(target)
	x := rect.X + rect.W/2
	switch strings.TrimSpace(region.HorizontalAlign) {
	case clipCaptionAlignLeft:
		x = rect.X + padding
	case clipCaptionAlignRight:
		x = rect.X + rect.W - padding
	}
	y := rect.Y + rect.H/2
	switch strings.TrimSpace(region.VerticalAlign) {
	case clipCaptionAlignTop:
		y = rect.Y + padding
	case clipCaptionAlignBottom:
		y = rect.Y + rect.H - padding
	}
	return x, y
}

func captionRegionHorizontalPadding(target ClipResolution) int {
	padding := int(math.Round(float64(target.Width) * 0.06))
	if padding < 32 {
		return 32
	}
	if padding > 76 {
		return 76
	}
	return padding
}

func captionASSAlignment(region ClipCaptionRegion) int {
	horizontal := 2
	switch strings.TrimSpace(region.HorizontalAlign) {
	case clipCaptionAlignLeft:
		horizontal = 1
	case clipCaptionAlignRight:
		horizontal = 3
	}
	switch strings.TrimSpace(region.VerticalAlign) {
	case clipCaptionAlignTop:
		return 6 + horizontal
	case clipCaptionAlignMiddle:
		return 3 + horizontal
	default:
		return horizontal
	}
}

func captionFontSize(target ClipResolution) int {
	size := int(math.Round(float64(target.Height) * 0.047))
	if size < 44 {
		return 44
	}
	if size > 92 {
		return 92
	}
	return size
}

func captionBorderSize(target ClipResolution, thickness string) int {
	key, ok := normalizeCaptionBorderThickness(thickness)
	if !ok || key == clipCaptionBorderMedium {
		key = clipCaptionBorderMedium
	}
	if key == clipCaptionBorderNone {
		return 0
	}
	ratio := 0.006
	switch key {
	case clipCaptionBorderThin:
		ratio = 0.0035
	case clipCaptionBorderThick:
		ratio = 0.009
	case clipCaptionBorderExtraThick:
		ratio = 0.012
	}
	size := int(math.Round(float64(target.Height) * ratio))
	if size < 4 {
		return 4
	}
	if size > 24 {
		return 24
	}
	return size
}

func captionShadowSize(target ClipResolution) int {
	size := int(math.Round(float64(target.Height) * 0.0025))
	if size < 2 {
		return 2
	}
	if size > 5 {
		return 5
	}
	return size
}

func appendClipCaptionFilterGraph(filterGraph string, assPath string, fontPath string) string {
	return filterGraph + fmt.Sprintf(";[vout]subtitles=filename='%s':fontsdir='%s',format=yuv420p[vcaption]",
		escapeFFmpegFilterValue(assPath),
		escapeFFmpegFilterValue(filepath.Dir(fontPath)),
	)
}

func defaultCaptionFontPath() string {
	if runtime.GOOS == "darwin" {
		return darwinDefaultCaptionFontPath
	}
	return linuxDefaultCaptionFontPath
}

func defaultCaptionFontFamily() string {
	if runtime.GOOS == "darwin" {
		return darwinDefaultCaptionFontFamily
	}
	return linuxDefaultCaptionFontFamily
}

func (s *Service) resolveCaptionFont(value string) (clipCaptionResolvedFont, error) {
	key, err := normalizeCaptionFontKey(value)
	if err != nil {
		return clipCaptionResolvedFont{}, err
	}
	if key == clipCaptionFontDefault {
		font := clipCaptionResolvedFont{
			Key:    key,
			Family: strings.TrimSpace(s.captionFontFamily),
			Path:   strings.TrimSpace(s.captionFontPath),
		}
		if font.Family == "" {
			font.Family = defaultCaptionFontFamily()
		}
		if font.Path == "" {
			font.Path = defaultCaptionFontPath()
		}
		if err := validateCaptionFontFile(font.Path); err != nil {
			return clipCaptionResolvedFont{}, fmt.Errorf("youtube clip captions default font is unavailable at %s: %w", font.Path, err)
		}
		return font, nil
	}
	spec, ok := captionFontSpecs()[key]
	if !ok {
		return clipCaptionResolvedFont{}, fmt.Errorf("caption_plan font_family must be one of default, inter, arial, arial_narrow, or dejavu_sans_condensed")
	}
	for _, path := range spec.Paths {
		if err := validateCaptionFontFile(path); err == nil {
			return clipCaptionResolvedFont{Key: key, Family: spec.Family, Path: path}, nil
		}
	}
	return clipCaptionResolvedFont{}, fmt.Errorf("youtube clip captions requested font %q is unavailable; install the font or configure youtube_clip_caption_font_path/youtube_clip_caption_font_family", spec.Family)
}

func validateCaptionFontFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("missing font path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Size() <= 0 {
		return fmt.Errorf("not a readable font file")
	}
	return nil
}

func captionFontSpecs() map[string]clipCaptionFontSpec {
	return map[string]clipCaptionFontSpec{
		clipCaptionFontInter: {
			Key:    clipCaptionFontInter,
			Family: "Inter",
			Paths: []string{
				"/usr/share/fonts/truetype/inter/Inter-roman.var.ttf",
				"/usr/share/fonts/truetype/inter/Inter.var.ttf",
				"/usr/share/fonts/truetype/inter/Inter-Bold.ttf",
				"/usr/share/fonts/truetype/inter-v/Inter.var.ttf",
				"/Library/Fonts/Inter.ttc",
				"/Library/Fonts/Inter Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Inter.ttc"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Inter Bold.ttf"),
			},
		},
		clipCaptionFontArial: {
			Key:    clipCaptionFontArial,
			Family: "Arial",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Arial Bold.ttf",
				"/Library/Fonts/Arial Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Arial Bold.ttf"),
			},
		},
		clipCaptionFontArialNarrow: {
			Key:    clipCaptionFontArialNarrow,
			Family: "Arial Narrow",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Arial Narrow Bold.ttf",
				"/Library/Fonts/Arial Narrow Bold.ttf",
				"/System/Library/Fonts/Supplemental/Arial Narrow.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Arial Narrow Bold.ttf"),
			},
		},
		clipCaptionFontDejaVuSansCondensed: {
			Key:    clipCaptionFontDejaVuSansCondensed,
			Family: linuxDefaultCaptionFontFamily,
			Paths: []string{
				linuxDefaultCaptionFontPath,
				"/usr/local/share/fonts/dejavu/DejaVuSansCondensed-Bold.ttf",
				"/opt/homebrew/share/fonts/dejavu/DejaVuSansCondensed-Bold.ttf",
			},
		},
	}
}

func normalizeCaptionFontKey(value string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	switch key {
	case "", clipCaptionFontDefault, "system", "system_sans":
		return clipCaptionFontDefault, nil
	case clipCaptionFontInter:
		return clipCaptionFontInter, nil
	case clipCaptionFontArial:
		return clipCaptionFontArial, nil
	case clipCaptionFontArialNarrow, "arialnarrow":
		return clipCaptionFontArialNarrow, nil
	case clipCaptionFontDejaVuSansCondensed, "dejavu", "dejavu_sans", "dejavu_sans_condensed_bold":
		return clipCaptionFontDejaVuSansCondensed, nil
	default:
		return "", fmt.Errorf("caption_plan font_family must be one of default, inter, arial, arial_narrow, or dejavu_sans_condensed")
	}
}

func normalizeCaptionBorderThickness(value string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	switch key {
	case "", clipCaptionBorderMedium:
		return clipCaptionBorderMedium, true
	case clipCaptionBorderNone, clipCaptionBorderThin, clipCaptionBorderThick, clipCaptionBorderExtraThick:
		return key, true
	default:
		return "", false
	}
}

func normalizeCaptionStyleSource(value string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	switch key {
	case clipCaptionStyleSourceNone, clipCaptionStyleSourceOpusDefault, clipCaptionStyleSourceUserSpecified, clipCaptionStyleSourceCreativeMix, clipCaptionStyleSourceClipPalette:
		return key, nil
	default:
		return "", fmt.Errorf("caption_plan style_source must be one of none, opus_default, user_specified, creative_mix, or clip_palette")
	}
}

func captionASSOpaqueColor(value string) (string, error) {
	r, g, b, err := parseCaptionColor(value, false)
	if err != nil {
		return "", err
	}
	return formatASSColor(r, g, b, 0), nil
}

func captionASSBackgroundColor(value string, opacity float64) (string, error) {
	if !captionBackgroundEnabled(value, opacity) {
		return formatASSColor(0, 0, 0, 128), nil
	}
	r, g, b, err := parseCaptionColor(value, true)
	if err != nil {
		return "", err
	}
	alpha := 255 - int(math.Round(opacity*255))
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 255 {
		alpha = 255
	}
	return formatASSColor(r, g, b, alpha), nil
}

func captionASSOverrideColor(styleColor string) string {
	color := strings.TrimSpace(styleColor)
	if strings.HasPrefix(color, "&H") && len(color) == len("&H00FFFFFF") {
		return "&H" + color[len("&H00"):] + "&"
	}
	return color
}

func captionBackgroundEnabled(value string, opacity float64) bool {
	return opacity > 0 && captionBackgroundColorSpecified(value)
}

func captionBackgroundColorSpecified(value string) bool {
	color := strings.ToLower(strings.TrimSpace(value))
	return color != "" && color != "transparent" && color != "none"
}

func invalidCaptionOpacity(value float64) bool {
	return value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0)
}

func parseCaptionColor(value string, allowTransparent bool) (int, int, int, error) {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	if allowTransparent && (key == "" || key == "transparent" || key == "none") {
		return 0, 0, 0, nil
	}
	if hexValue, ok := captionNamedColors()[key]; ok {
		key = hexValue
	}
	key = strings.TrimPrefix(key, "#")
	if len(key) == 3 {
		key = string([]byte{key[0], key[0], key[1], key[1], key[2], key[2]})
	}
	if len(key) != 6 {
		return 0, 0, 0, fmt.Errorf("must be a supported named color or #RRGGBB hex color")
	}
	value64, err := strconv.ParseUint(key, 16, 32)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("must be a supported named color or #RRGGBB hex color")
	}
	r := int((value64 >> 16) & 0xff)
	g := int((value64 >> 8) & 0xff)
	b := int(value64 & 0xff)
	return r, g, b, nil
}

func captionNamedColors() map[string]string {
	return map[string]string{
		"white":        "ffffff",
		"black":        "000000",
		"green":        "00ff00",
		"bright_green": "00ff00",
		"lime":         "84cc16",
		"yellow":       "ffe600",
		"red":          "ff3b30",
		"blue":         "2f80ff",
		"cyan":         "00d1ff",
		"magenta":      "ff2bd6",
		"orange":       "ff9500",
		"purple":       "a855f7",
		"pink":         "ff4fa3",
	}
}

func formatASSColor(r int, g int, b int, alpha int) string {
	return fmt.Sprintf("&H%02X%02X%02X%02X", alpha, b, g, r)
}

func validateCaptionRenderer(ctx context.Context, tools ToolPaths) error {
	path := strings.TrimSpace(tools.FFmpegPath)
	if path == "" {
		return fmt.Errorf("youtube clip captions require ffmpeg with libass subtitles support")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, path, "-hide_banner", "-filters").Output()
	if err != nil {
		if checkCtx.Err() != nil {
			return fmt.Errorf("youtube clip captions could not verify ffmpeg subtitles support: %w", checkCtx.Err())
		}
		return fmt.Errorf("youtube clip captions could not verify ffmpeg subtitles support: %w", err)
	}
	if !bytes.Contains(output, []byte(" subtitles ")) && !bytes.Contains(output, []byte(" ass ")) {
		return fmt.Errorf("youtube clip captions require ffmpeg with libass subtitles support")
	}
	return nil
}

func captionPlanMode(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.Mode)
}

func captionPlanStylePreset(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.StylePreset)
}

func captionPlanStyleSource(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.StyleSource)
}

func captionPlanTimingQuality(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.TimingQuality)
}

func captionPlanConfidence(plan *ClipCaptionPlan) float64 {
	if plan == nil {
		return 0
	}
	return plan.Confidence
}

func captionPlanReason(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.Reason)
}

func captionPlanFontFamily(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.FontFamily)
}

func captionPlanFontColor(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.FontColor)
}

func captionPlanHighlightColor(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.HighlightColor)
}

func captionPlanBorderColor(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.BorderColor)
}

func captionPlanBorderThickness(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.BorderThickness)
}

func captionPlanBackgroundColor(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.BackgroundColor)
}

func captionPlanBackgroundOpacity(plan *ClipCaptionPlan) float64 {
	if plan == nil {
		return 0
	}
	return plan.BackgroundOpacity
}

func invalidCaptionConfidence(value float64) bool {
	return value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0)
}

func captionWordCount(text string) int {
	return len(strings.Fields(strings.TrimSpace(text)))
}

func formatASSTime(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	totalCentiseconds := int(math.Round(seconds * 100))
	centiseconds := totalCentiseconds % 100
	totalSeconds := totalCentiseconds / 100
	secs := totalSeconds % 60
	totalMinutes := totalSeconds / 60
	minutes := totalMinutes % 60
	hours := totalMinutes / 60
	return fmt.Sprintf("%d:%02d:%02d.%02d", hours, minutes, secs, centiseconds)
}

func escapeASSText(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '{':
			builder.WriteString(`\{`)
		case '}':
			builder.WriteString(`\}`)
		case '\n', '\r':
			builder.WriteString(`\N`)
		default:
			if unicode.IsControl(r) && r != '\t' {
				continue
			}
			builder.WriteRune(r)
		}
	}
	return strings.TrimSpace(builder.String())
}

func escapeASSStyleField(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, ",", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	return text
}

func escapeFFmpegFilterValue(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		`:`, `\:`,
		`,`, `\,`,
		`[`, `\[`,
		`]`, `\]`,
	)
	return replacer.Replace(value)
}
