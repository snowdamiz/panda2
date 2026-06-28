# YouTube Aspect Ratio And Smart Crop Plan

## Reader And Outcome

Reader: an engineer working on Panda's YouTube clipping feature.

Post-read action: implement 16:9 and 9:16 clipping with model-planned visual composition, using sampled thumbnails from detected clips to decide what regions should be cropped, stacked, or preserved.

## Goal

Panda should create YouTube clips in either 16:9 or 9:16. The transcript clip detector still chooses the moments and may return continuous or spliced multi-cut clips. A new visual composition step then looks at thumbnails sampled from each detected clip/cut and returns a strict structured plan for the render.

This must not be a deterministic crop fallback. If a streamer has a facecam in a corner and primary content elsewhere, the visual model should identify the facecam source region and the primary content source region, decide how those regions map into the 9:16 canvas, and return output rectangles that exactly compose the target frame. If the model response is invalid, fix the request/schema/prompt or fail with a clear error. Do not silently center crop, corner crop, or fall back to the old source-aspect renderer.

## Current Architecture Fit

The current implementation is transcript-first:

- `internal/youtube/clip.go` resolves a YouTube URL, extracts audio chunks, transcribes them with timestamps, asks `clipDetector.Detect` for ranked clip segments, downloads the source video, renders each clip with ffmpeg, uploads the MP4 to R2, and returns watch links.
- `internal/youtube/clip_detector.go` already uses OpenRouter chat completions with strict JSON schema output for transcript-backed clip decisions.
- `renderVideoSegment` currently preserves the source visual framing. It cuts by time, maps video/audio, encodes H.264/AAC, and does not apply crop, pad, scale, stack, overlay, or target aspect filters.
- `internal/llm.Message.Content` is currently a string, so the app cannot yet send thumbnail image content blocks through the existing OpenRouter client.

The clean extension is:

1. Detect transcript-backed clips as today.
2. Download the source video as today.
3. Extract representative thumbnails for each detected clip.
4. Ask a visual composition model for a strict JSON render plan for the requested aspect ratio, including source crop rectangles and target output rectangles for every relevant region in each cut.
5. Render from that plan with ffmpeg.
6. Upload and return results as today, including aspect/layout metadata.

## Research Summary

Sources checked on 2026-06-28:

- Google Gemini model docs: <https://ai.google.dev/gemini-api/docs/models>
- Google Gemini structured output docs: <https://ai.google.dev/gemini-api/docs/structured-output>
- Google Gemini image understanding and object detection docs: <https://ai.google.dev/gemini-api/docs/image-understanding>
- Google Gemini video understanding docs: <https://ai.google.dev/gemini-api/docs/video-understanding>
- OpenRouter structured output docs: <https://openrouter.ai/docs/features/structured-outputs>
- OpenRouter live model catalog: <https://openrouter.ai/api/v1/models>

Relevant findings:

- Google's current Gemini docs list Gemini 3.5 Flash as the stable production Flash model and describe Gemini models as multimodal.
- Gemini supports JSON-schema structured output, which matches Panda's existing strict-response pattern.
- Gemini image understanding docs include object detection and spatial reasoning guidance, which is directly relevant to finding facecams, screen content, speakers, captions, and other regions in thumbnails.
- Google video understanding is available, but direct video analysis samples frames at model-defined intervals. For this feature, extracting specific thumbnails around the actual transcript-selected clip boundaries is more controllable and cheaper.
- OpenRouter's live catalog currently exposes `google/gemini-3.5-flash` with `text`, `image`, `video`, `file`, and `audio` inputs and `structured_outputs`.
- OpenRouter's live catalog currently exposes `openai/gpt-5.4-mini` with text/image/file inputs and `structured_outputs`. It is a good lower-cost candidate for the existing text-only transcript clip detector.

## Model Recommendation

Use two configured model roles:

- Visual composition planner: `google/gemini-3.5-flash`
- Transcript clip detector: `openai/gpt-5.4-mini`, or keep the existing configured clip detector model if production already has a known-good choice

Why Gemini 3.5 Flash for visual composition:

- It is multimodal and accepts the exact thumbnail frames Panda extracts.
- It supports structured output through OpenRouter, which lets Panda reject malformed crop/layout plans instead of guessing.
- It is strong for spatial image understanding and object/layout detection, which is the actual problem here.
- It has enough context to receive several thumbnails, clip metadata, transcript snippets, and schema instructions in one request.

