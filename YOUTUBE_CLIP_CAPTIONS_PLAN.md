# YouTube Clip Captions Plan

## Reader And Action

Reader: the engineer implementing caption rendering for Panda's YouTube clipping feature.

After reading this, they should be able to add LLM-planned, burned-in subtitles to rendered clips without adding deterministic placement fallbacks, bypassing structured output, or leaving the existing visual composition path split into competing systems.

## Goal

Panda already detects strong YouTube moments, plans aspect-ratio-aware visual compositions, and renders each clip. The next version should also render high-quality short-form captions that feel comparable to Opus Clips: bold readable text, strong stroke/shadow contrast, clean line breaks, word or phrase highlighting when timing supports it, and placement that respects the actual clip layout.

Caption placement must be planned by the LLM as part of the clip composition decision. Some clips should use a lower global caption band. Others should attach captions to one side of a split-screen, to stacked regions, or to speaker-specific regions when that is more readable. Panda should not silently fall back to a hard-coded bottom-center subtitle position when the plan is missing or malformed. Missing, invalid, or unrenderable caption plans should go through the same structured-output repair and validation path as the rest of clip composition.

## Current State

The clipping flow has the right shape for this:

- Transcript segments are extracted before clip detection.
- The detector returns transcript-backed clip segments.
- The visual composition planner receives the clip, transcript timeline, sampled thumbnails, requested aspect ratio, and layout instructions.
- The planner already returns strict JSON and has a validation-plus-repair loop.
- The renderer turns each composition plan into an ffmpeg filter graph and concatenates multiple rendered segments when a clip is spliced or dynamically planned.

The gap is that "captions" currently appears only as a visual region role, not as a rendered subtitle layer. That role is ambiguous once Panda adds real generated captions. The implementation should split "preserve visible source captions" from "render Panda captions" and remove or rename the old ambiguous role during the change.

## Target Behavior

For each rendered clip, Panda should produce one burned-in video file that includes:

- The existing LLM-planned crop, stack, or overlay composition.
- A caption layer planned in the same LLM response as the visual composition.
- Caption timing tied to transcript-backed source spans, not invented text.
- A consistent short-form visual style: large condensed sans-serif text, white primary fill, thick black stroke, soft shadow, optional active-word highlight, and no low-contrast transparent boxes unless explicitly planned.
- Placement that is safe for the chosen aspect ratio and layout: bottom band for simple full-frame clips, per-panel regions for split-screen or stacked clips, and custom safe zones when lower-third text would hide important source content.
- Metadata in the tool result showing whether captions were rendered, which caption mode was used, and the planner confidence/reason.

Users should be able to ask for caption guidance naturally through the existing YouTube clipping tool, for example "big captions", "subtitles at the top", "do not cover the facecam", or "no subtitles". The model should pass that through as caption instructions instead of smuggling it into generic layout instructions.

## Non-Goals

- Do not add detachable `.srt`, `.vtt`, or sidecar files as the primary user artifact. The Discord-facing clip should have burned-in captions.
- Do not hand-write intent fallbacks such as "if vertical then bottom center." The LLM must return a valid caption plan or the request should fail/repair.
- Do not generate new transcript wording. Caption text must come from transcript spans.
- Do not implement browser-based testing for this work.

## Proposed Data Model

Extend the composition result with a first-class caption plan:

```json
{
  "caption_plan": {
    "mode": "burned_in",
    "style_preset": "opus_bold",
    "regions": [
      {
        "id": "bottom_global",
        "output_rect": {"x": 80, "y": 720, "w": 840, "h": 220},
        "horizontal_align": "center",
        "vertical_align": "middle",
        "max_lines": 2,
        "z_index": 20
      }
    ],
    "cues": [
      {
        "caption_region_id": "bottom_global",
        "source_start_seconds": 10.0,
        "source_end_seconds": 12.4,
        "start_word_id": "w_0001",
        "end_word_id": "w_0007",
        "emphasis_word_ids": ["w_0004"]
      }
    ],
    "confidence": 0.86,
    "reason": "Bottom captions avoid the speaker's face and leave the screen share readable."
  }
}
```

Use transcript word IDs when upstream transcription can provide word timing. If the current transcription provider does not return words in the configured response, add a real word-timing source before shipping Opus-quality captions. Acceptable sources are provider word timestamps or a deterministic forced-alignment engine driven by the existing audio and transcript. Segment-only timing can be supported only as an explicit lower-quality mode planned by the LLM and surfaced in metadata, not as a hidden fallback.

The LLM should plan regions and cue-to-region assignment. Code should render the plan faithfully, assemble caption text from the referenced transcript words, and reject invalid references.

## LLM Planning Changes

Fold caption planning into the existing composition planner rather than adding a separate caption planner. The planner has the visual thumbnails, output layout, transcript timeline, and requested aspect ratio in one place, which is exactly the context needed to decide where subtitles belong.

Prompt changes:

- Tell the planner that Panda renders a separate subtitle layer after video composition.
- Ask it to choose caption regions based on visible content, split screens, stacked regions, and user caption instructions.
- Require captions for normal spoken clips unless the user explicitly requested no captions or transcript quality is unusable.
- Require every cue to reference transcript word or segment IDs supplied by Panda.
- Require per-region captions for split-screen or stacked layouts when one global region would cover important content.
- Require concise cue grouping: usually 2-7 words per beat, no more than two lines, no dense paragraph captions.
- Ask for a short rationale that mentions the visual conflict being avoided.

Schema changes:

