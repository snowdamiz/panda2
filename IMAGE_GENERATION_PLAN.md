# Panda Image Generation Plan

## Reader And Outcome

Reader: an engineer working on Panda's Discord assistant.

Post-read action: implement image generation so a Discord user can ask Panda for a meme, sprite sheet, icon, illustration, or similar visual asset and receive an uploaded image in Panda's response.

## Goal

Panda should generate and post images when the user's request calls for visual output. The model decides to use image generation through a real function tool. Do not add keyword, regex, or hard-coded intent fallback paths for phrases like "make a meme" or "sprite sheet"; if the model misses the tool call, fix the tool schema, tool availability, prompt instructions, or provider request format.

The first production slice should support text-to-image generation and posting the generated file back to the current Discord conversation. Image-to-image using user attachments can follow after the text-to-image path is stable.

## Current Architecture Fit

Panda already has the pieces this should extend:

- The assistant exposes model-callable tools through the tool registry and executor.
- Feature gates, Panda permissions, and role/tool access decide which tools the model sees.
- The router reserves usage before assistant work and commits or releases it after the response.
- Discord rendering already creates normal assistant responses, embeds, confirmations, and message chunks.
- Discord write tools exist, but current message writes are text-oriented and confirmed because they can post arbitrary content to channels.

The clean path is to treat image generation as an assistant response attachment, not as a generic Discord write. The user asked Panda to answer with an image, so Panda's own response should include that image without a second confirmation button. Cross-channel generated image posting can be a later confirmed write flow.

## Provider Choice

Use OpenRouter's dedicated Images API for the initial implementation because the app already uses OpenRouter configuration and request conventions. OpenRouter's current image API exposes:

- a dedicated image generation endpoint that accepts `model` and `prompt` and returns generated image data plus usage/cost metadata,
- image model discovery through an image models endpoint,
- per-model and per-endpoint capability records for supported parameters such as aspect ratio, quality, format, size, resolution, and streaming support.

Use Google's Nano Banana 2 model through OpenRouter for the first implementation. The initial configured/default OpenRouter image model should be `google/gemini-3.1-flash-image`, matching Gemini 3.1 Flash Image. Keep this as configuration, not a fallback chain. If product later wants Nano Banana Pro, switch the configured image model after validating availability, pricing, and supported parameters; do not silently route to another image model when Nano Banana 2 fails.

OpenRouter also exposes a beta chat server tool for image generation. Do not use that for the first slice. It returns model-mediated image URLs and leaves Panda with less control over binary upload, Discord file limits, provider usage accounting, and policy/error handling. Revisit it only after the direct image client exists.

## User Experience

When a user asks for an image, Panda should:

- call an image-generation tool with a concise visual prompt and explicit output settings,
- generate the image,
- upload the image as a Discord attachment in the current response,
- include a short text caption only when useful,
- return a clear, non-leaky error if generation fails or the request is blocked by policy.

Examples that should trigger the tool through the model:

- "create a meme for me about Monday standup"
- "make a pixel art sprite sheet for a red sports car"
- "draw an icon for our raid night"
- "make a transparent sticker of a wizard holding coffee"

Examples that should not silently fall back to text:

- If the tool is unavailable, Panda should say image generation is not enabled or configured.
- If the provider rejects the request, Panda should explain that it could not generate that image and ask for a safer revision.
- If requested dimensions or format are unsupported, Panda should ask for an adjusted request or retry only after the model supplies valid parameters.

## Public Capability And Access

Add a public "Image generation" feature that depends on assistant chat.

Recommended permissions:

- Discord permissions: view channel, send messages, attach files.
- Panda permission: a new optional assistant image-generation permission.
- Tool name: `panda.generate_image`.
- Feature gate: disabled unless the guild has selected or been granted image generation.
- Tool policy: available in assistive flows for users who can use assistant chat and image generation.
- Plan quota: consumes paid quota separately from text AI responses.

Image generation should not be exposed as arbitrary provider capability in user-facing capability summaries. Users should see "Panda can generate images when enabled", not provider names, model slugs, routing details, or costs.

## Tool Contract

Add one model-callable tool for the first slice:

`panda.generate_image`

Inputs:

- `prompt`: required. The model-written visual prompt to send to the image model.
- `caption`: optional. Short Discord text to accompany the file.
- `aspect_ratio`: optional. Prefer normalized values such as `1:1`, `16:9`, `9:16`, or `4:3`.
- `size`: optional. Explicit pixel size or provider-supported tier when configured.
- `quality`: optional. Provider-supported quality value.
- `output_format`: optional. `png`, `jpeg`, or `webp`.
- `transparent_background`: optional boolean, mapped only when the selected model supports transparent output.
- `count`: optional integer, initially capped at 1 unless product and billing explicitly allow multi-image responses.
- `filename_hint`: optional. Sanitized stem for the Discord attachment filename.

Outputs visible to the model:

- `generated`: boolean.
- `image_count`: integer.
- `filename`: sanitized filename.
- `caption`: caption that will be posted, if any.
- `provider_status`: success, policy_blocked, invalid_request, rate_limited, unavailable, or error.
- `user_message`: safe text the model may reuse in its final reply.

Outputs carried out-of-band by the executor:

- generated file bytes,
- MIME type,
- filename,
- alt text or caption,
- provider usage and cost metadata for billing/audit.

Do not put base64 image bytes in the LLM-visible tool message. Keep tool-message content compact and redacted.

## Image Client

Add a dedicated image generation client alongside the existing LLM client instead of overloading chat completions.

Responsibilities:

- Build the image API request from structured input.
- Use `google/gemini-3.1-flash-image` as the default configured image model unless deployment config overrides it.
- Validate requested options against configured or discovered model capabilities.
- Send OpenRouter auth and app attribution headers consistently with the existing client.
- Decode returned base64 image data.
- Preserve provider usage and cost metadata.
- Classify provider errors into stable application errors.
- Enforce request timeout, retry policy, circuit breaker behavior, and max generated byte limits.

No deterministic fallback model should be used. If the configured Nano Banana 2 model is missing, invalid, or unavailable, mark the tool unavailable or return a structured provider error.

## Response And Discord Upload Plumbing

Extend the internal assistant response shape to carry generated files from tool execution to Discord rendering.

Recommended flow:

1. The tool executor receives the model's image-generation tool call.
2. The image client returns image bytes and metadata.
3. The executor returns a normal LLM-visible tool message plus an out-of-band generated file payload.
4. The assistant accumulates generated files across tool rounds.
5. The command router includes generated files in the final response object.
6. Discord message rendering uploads those files as attachments in the same response message when possible.

Discord-specific requirements:

- Require Attach Files permission before advertising the tool.
- Respect Discord file size limits for the deployed bot/server tier.
- Sanitize filenames and force the extension to match the MIME type.
- Prefer one generated image per response in the first slice.
- If text must be chunked, send the image with the first meaningful response chunk.
- For interactions and queued natural-message jobs, preserve generated files in memory only long enough to send the response.

## Billing, Quotas, And Audit

Image generation needs separate accounting because image costs and abuse risk differ from text responses.

Implementation tasks:

- Add a billing metric for image generations.
- Add per-plan image-generation allowances.
- Reserve one image generation unit inside the image tool before provider spend.
- Commit the image reservation only after a successful generated file is available.
- Release the reservation on provider errors, policy blocks, invalid requests, or Discord upload failures.
- Record provider cost metadata when available.
- Add audit events for generated image attempts, success, policy block, and provider failure.
- Redact prompts in customer-facing support bundles unless a support mode explicitly allows safe summaries.

The existing AI response reservation should still cover the assistant turn. The image reservation covers the generated image itself.

## Safety And Abuse Handling

The tool should rely on provider policy enforcement and surface policy failures cleanly. It should not rewrite unsafe prompts into safe ones without the model explicitly asking the user for a safer version.

Add local controls for:

- maximum prompt length,
- maximum generated image count,
- maximum generated byte size,
- allowed output MIME types,
- optional guild/channel disablement through existing tool access controls,
- rate limits that account for the higher provider cost,
- logging that avoids raw prompt leakage by default.

For memes and images with text, the model should include the desired text in the visual prompt. Do not add a deterministic caption-rendering fallback that draws text locally when image generation fails.

## Implementation Slices

### Slice 1: Capability And Config