Why not an image generation model:

- This feature does not need generated pixels. Panda already has the source video and ffmpeg. The model should decide regions and layout; ffmpeg should render the actual clip.

Why not a local-only detector as the main decision maker:

- Face/person detectors can help, but they do not understand streaming layouts, game/UI content, slides, captions, webcam placement, or what should be prioritized for the requested aspect ratio. If added later, local CV should produce observations for the model, not bypass the model's composition decision.

Operational option:

- If keeping one model is more valuable than the lower text cost of `openai/gpt-5.4-mini`, use `google/gemini-3.5-flash` for both transcript detection and visual composition. Keep this a configuration choice, not an automatic fallback chain.

## User-Facing Tool Contract

Extend `panda.clip_youtube` with:

- `aspect_ratio`: optional enum: `auto`, `16:9`, `9:16`
- `layout_instructions`: optional string for user requests such as "keep the webcam visible" or "focus on gameplay"

Behavior:

- If the user explicitly asks for vertical, shorts, TikTok, Reels, phone, or 9:16, the assistant should pass `aspect_ratio="9:16"`.
- If the user explicitly asks for widescreen, YouTube, landscape, or 16:9, pass `aspect_ratio="16:9"`.
- If the user does not specify, pass `aspect_ratio="auto"` and let the visual composition planner choose `16:9` or `9:16` in its structured response.
- The executor should not infer a hard-coded aspect from keywords. Tool routing and argument selection belong to the assistant/tool schema path.

## New Internal Interfaces

Add a visual planner beside the transcript detector:

```go
type ClipCompositionPlanner interface {
    Configured() bool
    Plan(ctx context.Context, request ClipCompositionRequest) (ClipCompositionResult, error)
}
```

Recommended request shape:

```go
type ClipCompositionRequest struct {
    Title              string
    URL                string
    Uploader           string
    SourceAspectHint   string
    RequestedAspect    string
    LayoutInstructions string
    Clip               ClipDecision
    Thumbnails         []ClipThumbnail
}

type ClipThumbnail struct {
    ID                  string
    SourceSeconds       float64
    ClipSegmentIndex    int
    ClipOffsetSeconds   float64
    Width               int
    Height              int
    MIMEType            string
    Data                []byte
    TranscriptNearFrame string
}
```

Recommended response shape:

```go
type ClipCompositionResult struct {
    AspectRatio string                `json:"aspect_ratio"`
    LayoutMode  string                `json:"layout_mode"`
    Plans       []ClipFrameRenderPlan `json:"plans"`
    Confidence  float64               `json:"confidence"`
    Reason      string                `json:"reason"`
}

type ClipFrameRenderPlan struct {
    AppliesToSegmentIndex int                `json:"applies_to_segment_index"`
    SourceStartSeconds    float64            `json:"source_start_seconds"`
    SourceEndSeconds      float64            `json:"source_end_seconds"`
    Regions               []ClipRenderRegion `json:"regions"`
}

type ClipRenderRegion struct {
    Role       string   `json:"role"`
    SourceRect ClipRect `json:"source_rect"`
    OutputRect ClipRect `json:"output_rect"`
    Fit        string   `json:"fit"`
    ZIndex     int      `json:"z_index"`
}

type ClipRect struct {
    X int `json:"x"`
    Y int `json:"y"`
    W int `json:"w"`
    H int `json:"h"`
}
```

Use normalized coordinates from 0 to 1000. `source_rect` is the crop box in the original video frame. `output_rect` is the destination rectangle on the final 16:9 or 9:16 canvas. The model should never return ffmpeg filter strings. Panda validates the plan and constructs the filters itself.

Recommended region roles:

- `primary_content`: the gameplay, screen share, slides, product view, or other main content the clip is about.
- `facecam`: a webcam/person reaction box that should be preserved separately from the main content when useful.
- `speaker`: a full-size talking head or host region that is not a small facecam.
- `captions`: burned-in captions or important on-screen text when they need separate preservation.
- `full_frame`: the complete source frame for intentional full-frame layouts.

Supported `layout_mode` values for the first implementation:

- `full_frame`: preserve source frame, scaling/padding only to target aspect.
- `single_crop`: one model-selected crop region fills the output.
- `stacked_regions`: two or three regions, usually primary content plus facecam, tiled into the target aspect with output rectangles that exactly cover the canvas.
- `content_with_face_overlay`: primary content fills the output and a face/person region is overlaid using `output_rect` and `z_index`.

