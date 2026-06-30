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
	clipCaptionFontRoboto              = "roboto"
	clipCaptionFontRobotoCondensed     = "roboto_condensed"
	clipCaptionFontOpenSans            = "open_sans"
	clipCaptionFontNotoSans            = "noto_sans"
	clipCaptionFontNotoSansDisplay     = "noto_sans_display"
	clipCaptionFontLiberationSans      = "liberation_sans"
	clipCaptionFontFreeSans            = "free_sans"
	clipCaptionFontBebasNeue           = "bebas_neue"
	clipCaptionFontCantarell           = "cantarell"
	clipCaptionFontHelvetica           = "helvetica"
	clipCaptionFontImpact              = "impact"
	clipCaptionFontFutura              = "futura"
	clipCaptionFontAvenirNext          = "avenir_next"
	clipCaptionFontDINCondensed        = "din_condensed"
	clipCaptionFontVerdana             = "verdana"
	clipCaptionFontTrebuchetMS         = "trebuchet_ms"
	clipCaptionFontGeorgia             = "georgia"
	clipCaptionFontArialBlack          = "arial_black"

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

	clipCaptionAnimationNone       = "none"
	clipCaptionAnimationPop        = "pop"
	clipCaptionAnimationBounce     = "bounce"
	clipCaptionAnimationFade       = "fade"
	clipCaptionAnimationSlideUp    = "slide_up"
	clipCaptionAnimationSlideDown  = "slide_down"
	clipCaptionAnimationSlideLeft  = "slide_left"
	clipCaptionAnimationSlideRight = "slide_right"
	clipCaptionAnimationZoomIn     = "zoom_in"

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
	Animation       string
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
	mode, err := normalizeClipCaptionMode(request.CaptionMode)
	if err != nil {
		return err
	}
	if result.CaptionPlan == nil {
		if mode == clipCaptionRequestOff {
			return nil
		}
		return fmt.Errorf("caption_plan is required when captions are auto or on")
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
	if _, err := normalizeCaptionStyleSource(plan.StyleSource); err != nil {
		return err
	}
	fontKey, err := normalizeCaptionFontKey(plan.FontFamily)
	if err != nil {
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
	if _, err := normalizeCaptionAnimation(plan.Animation); err != nil {
		return err
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
	case clipCaptionPlanModeBurnedIn:
		available, err := captionFontKeyAvailableForRequest(fontKey, request)
		if err != nil {
			return err
		}
		if !available {
			keys := mustCaptionFontKeysForRequest(request)
			return fmt.Errorf("caption_plan font_family %q is not available; available font_family values are %s", fontKey, captionFontKeyList(keys))
		}
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
			return fmt.Errorf("caption cue %d crosses render plan boundaries: cue source %.3f-%.3f must fit inside one render plan; overlapping render plans: %s", index+1, startSeconds, endSeconds, captionRenderPlanBoundarySummary(startSeconds, endSeconds, renderPlans))
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

func captionRenderPlanBoundarySummary(startSeconds float64, endSeconds float64, plans []ClipFrameRenderPlan) string {
	overlapping := make([]ClipFrameRenderPlan, 0, len(plans))
	for _, plan := range plans {
		if endSeconds < plan.SourceStartSeconds-0.02 || startSeconds > plan.SourceEndSeconds+0.02 {
			continue
		}
		overlapping = append(overlapping, plan)
	}
	if len(overlapping) == 0 {
		overlapping = append(overlapping, plans...)
	}
	sort.SliceStable(overlapping, func(i, j int) bool {
		if overlapping[i].AppliesToSegmentIndex == overlapping[j].AppliesToSegmentIndex {
			return overlapping[i].SourceStartSeconds < overlapping[j].SourceStartSeconds
		}
		return overlapping[i].AppliesToSegmentIndex < overlapping[j].AppliesToSegmentIndex
	})
	if len(overlapping) > 6 {
		overlapping = overlapping[:6]
	}
	parts := make([]string, 0, len(overlapping))
	for _, plan := range overlapping {
		parts = append(parts, fmt.Sprintf("segment %d %.3f-%.3f", plan.AppliesToSegmentIndex, plan.SourceStartSeconds, plan.SourceEndSeconds))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
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
		Animation:       normalizeCaptionAnimationOrDefault(plan.Animation),
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
		events := captionASSDialogueEvents(text, words, cue.EmphasisWordIDs, region, target, style, sourceStart, sourceEnd)
		for _, event := range events {
			if strings.TrimSpace(event.Text) == "" {
				continue
			}
			start := math.Max(0, event.StartSeconds-renderPlan.SourceStartSeconds)
			end := math.Max(start+0.05, event.EndSeconds-renderPlan.SourceStartSeconds)
			builder.WriteString(fmt.Sprintf(
				"Dialogue: 0,%s,%s,PandaCaption,,0,0,0,,%s%s\n",
				formatASSTime(start),
				formatASSTime(end),
				captionASSRegionOverride(region, target, style, end-start),
				event.Text,
			))
		}
	}
	return builder.String(), nil
}

type captionASSDialogueEvent struct {
	StartSeconds float64
	EndSeconds   float64
	Text         string
}

func captionASSDialogueEvents(text string, words []TranscriptWord, emphasisWordIDs []string, region ClipCaptionRegion, target ClipResolution, style clipCaptionASSStyle, sourceStart float64, sourceEnd float64) []captionASSDialogueEvent {
	if len(words) > 0 {
		chunks := captionWordChunks(words, region, target)
		events := make([]captionASSDialogueEvent, 0, len(chunks))
		for _, chunk := range chunks {
			eventText := captionASSEventText("", chunk, emphasisWordIDs, region, target, style)
			if strings.TrimSpace(eventText) == "" {
				continue
			}
			events = append(events, captionASSDialogueEvent{
				StartSeconds: chunk[0].StartSeconds,
				EndSeconds:   chunk[len(chunk)-1].EndSeconds,
				Text:         eventText,
			})
		}
		return events
	}
	chunks := captionTextChunks(text, region, target)
	if len(chunks) == 0 {
		return nil
	}
	duration := sourceEnd - sourceStart
	if duration <= 0 {
		duration = float64(len(chunks)) * 0.5
	}
	events := make([]captionASSDialogueEvent, 0, len(chunks))
	for index, chunk := range chunks {
		start := sourceStart + duration*float64(index)/float64(len(chunks))
		end := sourceStart + duration*float64(index+1)/float64(len(chunks))
		if end <= start {
			end = start + 0.05
		}
		events = append(events, captionASSDialogueEvent{
			StartSeconds: start,
			EndSeconds:   end,
			Text:         captionASSEventText(strings.Join(chunk, " "), nil, emphasisWordIDs, region, target, style),
		})
	}
	return events
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

func captionWordChunks(words []TranscriptWord, region ClipCaptionRegion, target ClipResolution) [][]TranscriptWord {
	chunks := make([][]TranscriptWord, 0)
	current := make([]TranscriptWord, 0, maxCaptionWordsPerLine*captionMaxLines(region))
	lineCount := 1
	lineWords := 0
	lineVisible := 0
	maxLines := captionMaxLines(region)
	maxVisibleChars := captionMaxLineVisibleChars(region, target)
	for _, word := range words {
		if cleanTranscriptText(word.Text) == "" {
			continue
		}
		visible := captionVisibleRuneCount(word.Text)
		if len(current) > 0 && captionTokenWouldOverflowLine(lineWords, lineVisible, visible, maxVisibleChars) {
			if lineCount < maxLines {
				lineCount++
				lineWords = 0
				lineVisible = 0
			} else {
				chunks = append(chunks, current)
				current = make([]TranscriptWord, 0, maxCaptionWordsPerLine*maxLines)
				lineCount = 1
				lineWords = 0
				lineVisible = 0
			}
		}
		if lineWords > 0 {
			lineVisible++
		}
		current = append(current, word)
		lineWords++
		lineVisible += visible
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func captionTextChunks(text string, region ClipCaptionRegion, target ClipResolution) [][]string {
	fields := strings.Fields(cleanTranscriptText(text))
	if len(fields) == 0 {
		return nil
	}
	chunks := make([][]string, 0)
	current := make([]string, 0, maxCaptionWordsPerLine*captionMaxLines(region))
	lineCount := 1
	lineWords := 0
	lineVisible := 0
	maxLines := captionMaxLines(region)
	maxVisibleChars := captionMaxLineVisibleChars(region, target)
	for _, field := range fields {
		visible := captionVisibleRuneCount(field)
		if len(current) > 0 && captionTokenWouldOverflowLine(lineWords, lineVisible, visible, maxVisibleChars) {
			if lineCount < maxLines {
				lineCount++
				lineWords = 0
				lineVisible = 0
			} else {
				chunks = append(chunks, current)
				current = make([]string, 0, maxCaptionWordsPerLine*maxLines)
				lineCount = 1
				lineWords = 0
				lineVisible = 0
			}
		}
		if lineWords > 0 {
			lineVisible++
		}
		current = append(current, field)
		lineWords++
		lineVisible += visible
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func captionTokenWouldOverflowLine(lineWords int, lineVisible int, tokenVisible int, maxVisibleChars int) bool {
	if lineWords == 0 {
		return false
	}
	return lineWords >= maxCaptionWordsPerLine || lineVisible+1+tokenVisible > maxVisibleChars
}

func captionVisibleRuneCount(text string) int {
	return len([]rune(cleanTranscriptText(text)))
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

func captionASSRegionOverride(region ClipCaptionRegion, target ClipResolution, style clipCaptionASSStyle, durationSeconds float64) string {
	x, y := captionASSPosition(region, target)
	alignment := captionASSAlignment(region)
	placement := fmt.Sprintf("\\an%d\\pos(%d,%d)\\q2", alignment, x, y)
	if move, ok := captionASSMoveOverride(style.Animation, alignment, x, y, target, durationSeconds); ok {
		placement = move + "\\q2"
	}
	return "{" + placement + captionASSAnimationOverride(style.Animation, durationSeconds) + "}"
}

func captionASSMoveOverride(animation string, alignment int, x int, y int, target ClipResolution, durationSeconds float64) (string, bool) {
	durationMS := captionAnimationEntryMillis(durationSeconds)
	if durationMS <= 0 {
		return "", false
	}
	offset := captionAnimationOffset(target)
	startX, startY := x, y
	switch normalizeCaptionAnimationOrDefault(animation) {
	case clipCaptionAnimationSlideUp:
		startY += offset
	case clipCaptionAnimationSlideDown:
		startY -= offset
	case clipCaptionAnimationSlideLeft:
		startX += offset
	case clipCaptionAnimationSlideRight:
		startX -= offset
	default:
		return "", false
	}
	return fmt.Sprintf("\\an%d\\move(%d,%d,%d,%d,0,%d)", alignment, startX, startY, x, y, durationMS), true
}

func captionASSAnimationOverride(animation string, durationSeconds float64) string {
	entryMS := captionAnimationEntryMillis(durationSeconds)
	exitMS := captionAnimationExitMillis(durationSeconds)
	switch normalizeCaptionAnimationOrDefault(animation) {
	case clipCaptionAnimationPop:
		if entryMS <= 0 {
			return ""
		}
		return fmt.Sprintf("\\fscx72\\fscy72\\t(0,%d,\\fscx%d\\fscy100)", entryMS, captionStyleScaleX)
	case clipCaptionAnimationBounce:
		if entryMS <= 0 {
			return ""
		}
		overshootMS := captionMinInt(entryMS, 90)
		settleMS := captionMinInt(entryMS+80, 190)
		return fmt.Sprintf("\\fscx76\\fscy76\\t(0,%d,\\fscx106\\fscy108)\\t(%d,%d,\\fscx%d\\fscy100)", overshootMS, overshootMS, settleMS, captionStyleScaleX)
	case clipCaptionAnimationFade:
		if entryMS <= 0 && exitMS <= 0 {
			return ""
		}
		return fmt.Sprintf("\\fad(%d,%d)", entryMS, exitMS)
	case clipCaptionAnimationZoomIn:
		if entryMS <= 0 {
			return ""
		}
		return fmt.Sprintf("\\fscx118\\fscy118\\t(0,%d,\\fscx%d\\fscy100)", entryMS, captionStyleScaleX)
	default:
		return ""
	}
}

func captionAnimationEntryMillis(durationSeconds float64) int {
	durationMS := int(math.Round(durationSeconds * 1000))
	if durationMS < 120 {
		return 0
	}
	entry := durationMS / 4
	if entry < 70 {
		return 70
	}
	if entry > 150 {
		return 150
	}
	return entry
}

func captionAnimationExitMillis(durationSeconds float64) int {
	durationMS := int(math.Round(durationSeconds * 1000))
	if durationMS < 220 {
		return 0
	}
	exit := durationMS / 5
	if exit < 60 {
		return 60
	}
	if exit > 120 {
		return 120
	}
	return exit
}

func captionAnimationOffset(target ClipResolution) int {
	offset := int(math.Round(float64(target.Height) * 0.035))
	if offset < 28 {
		return 28
	}
	if offset > 72 {
		return 72
	}
	return offset
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
		return clipCaptionResolvedFont{}, fmt.Errorf("caption_plan font_family must be one of %s", captionFontKeyList(allClipCaptionFontKeys()))
	}
	if font, ok := s.configuredCaptionFontForSpec(key, spec); ok {
		return font, nil
	}
	for _, path := range spec.Paths {
		if err := validateCaptionFontFile(path); err == nil {
			return clipCaptionResolvedFont{Key: key, Family: spec.Family, Path: path}, nil
		}
	}
	return clipCaptionResolvedFont{}, fmt.Errorf("youtube clip captions requested font %q is unavailable; install the font or configure youtube_clip_caption_font_path/youtube_clip_caption_font_family", spec.Family)
}

func (s *Service) configuredCaptionFontForSpec(key string, spec clipCaptionFontSpec) (clipCaptionResolvedFont, bool) {
	font := clipCaptionResolvedFont{
		Key:    key,
		Family: strings.TrimSpace(s.captionFontFamily),
		Path:   strings.TrimSpace(s.captionFontPath),
	}
	if font.Family == "" || !strings.EqualFold(font.Family, strings.TrimSpace(spec.Family)) {
		return clipCaptionResolvedFont{}, false
	}
	if err := validateCaptionFontFile(font.Path); err != nil {
		return clipCaptionResolvedFont{}, false
	}
	return font, true
}

func (s *Service) availableCaptionFontKeys() []string {
	keys := make([]string, 0, len(allClipCaptionFontKeys()))
	for _, key := range allClipCaptionFontKeys() {
		if _, err := s.resolveCaptionFont(key); err == nil {
			keys = append(keys, key)
		}
	}
	return keys
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

func allClipCaptionFontKeys() []string {
	return []string{
		clipCaptionFontDefault,
		clipCaptionFontInter,
		clipCaptionFontArial,
		clipCaptionFontArialNarrow,
		clipCaptionFontDejaVuSansCondensed,
		clipCaptionFontRoboto,
		clipCaptionFontRobotoCondensed,
		clipCaptionFontOpenSans,
		clipCaptionFontNotoSans,
		clipCaptionFontNotoSansDisplay,
		clipCaptionFontLiberationSans,
		clipCaptionFontFreeSans,
		clipCaptionFontBebasNeue,
		clipCaptionFontCantarell,
		clipCaptionFontHelvetica,
		clipCaptionFontImpact,
		clipCaptionFontFutura,
		clipCaptionFontAvenirNext,
		clipCaptionFontDINCondensed,
		clipCaptionFontVerdana,
		clipCaptionFontTrebuchetMS,
		clipCaptionFontGeorgia,
		clipCaptionFontArialBlack,
	}
}

func captionFontKeysForRequest(request ClipCompositionRequest) ([]string, error) {
	if len(request.AvailableCaptionFonts) == 0 {
		return allClipCaptionFontKeys(), nil
	}
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(request.AvailableCaptionFonts))
	for _, value := range request.AvailableCaptionFonts {
		key, err := normalizeCaptionFontKey(value)
		if err != nil {
			return nil, fmt.Errorf("available_caption_fonts contains unsupported font %q: %w", value, err)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("available_caption_fonts must include at least one supported font")
	}
	return keys, nil
}

func mustCaptionFontKeysForRequest(request ClipCompositionRequest) []string {
	keys, err := captionFontKeysForRequest(request)
	if err != nil || len(keys) == 0 {
		return []string{clipCaptionFontDefault}
	}
	return keys
}

func captionFontKeyAvailableForRequest(key string, request ClipCompositionRequest) (bool, error) {
	if len(request.AvailableCaptionFonts) == 0 {
		return true, nil
	}
	keys, err := captionFontKeysForRequest(request)
	if err != nil {
		return false, err
	}
	for _, available := range keys {
		if available == key {
			return true, nil
		}
	}
	return false, nil
}

func captionFontKeyList(keys []string) string {
	return strings.Join(keys, ", ")
}

func captionFontSpecs() map[string]clipCaptionFontSpec {
	return map[string]clipCaptionFontSpec{
		clipCaptionFontInter: {
			Key:    clipCaptionFontInter,
			Family: "Inter",
			Paths: []string{
				"/usr/share/fonts/opentype/inter/InterDisplay-Bold.otf",
				"/usr/share/fonts/opentype/inter/Inter-Bold.otf",
				"/usr/share/fonts/opentype/inter/InterDisplay-Regular.otf",
				"/usr/share/fonts/opentype/inter/Inter-Regular.otf",
				"/usr/share/fonts/truetype/inter/Inter-roman.var.ttf",
				"/usr/share/fonts/truetype/inter/Inter.var.ttf",
				"/usr/share/fonts/truetype/inter/Inter-Bold.ttf",
				"/usr/share/fonts/truetype/inter-v/Inter.var.ttf",
				"/Library/Fonts/Inter.ttc",
				"/Library/Fonts/Inter Bold.ttf",
				"/Library/Fonts/Inter-Bold.otf",
				"/Library/Fonts/InterDisplay-Bold.otf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Inter.ttc"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Inter Bold.ttf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Inter-Bold.otf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/InterDisplay-Bold.otf"),
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
		clipCaptionFontRoboto: {
			Key:    clipCaptionFontRoboto,
			Family: "Roboto",
			Paths: []string{
				"/usr/share/fonts/truetype/roboto/unhinted/RobotoTTF/Roboto-Bold.ttf",
				"/usr/share/fonts/truetype/roboto/unhinted/RobotoTTF/Roboto-Medium.ttf",
				"/usr/share/fonts/truetype/roboto/unhinted/RobotoTTF/Roboto-Regular.ttf",
				"/Library/Fonts/Roboto-Bold.ttf",
				"/Library/Fonts/Roboto Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Roboto-Bold.ttf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Roboto Bold.ttf"),
			},
		},
		clipCaptionFontRobotoCondensed: {
			Key:    clipCaptionFontRobotoCondensed,
			Family: "Roboto Condensed",
			Paths: []string{
				"/usr/share/fonts/truetype/roboto/unhinted/RobotoCondensed-Bold.ttf",
				"/usr/share/fonts/truetype/roboto/unhinted/RobotoCondensed-Medium.ttf",
				"/usr/share/fonts/truetype/roboto/unhinted/RobotoCondensed-Regular.ttf",
				"/Library/Fonts/RobotoCondensed-Bold.ttf",
				"/Library/Fonts/Roboto Condensed Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/RobotoCondensed-Bold.ttf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Roboto Condensed Bold.ttf"),
			},
		},
		clipCaptionFontOpenSans: {
			Key:    clipCaptionFontOpenSans,
			Family: "Open Sans",
			Paths: []string{
				"/usr/share/fonts/truetype/open-sans/OpenSans-ExtraBold.ttf",
				"/usr/share/fonts/truetype/open-sans/OpenSans-Bold.ttf",
				"/usr/share/fonts/truetype/open-sans/OpenSans-Semibold.ttf",
				"/Library/Fonts/OpenSans-Bold.ttf",
				"/Library/Fonts/Open Sans Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/OpenSans-Bold.ttf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Open Sans Bold.ttf"),
			},
		},
		clipCaptionFontNotoSans: {
			Key:    clipCaptionFontNotoSans,
			Family: "Noto Sans",
			Paths: []string{
				"/usr/share/fonts/truetype/noto/NotoSans-Bold.ttf",
				"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
				"/Library/Fonts/NotoSans-Bold.ttf",
				"/Library/Fonts/Noto Sans Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/NotoSans-Bold.ttf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Noto Sans Bold.ttf"),
			},
		},
		clipCaptionFontNotoSansDisplay: {
			Key:    clipCaptionFontNotoSansDisplay,
			Family: "Noto Sans Display",
			Paths: []string{
				"/usr/share/fonts/truetype/noto/NotoSansDisplay-Bold.ttf",
				"/usr/share/fonts/truetype/noto/NotoSansDisplay-Regular.ttf",
				"/Library/Fonts/NotoSansDisplay-Bold.ttf",
				"/Library/Fonts/Noto Sans Display Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/NotoSansDisplay-Bold.ttf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Noto Sans Display Bold.ttf"),
			},
		},
		clipCaptionFontLiberationSans: {
			Key:    clipCaptionFontLiberationSans,
			Family: "Liberation Sans",
			Paths: []string{
				"/usr/share/fonts/truetype/liberation2/LiberationSans-Bold.ttf",
				"/usr/share/fonts/truetype/liberation2/LiberationSans-Regular.ttf",
				"/usr/local/share/fonts/liberation/LiberationSans-Bold.ttf",
				"/opt/homebrew/share/fonts/liberation/LiberationSans-Bold.ttf",
				"/Library/Fonts/LiberationSans-Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/LiberationSans-Bold.ttf"),
			},
		},
		clipCaptionFontFreeSans: {
			Key:    clipCaptionFontFreeSans,
			Family: "FreeSans",
			Paths: []string{
				"/usr/share/fonts/truetype/freefont/FreeSansBold.ttf",
				"/usr/share/fonts/truetype/freefont/FreeSans.ttf",
				"/usr/local/share/fonts/freefont/FreeSansBold.ttf",
				"/opt/homebrew/share/fonts/freefont/FreeSansBold.ttf",
				"/Library/Fonts/FreeSansBold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/FreeSansBold.ttf"),
			},
		},
		clipCaptionFontBebasNeue: {
			Key:    clipCaptionFontBebasNeue,
			Family: "Bebas Neue",
			Paths: []string{
				"/usr/share/fonts/opentype/bebas-neue/BebasNeue-Bold.otf",
				"/usr/share/fonts/opentype/bebas-neue/BebasNeue-Regular.otf",
				"/Library/Fonts/BebasNeue-Bold.otf",
				"/Library/Fonts/Bebas Neue Bold.otf",
				"/Library/Fonts/BebasNeue-Regular.otf",
				"/Library/Fonts/Bebas Neue Regular.otf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/BebasNeue-Bold.otf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Bebas Neue Bold.otf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/BebasNeue-Regular.otf"),
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Bebas Neue Regular.otf"),
			},
		},
		clipCaptionFontCantarell: {
			Key:    clipCaptionFontCantarell,
			Family: "Cantarell",
			Paths: []string{
				"/usr/share/fonts/opentype/cantarell/Cantarell-ExtraBold.otf",
				"/usr/share/fonts/opentype/cantarell/Cantarell-Bold.otf",
				"/usr/share/fonts/opentype/cantarell/Cantarell-Regular.otf",
				"/Library/Fonts/Cantarell-Bold.otf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Cantarell-Bold.otf"),
			},
		},
		clipCaptionFontHelvetica: {
			Key:    clipCaptionFontHelvetica,
			Family: "Helvetica",
			Paths: []string{
				"/System/Library/Fonts/Helvetica.ttc",
				"/System/Library/Fonts/HelveticaNeue.ttc",
				"/Library/Fonts/Helvetica.ttc",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Helvetica.ttc"),
			},
		},
		clipCaptionFontImpact: {
			Key:    clipCaptionFontImpact,
			Family: "Impact",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Impact.ttf",
				"/Library/Fonts/Impact.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Impact.ttf"),
			},
		},
		clipCaptionFontFutura: {
			Key:    clipCaptionFontFutura,
			Family: "Futura",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Futura.ttc",
				"/Library/Fonts/Futura.ttc",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Futura.ttc"),
			},
		},
		clipCaptionFontAvenirNext: {
			Key:    clipCaptionFontAvenirNext,
			Family: "Avenir Next",
			Paths: []string{
				"/System/Library/Fonts/Avenir Next.ttc",
				"/System/Library/Fonts/Avenir Next Condensed.ttc",
				"/Library/Fonts/Avenir Next.ttc",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Avenir Next.ttc"),
			},
		},
		clipCaptionFontDINCondensed: {
			Key:    clipCaptionFontDINCondensed,
			Family: "DIN Condensed",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/DIN Condensed Bold.ttf",
				"/System/Library/Fonts/Supplemental/DIN Alternate Bold.ttf",
				"/Library/Fonts/DIN Condensed Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/DIN Condensed Bold.ttf"),
			},
		},
		clipCaptionFontVerdana: {
			Key:    clipCaptionFontVerdana,
			Family: "Verdana",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Verdana Bold.ttf",
				"/System/Library/Fonts/Supplemental/Verdana.ttf",
				"/Library/Fonts/Verdana Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Verdana Bold.ttf"),
			},
		},
		clipCaptionFontTrebuchetMS: {
			Key:    clipCaptionFontTrebuchetMS,
			Family: "Trebuchet MS",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Trebuchet MS Bold.ttf",
				"/System/Library/Fonts/Supplemental/Trebuchet MS.ttf",
				"/Library/Fonts/Trebuchet MS Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Trebuchet MS Bold.ttf"),
			},
		},
		clipCaptionFontGeorgia: {
			Key:    clipCaptionFontGeorgia,
			Family: "Georgia",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Georgia Bold.ttf",
				"/System/Library/Fonts/Supplemental/Georgia.ttf",
				"/Library/Fonts/Georgia Bold.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Georgia Bold.ttf"),
			},
		},
		clipCaptionFontArialBlack: {
			Key:    clipCaptionFontArialBlack,
			Family: "Arial Black",
			Paths: []string{
				"/System/Library/Fonts/Supplemental/Arial Black.ttf",
				"/Library/Fonts/Arial Black.ttf",
				filepath.Join(os.Getenv("HOME"), "Library/Fonts/Arial Black.ttf"),
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
	case clipCaptionFontRoboto:
		return clipCaptionFontRoboto, nil
	case clipCaptionFontRobotoCondensed, "robotocondensed":
		return clipCaptionFontRobotoCondensed, nil
	case clipCaptionFontOpenSans, "opensans":
		return clipCaptionFontOpenSans, nil
	case clipCaptionFontNotoSans, "notosans":
		return clipCaptionFontNotoSans, nil
	case clipCaptionFontNotoSansDisplay, "notosansdisplay":
		return clipCaptionFontNotoSansDisplay, nil
	case clipCaptionFontLiberationSans, "liberation", "liberation_sans_bold", "liberationsans":
		return clipCaptionFontLiberationSans, nil
	case clipCaptionFontFreeSans, "freesans":
		return clipCaptionFontFreeSans, nil
	case clipCaptionFontBebasNeue, "bebas", "bebasneue":
		return clipCaptionFontBebasNeue, nil
	case clipCaptionFontCantarell:
		return clipCaptionFontCantarell, nil
	case clipCaptionFontHelvetica, "helvetica_neue", "helveticaneue":
		return clipCaptionFontHelvetica, nil
	case clipCaptionFontImpact:
		return clipCaptionFontImpact, nil
	case clipCaptionFontFutura:
		return clipCaptionFontFutura, nil
	case clipCaptionFontAvenirNext, "avenir", "avenirnext":
		return clipCaptionFontAvenirNext, nil
	case clipCaptionFontDINCondensed, "din", "dincondensed", "din_alternate", "dinalternate":
		return clipCaptionFontDINCondensed, nil
	case clipCaptionFontVerdana:
		return clipCaptionFontVerdana, nil
	case clipCaptionFontTrebuchetMS, "trebuchet", "trebuchetms":
		return clipCaptionFontTrebuchetMS, nil
	case clipCaptionFontGeorgia:
		return clipCaptionFontGeorgia, nil
	case clipCaptionFontArialBlack, "arialblack":
		return clipCaptionFontArialBlack, nil
	default:
		return "", fmt.Errorf("caption_plan font_family must be one of %s", captionFontKeyList(allClipCaptionFontKeys()))
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

func normalizeCaptionAnimation(value string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	switch key {
	case "", clipCaptionAnimationNone:
		return clipCaptionAnimationNone, nil
	case clipCaptionAnimationPop:
		return clipCaptionAnimationPop, nil
	case clipCaptionAnimationBounce:
		return clipCaptionAnimationBounce, nil
	case clipCaptionAnimationFade:
		return clipCaptionAnimationFade, nil
	case clipCaptionAnimationSlideUp, "rise", "rise_up":
		return clipCaptionAnimationSlideUp, nil
	case clipCaptionAnimationSlideDown, "drop", "drop_in":
		return clipCaptionAnimationSlideDown, nil
	case clipCaptionAnimationSlideLeft:
		return clipCaptionAnimationSlideLeft, nil
	case clipCaptionAnimationSlideRight:
		return clipCaptionAnimationSlideRight, nil
	case clipCaptionAnimationZoomIn, "zoom":
		return clipCaptionAnimationZoomIn, nil
	default:
		return "", fmt.Errorf("caption_plan animation must be one of %s", captionAnimationList(allClipCaptionAnimations()))
	}
}

func normalizeCaptionAnimationOrDefault(value string) string {
	animation, err := normalizeCaptionAnimation(value)
	if err != nil {
		return clipCaptionAnimationNone
	}
	return animation
}

func allClipCaptionAnimations() []string {
	return []string{
		clipCaptionAnimationNone,
		clipCaptionAnimationPop,
		clipCaptionAnimationBounce,
		clipCaptionAnimationFade,
		clipCaptionAnimationSlideUp,
		clipCaptionAnimationSlideDown,
		clipCaptionAnimationSlideLeft,
		clipCaptionAnimationSlideRight,
		clipCaptionAnimationZoomIn,
	}
}

func captionAnimationList(animations []string) string {
	return strings.Join(animations, ", ")
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

func captionPlanAnimation(plan *ClipCaptionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.Animation)
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