- Add image generation config fields for base URL, model, timeout, and max bytes.
- Default the image model to `google/gemini-3.1-flash-image` and expose an override such as `OPENROUTER_IMAGE_MODEL`.
- Add environment and JSON config support.
- Add config validation and health/status warnings.
- Add the public feature and Panda permission.
- Add Attach Files to the feature's Discord permission list.
- Add tests for feature expansion, permission names, and config loading.

### Slice 2: Image Client

- Implement the image client with request/response structs.
- Add model capability discovery helpers for the image models endpoint.
- Decode base64 image output and classify errors.
- Add usage/cost parsing.
- Add unit tests with a local HTTP server for successful generation, provider errors, invalid base64, unsupported parameters, and missing config.

### Slice 3: Tool Execution

- Add `panda.generate_image` to the registry.
- Add executor wiring for the image client.
- Add a generated-file payload to execution results.
- Ensure the tool is only advertised when the client is configured, the feature is enabled, the user has permission, and Discord attachment permission is present.
- Add unit tests for tool visibility, argument validation, no-byte leakage in tool messages, billing reservation behavior, and policy/error responses.

### Slice 4: Assistant And Router Response Files

- Extend assistant responses and command responses with generated file attachments.
- Accumulate tool-generated files across model rounds.
- Keep final assistant text concise when files are present.
- Ensure empty text plus a generated file is still treated as a valid assistant payload.
- Add tests for chat, natural-message, and background-task paths.

### Slice 5: Discord Upload

- Extend Discord response rendering to attach generated files.
- Add upload handling for initial interaction responses, follow-up/edit flows, and natural-message responses.
- Add file-size, MIME, and filename protections.
- Add tests using mocked Discord REST interfaces.

### Slice 6: Billing, Audit, And Rollout

- Add the image-generation billing metric and per-plan allowances.
- Record provider cost metadata.
- Add audit events and operator observability counters.
- Update setup/capability summaries so admins can enable and reason about the feature without seeing provider routing details.
- Update public docs and plan tables only after product pricing is decided.

## Tests

Minimum test coverage before shipping:

- Config loading and validation for image generation settings, including the Nano Banana 2 default model and override.
- Feature catalog expansion and Discord permission bitfield includes Attach Files.
- Admin permission allowlist accepts the new Panda permission.
- Tool registry advertises `panda.generate_image` only under the right feature, permission, and runtime conditions.
- Image client sends the expected OpenRouter image request and parses image bytes, usage, cost, and errors.
- Tool execution does not include base64 image bytes in LLM-visible messages.
- Billing reservation commits on successful generated file and releases on failures.
- Assistant responses with files are not treated as empty.
- Discord rendering uploads generated files with safe filenames and MIME types.
- Provider policy errors produce safe user-facing text.

Do not test this in the browser. Browser verification is explicitly left to the user.

## Acceptance Criteria

- A user with image generation enabled can ask for a meme or sprite sheet in natural Discord chat and receive an uploaded image.
- The model chooses image generation through `panda.generate_image`; there is no deterministic keyword fallback.
- If image generation is disabled, unconfigured, over quota, or blocked by policy, Panda returns a clear text response and does not pretend an image was created.
- Generated bytes are never exposed in LLM-visible tool messages, logs, or support bundles.
- Billing, audit, and usage records distinguish image generation from normal text AI responses.
- Existing text assistant, web search, attachment summarization, music, reminders, and Discord write tools keep their current behavior.

## References Verified 2026-06-25

- [OpenRouter Images API](https://openrouter.ai/docs/api/api-reference/images/create-images): dedicated image generation endpoint, base64 image response, and usage/cost metadata.
- [OpenRouter Image Generation Guide](https://openrouter.ai/docs/guides/overview/multimodal/image-generation): image model discovery and per-endpoint capabilities.
- OpenRouter model slug to use for the first implementation: `google/gemini-3.1-flash-image`.
- [Google Gemini Image Generation Docs](https://ai.google.dev/gemini-api/docs/image-generation): Nano Banana naming across Gemini image models, including Nano Banana, Nano Banana 2, and Nano Banana Pro.
- [OpenRouter Image-Generation Server Tool](https://openrouter.ai/docs/guides/features/server-tools/image-generation): beta, chat-tool-mediated image generation; intentionally deferred for the first slice.