Do not add layout modes that cannot be rendered and tested immediately.

For the streamer example, a valid 9:16 `stacked_regions` plan should contain at least:

- one `primary_content` region with a source crop around the gameplay/window/screen content and an output rectangle such as `x=0,y=0,w=1000,h=700`,
- one `facecam` region with a source crop around the webcam and an output rectangle such as `x=0,y=700,w=1000,h=300`,
- rectangles whose normalized output heights add to exactly `1000` and widths fill exactly `1000`, so the rendered `1080x1920` canvas has no gaps, overlap, or accidental source-aspect padding.

## Thumbnail Sampling Strategy

After source download and before rendering, extract thumbnails with ffmpeg.

For each detected clip:

- For continuous clips, sample start plus 1 second, midpoint, and end minus 1 second.
- For clips longer than 30 seconds, add quarter and three-quarter samples.
- For spliced clips, sample each segment near its start and midpoint, capped by a configured max thumbnail count.
- Clamp sample times inside the segment boundaries.
- Encode thumbnails as JPEG, max edge around 720px, quality around 75.
- Include the nearest transcript text so the model knows whether the frame is hook, setup, reaction, or payoff.

Sampling is deterministic data collection, not a rendering fallback. The model still owns the composition choice.

## LLM Client Changes

Extend `internal/llm.Message` to support multimodal content while preserving the existing string-only path.

Recommended approach:

- Keep `Content string` for existing chat callers.
- Add optional content parts, such as `ContentParts []ContentPart`.
- Implement custom JSON marshaling so `content` is either the existing string or an array of OpenAI-compatible blocks.
- Add `ContentPart{Type:"text"}` and `ContentPart{Type:"image_url"}` with data URLs for local thumbnails.
- Keep response parsing unchanged because planner output is still text JSON.

This is request formatting work. Do not work around missing image support by uploading thumbnails somewhere public unless that becomes a deliberate storage design.

## Visual Planner Prompt Contract

The system prompt should tell the planner:

- You are choosing render composition for short-form video.
- Use only the supplied thumbnails and metadata.
- Return strict JSON matching the schema.
- Prefer watchability over maximizing every source pixel.
- For 9:16, consider whether the main content, facecam, captions, or speaker should be isolated, stacked, or overlaid.
- For streamer layouts, identify separate `primary_content` and `facecam` regions when both are visible, then map both into the 9:16 canvas when that is more watchable than a single crop.
- For stacked 9:16 layouts, output rectangles must tile the final canvas exactly: no gaps, no overlaps, and normalized dimensions that add to the full `1000x1000` output coordinate space.
- Preserve important text/UI when it is central to understanding the clip.
- Do not invent regions that are not visible in the thumbnails.
- If the source layout changes between sampled frames, return separate plans by segment/time range. A spliced clip may therefore have multiple cuts, and each cut may have multiple source regions.
- Choose `full_frame` only when it is intentionally best for the requested aspect.

The user prompt should include:

- Video title/uploader.
- Requested aspect ratio or `auto`.
- User layout instructions.
- Clip title, type, reason, scores, and transcript snippets.
- Thumbnail IDs with timestamps and nearby transcript.
- A reminder that `source_rect` and `output_rect` coordinates are normalized 0..1000, and that `output_rect` describes placement on the final 16:9 or 9:16 canvas.

Use temperature around `0.2`. This is not creative writing; the model should be precise and repeatable.

## Validation And Repair

Validate every composition response before rendering:

- `aspect_ratio` is `16:9` or `9:16`.
- `layout_mode` is supported.
- `confidence` is 0..1.
- Every plan maps to an existing clip segment/time range.
- Every plan covers only time inside its clip segment, and plans for the same segment are ordered and non-overlapping.
- Every clip segment has full time coverage by one or more plans. For a 30 second source segment, the union of plan time ranges must cover that segment from start to end without gaps.
- Every region has a known role and both `source_rect` and `output_rect` stay within 0..1000.
- Region source and output width/height are above a minimum usable size.
- Required regions exist for the selected layout mode.
- Region count is sane.
- `fit` is a supported enum such as `cover` or `contain`; stretching source pixels is not allowed.
- For `single_crop`, exactly one region exists and its `output_rect` fills the whole canvas: `x=0,y=0,w=1000,h=1000`.
- For `full_frame`, exactly one region exists, its `source_rect` is the full source frame, and its `output_rect` fills the whole canvas.
- For `stacked_regions`, regions must tile the full output canvas exactly. For the first vertical implementation, require `x=0,w=1000` on every region, `y` values that touch without gaps, and a final bottom edge of `1000`.
- For 9:16 streamer layouts with both facecam and main content visible in thumbnails, `stacked_regions` must include `primary_content` and `facecam`; a single crop is valid only when the model explains why one region is genuinely better.
- For `content_with_face_overlay`, `primary_content` must fill the whole canvas and overlay regions must have higher `z_index`, nonzero output size, and bounded overlap.
- After converting normalized `output_rect` values to target pixels, integer rounding must still produce rectangles that exactly fill the `1080x1920` or `1920x1080` frame for tiled layouts. The renderer may adjust only rounding residue, not the model's chosen layout.