- Add `caption_plan` to the strict JSON schema.
- Add enums for `mode`, `style_preset`, alignment, and quality level.
- Add region IDs and cue references.
- Keep `additionalProperties: false`.
- Increase composition max tokens if word-level cue plans make the response larger.

Repair behavior:

- If caption JSON is truncated, invalid, references missing transcript spans, overlaps impossible timing, or creates off-canvas regions, repair with the same structured-output retry mechanism.
- If repair fails, return a clipping error that names caption planning validation. Do not render an uncaptained clip as a fallback.

## Rendering Design

Render captions after the visual composition filter graph has produced the composed video stream.

Implementation approach:

- Generate an ASS subtitle file for each rendered plan segment.
- Convert normalized caption regions into output pixel positions.
- Use libass through ffmpeg so stroke, shadow, font, alignment, karaoke-like highlighting, and per-cue positioning are controllable.
- Feed the composed video stream into the ASS filter and map the resulting stream as final output.
- Keep audio mapping unchanged.
- For multi-plan or spliced clips, generate captions relative to each plan segment's local zero time before concatenation.

Caption style:

- Font: packaged or configured bold sans-serif with reliable deployment in Docker.
- Fill: white.
- Outline: thick black stroke scaled by output height.
- Shadow: subtle black shadow.
- Highlight: yellow or brand accent for current word/emphasis when word timings exist.
- Case: preserve source casing by default; allow style transforms only if the planner explicitly chooses a preset that defines it.
- Motion: start with ASS-supported pop/scale emphasis where it remains readable; avoid elaborate animation that makes ffmpeg rendering fragile.

If the font is missing, rendering should fail with a clear setup error. It should not silently switch to an unknown platform font that changes layout.

## Validation Rules

Add validation before rendering:

- `caption_plan` is present unless the user explicitly requested captions off.
- Every caption region has a unique ID and valid normalized rectangle.
- Region rectangles stay inside the output canvas and are large enough for the requested line count.
- Cue times are ordered, non-empty, and within the plan segment they apply to.
- Cue word or segment references exist and map to source-backed transcript text.
- Cue text assembled from references is not empty.
- Cues assigned to a region must fit the region's `max_lines` and planned text density.
- Caption regions must not exceed an agreed maximum count per plan.
- Caption `z_index` must render above video regions.
- If the planner chooses per-panel captions, every cue must name the target region.
- If captions are disabled, the plan must include a reason and confidence.

Keep validation strict. The model should learn the shape through repair prompts; the code should not rescue malformed intent with hard-coded placement.

## Tool And User-Facing Changes

Extend the YouTube clipping tool schema with:

- `captions`: enum such as `auto`, `on`, `off`.
- `caption_instructions`: natural language guidance from the user.

Routing guidance should tell the assistant:

- Use `captions=on` when the user asks for subtitles, captions, transcript overlay, or Opus-style clips.
- Use `captions=off` only when the user explicitly asks for no captions.
- Put placement/style requests in `caption_instructions`.
- Keep existing `layout_instructions` for camera/crop/composition guidance.

The clip result should include caption fields per rendered clip so the final assistant response can truthfully report that captions were rendered.

## Legacy Cleanup

During implementation, audit the existing `captions` visual region role. If it was meant to preserve source-burned captions as part of the video crop, rename it to a clearer source-region role. If it is unused after the new subtitle layer exists, remove it from the schema, validation, prompts, and tests in the same change. Do not leave the old ambiguous role beside the new caption plan.

## Implementation Slices

1. Add transcript word timing support.
   - Extend transcription parsing to retain word IDs, source times, and text.
   - Thread word timing through clip detection and composition inputs.
   - Add tests for chunk-offset word timing and transcript reference stability.

2. Extend clip request and tool schema.
   - Add caption mode and caption instructions to the request.
   - Update assistant tool descriptions so the LLM routes caption requests explicitly.
   - Update executor tests to verify caption arguments are passed through.

3. Extend composition planning schema and prompt.
   - Add `caption_plan`.
   - Include transcript word or segment references in planner input.
   - Add validation tests for valid bottom captions, per-panel captions, disabled captions, bad references, off-canvas regions, and missing plans.

4. Build ASS caption generation.
   - Convert caption regions and cues into ASS styles/events.
   - Escape ASS text safely.
   - Add unit tests for text assembly, line grouping, region positioning, and highlight timing.

5. Integrate ASS rendering into ffmpeg.
   - Apply subtitles after composition.
   - Preserve existing audio and concatenation behavior.
   - Add filter graph tests for captioned full-frame, single-crop, stacked, and overlay layouts.

6. Add metadata and result reporting.
   - Store caption mode, style preset, confidence, and reason on each rendered clip.
   - Include those fields in the tool result.

7. Remove or rename legacy caption region behavior.
   - Update prompts, schemas, validators, and tests so there is only one clear meaning for generated captions.

8. Verify without browser testing.
   - Run Go unit tests for planning validation, ASS generation, executor routing, and renderer filter construction.
   - Run a local ffmpeg smoke test with a synthetic source video if available in the normal test environment.

## Acceptance Criteria

- A normal vertical clip renders burned-in captions by default when caption mode is `auto` or `on`.
- A split-screen or stacked clip can render captions in multiple planned regions without covering the split or facecam.
- The planner can choose top, bottom, or per-panel caption regions based on thumbnails and layout.
- Invalid caption plans trigger structured repair and then fail clearly if still invalid.
- No uncaptained deterministic fallback is used when captions were required.
- Captions are readable on 9:16 and 16:9 outputs.
- Existing clip detection, visual composition, upload, and Discord result behavior still work.
- Legacy ambiguous caption-region code is removed or renamed.