If validation fails:

1. Retry the same model once with the validation errors and the original thumbnail/request context.
2. If it still fails, fail that clip with a clear composition error.

Do not render a center crop, full-frame output, or source-aspect output unless the model returned a valid plan asking for that.

## Rendering Strategy

Replace the old source-aspect render path with plan-based rendering. The old behavior becomes `layout_mode="full_frame"` in a valid model plan, not a separate legacy path.

Rendering rules:

- Target `16:9`: default resolution `1920x1080`.
- Target `9:16`: default resolution `1080x1920`.
- Enforce even dimensions for H.264.
- Always finish with `setsar=1`.
- Continue using H.264/AAC and `+faststart`.

Filter directions:

- `full_frame`: scale to fit target and pad if needed.
- `single_crop`: crop `source_rect`, scale to the full-frame `output_rect`, and fill the target canvas.
- `stacked_regions`: split the video stream once per region, crop each `source_rect`, scale each crop to its validated `output_rect`, and compose those outputs on a blank target canvas. For the common 9:16 streamer case, this produces an exact `1080x1920` frame such as content occupying the top 70 percent and facecam occupying the bottom 30 percent.
- `content_with_face_overlay`: crop/scale primary content into the full-frame `output_rect`, crop/scale facecam or speaker regions into their overlay `output_rect`, and overlay by ascending `z_index`.

For spliced clips:

- Render each clip segment/cut with the composition plan that applies to that segment. A single cut may use multiple regions, such as `primary_content` and `facecam`, and a spliced clip may have different region boxes for each source cut.
- If a segment has multiple plan time ranges because the source layout changes mid-cut, render those ranges separately with their matching region maps before concatenating them back into the clip flow.
- Normalize all segment outputs to the same dimensions, frame rate policy, video codec, audio codec, and pixel format.
- Concatenate with the existing concat path.

Pixel conversion details:

- Convert normalized source rectangles against the probed source width/height.
- Convert normalized output rectangles against the exact target resolution.
- For tiled layouts, assign integer pixel edges from normalized boundaries, then force the final right/bottom edge to the target width/height after validation. This absorbs rounding residue only; it must not create a new layout.
- For the 9:16 stacked streamer layout, verify final pixel rectangles sum to `1080x1920`, such as `1080x1344` content plus `1080x576` facecam.

## Result Metadata

Add aspect/composition metadata to `RenderedClip`:

- `AspectRatio`
- `LayoutMode`
- `CompositionReason`
- `CompositionConfidence`

These fields help debug why a vertical clip chose stacked layout instead of a crop.

Do not expose provider names, raw thumbnail bytes, or prompt content to normal users.

## Configuration

Add:

- `OPENROUTER_CLIP_COMPOSITION_MODEL`
- `OPENROUTER_CLIP_COMPOSITION_TIMEOUT`
- `YOUTUBE_CLIP_THUMBNAIL_MAX_COUNT`
- `YOUTUBE_CLIP_THUMBNAIL_MAX_EDGE`
- `YOUTUBE_CLIP_VERTICAL_RESOLUTION`
- `YOUTUBE_CLIP_LANDSCAPE_RESOLUTION`

Recommended defaults:

- `OPENROUTER_CLIP_COMPOSITION_MODEL=google/gemini-3.5-flash`
- `OPENROUTER_CLIP_COMPOSITION_TIMEOUT=2m`
- `YOUTUBE_CLIP_THUMBNAIL_MAX_COUNT=8`
- `YOUTUBE_CLIP_THUMBNAIL_MAX_EDGE=720`
- `YOUTUBE_CLIP_VERTICAL_RESOLUTION=1080x1920`
- `YOUTUBE_CLIP_LANDSCAPE_RESOLUTION=1920x1080`

If the composition model is not configured, YouTube clipping should be considered not fully configured for aspect-aware clipping. Do not silently use the transcript detector or the default chat model as a fallback composition model.

## Implementation Slices

### Slice 1: Data Contracts And Config

- Add aspect ratio to `ClipRequest`, tool schema, and result metadata.
- Add visual composition config fields and env/file parsing.
- Add `ClipCompositionPlanner` interface and model-backed implementation shell.
- Add tests for config/env parsing and tool schema.

### Slice 2: Multimodal OpenRouter Requests

- Add message content parts to `internal/llm`.
- Preserve all existing text-only chat behavior.
- Add unit tests proving image content blocks serialize correctly with `response_format` and provider parameter requirements.

### Slice 3: Thumbnail Extraction

- Add ffmpeg thumbnail extraction helpers.
- Sample thumbnails from detected clip segments.
- Decode thumbnail dimensions with Go image libraries.
- Add tests using fake ffmpeg commands that assert sample timestamps and output paths.

### Slice 4: Composition Planner

- Implement strict schema, prompt, validation, and one repair retry.
- Add fake planner tests for valid single crop, stacked streamer regions, invalid boxes, unsupported layout modes, incomplete output tiling, missing facecam/content roles, and failed repair.
- Ensure invalid planner output fails instead of rendering a deterministic fallback.

### Slice 5: Plan-Based Renderer

- Replace `renderVideoSegment` with a plan-aware renderer.
- Build ffmpeg filter args for `full_frame`, `single_crop`, `stacked_regions`, and `content_with_face_overlay`.
- Keep concat behavior, but require normalized segment outputs.
- Add tests for generated ffmpeg arguments and error handling.

### Slice 6: End-To-End Service Wiring

- Wire the planner into `youtube.Service`.
- Move source video download before composition planning if needed.
- For each detected clip, sample thumbnails, plan composition, render, upload, and return metadata.
- Update existing YouTube clipping tests to cover requested `9:16`, requested `16:9`, and `auto`.

### Slice 7: Cleanup

- Confirm the old source-aspect render path is legacy.
- Remove it as a separate path once `full_frame` plan rendering exists.
- Keep only shared helpers that the new renderer still uses.

## Testing

Do not test in the browser.

Use:

- Go unit tests for schema, config, validation, planner repair, and ffmpeg args.
- Existing service-style tests with fake `yt-dlp`, fake `ffmpeg`, fake transcript detector, fake visual planner, and fake uploader.
- Golden-ish assertions for ffmpeg filter graphs where helpful, but avoid brittle ordering beyond the contract the code owns.

High-value cases:

- User asks for 9:16 and planner returns stacked facecam/content layout whose normalized output rectangles exactly tile `1000x1000`.
- The 9:16 streamer case converts to exact pixels, such as primary content `1080x1344` plus facecam `1080x576`, with no output gap or overlap.
- A spliced clip has two source cuts and each cut has separate `primary_content` and `facecam` source rectangles.
- A continuous clip has one source segment but two visual plans because the facecam moves mid-clip.
- User asks for 16:9 and planner returns full-frame layout.
- User omits aspect and planner chooses 9:16.
- Planner returns malformed JSON.
- Planner returns valid JSON with invalid crop boxes.
- Planner returns stacked regions whose heights add to 950 or 1050; validation retries the model and then fails if repair is invalid.
- Planner repair succeeds.
- Planner repair fails and the clip fails without fallback rendering.
- Spliced clip renders each segment to the same dimensions before concat.

## Rollout

1. Land behind config so existing deployments remain unconfigured until `OPENROUTER_CLIP_COMPOSITION_MODEL` is set.
2. Enable in a staging guild with a small set of known video fixtures.
3. Compare resulting clip metadata and output sizes.
4. Watch OpenRouter model errors, schema failures, thumbnail counts, and ffmpeg failures.
5. Only then make aspect-aware clipping part of the normal YouTube clipping configuration checklist.

## Open Questions

- Should `auto` default toward 9:16 for short-form social clips, or should the visual planner freely choose either aspect based on content?
- Should Panda render both 16:9 and 9:16 variants when the user asks generally for clips?
- Should local CV observations include candidate facecam/content boxes to help the model, while still requiring the model to return the final `source_rect` and `output_rect` plan?
- Should local CV observations be added later to tell the model likely face/person/text regions before it decides layout?
