# Composed Tools Improvement Plan

## Reader And Outcome

Reader: an engineer working on Panda's Discord assistant.

Post-read action: make composed tools safer, easier to approve, easier to expose to the right users, and easier to debug after they run.

## Goal

Composed tools should feel like trustworthy server automations, not opaque generated snippets. An admin should be able to describe a workflow, inspect exactly what Panda drafted, approve it with a clear risk summary, decide who can use it, simulate it with realistic input, and diagnose failures without reading database rows or raw JSON.

The highest-value change is to turn composed tools into a managed lifecycle:

- draft with strict structured output,
- validate and lint with actionable findings,
- approve only through explicit confirmation,
- expose deliberately after approval,
- run with a narrow approved capability envelope,
- show health, run transcripts, and drift warnings when things change.

Do not add keyword, regex, or hard-coded intent fallbacks. If the model misses a composed-tool draft or emits invalid structure, fix the tool schema, request format, prompt, structured-output contract, or validation loop.

## Non-Goals

- Browser testing by the implementer. Unit and integration tests are enough; the user will do browser checks.
- Adding broad new Discord write tools to composed execution.
- Making message-create automations fire on every normal chat message.
- Letting model-generated specs bypass Panda permissions, feature gates, billing gates, or Discord permission checks.
- Keeping legacy draft or job surfaces around after they are confirmed unused.

## Current Shape

Panda already has a strong foundation:

- A model-callable management tool can preview, draft, list, show, approve, run, simulate, export, pause, resume, disable, archive, delete, and roll back composed tools.
- Drafts are versioned, approved versions are immutable, and rollback points at a previously approved version.
- Approved tools are exposed as dynamic model tools only when feature gates, invocation mode, tool access, and permissions allow them.
- Runtime execution records runs, validates input and output schemas, applies cooldowns, applies per-hour limits, dedupes event triggers, audits invocations, and auto-pauses after repeated failures.
- Event-triggered and scheduled runs reuse the normal queue and scheduler systems.
- Validation already blocks owner-only native tools, unsupported Discord writes, broad mentions, unsupported event types, overly broad voice triggers, and legacy model-routing fields.

The weak spots are concentrated in lifecycle safety, model-output discipline, approval clarity, access propagation, schedule controls, and diagnostics.

## Audit Findings

### Permanent Delete Is Too Easy

The management action for permanent delete currently mutates immediately when it is not a dry run. Confirmation copy for delete exists elsewhere, but the composed management flow does not use it and the confirmation router does not complete a composed delete action.

Impact:

- A natural-language request such as "remove this tool" can become a hard delete through a model tool call.
- Deleting removes versions, runs, dedupe rows, and the tool record, so the blast radius is much larger than archive.

Fix direction:

- Treat archive as the default reversible removal action.
- Require a confirmation button for permanent delete.
- Add the delete button action to the confirmation router.
- Keep hard delete available only when the user explicitly asks for permanent deletion.

### Drafting Still Has Hidden Deterministic Behavior

Natural-language drafting has a hidden branch that string-matches policy or moderator-note requests and returns a fixed spec instead of going through the model draft path. Spec normalization also silently fills important missing fields.

Impact:

- This conflicts with the project rule that model/request formatting and structured output issues must be fixed upstream instead of bypassing LLM decisions.
- Tests currently lock in the hidden branch, so future work may accidentally preserve it.
- Silent field completion can make an invalid or underspecified model response look intentional.

Fix direction:

- Remove the hidden policy/mod-note draft branch after confirming it is legacy.
- Convert moderator-note creation into either a normal composed-tool draft through structured output or an explicit native capability.
- Make required spec fields fail validation instead of being silently invented.
- Keep harmless canonicalization, such as trimming names and clamping impossible negative limits, but do not infer workflow intent.

### Approval UX Hides The Important Parts

Approval currently returns a terse success message with the risk level. That is not enough for server automation. Admins need to see the trigger, targets, native tools, writes, unattended execution modes, rate limits, dedupe window, and required Discord permissions before approval.

Impact:

- Admins can approve powerful workflows without a readable capability envelope.
- "Risk: high" is accurate but not actionable.
- The approval step does not naturally explain what to do after approval.

Fix direction:

- Return a structured approval preview for every draft and approval confirmation.
- Show human-readable trigger and target summaries.
- Show native tool allowlist, composed-tool dependencies, write actions, and Discord permission requirements.
- Show safety controls: approval required, write confirmation behavior, max nested depth, cooldown, hourly limit, dedupe window.
- Show exposure state after approval: enabled but private, enabled for specific roles/users, or generally callable.

### Approval Does Not Mean Exposure

Approved dynamic tools are still gated by explicit composed-tool access when the server requires it. That is a good default, but the workflow does not explain it clearly.

Impact:

- Admins may approve a tool and then think it is broken because nobody can call it.
- The next action is split across composed-tool management and tool-access management.

Fix direction:

- After approval, show "enabled but not exposed" when no caller group can use it.
- Offer next actions in the model-readable result: keep private, allow admins, allow a role, allow a user, or open to everyone if policy permits.
- Add a dedicated "exposure" field to show/list results so Panda can answer "who can run this?" without guessing.

### Denied Tool Rules Do Not Fully Reach Assistant Execution

The command router computes denied tool rules, but the assistant request and executor context carry allowed and restricted tools without a denied-tools set.

Impact:

- Natural assistant tool execution can miss explicit deny rules even when router-level access calculated them.
- Composed dynamic tools are partly protected by explicit allow rules, but management tools and native tools share the same access machinery and should honor deny consistently.

Fix direction:

- Carry denied tools through assistant request structs, tool execution context, and tool access construction.
- Add tests proving denied native tools, denied composed tools, and denied management tools are hidden and cannot execute.

### Scheduled Runs Need Backpressure

The scheduler can parse very small recurring intervals, and composed runs that are rate-limited or deduped still create run records. That can churn the queue and database.

Impact:

- A bad or accidental schedule can repeatedly wake up and produce skipped runs.
- Rate-limited composed tools can still generate operational noise.
- Past or too-near schedules can create confusing behavior.

Fix direction:

- Enforce minimum delay for first run and minimum recurrence for composed schedules.
- Reject or normalize past run times.
- Make rate-limited, deduped, and blocked schedule results visible in schedule state.
- Add backoff for repeated skipped composed schedule executions.
- Keep per-guild schedule capacity and billing gates in place.

### Runtime Drift Is Hard To See

A composed spec can become unhealthy after approval because features are disabled, native tools are removed or hidden, Discord channel or role references no longer resolve, permissions change, or nested dependencies form a cycle. Some dynamic listing paths silently skip invalid tools.

Impact:

- Admins see "not available" behavior without a reason.
- Event and schedule automations can stop working after configuration drift.

Fix direction:

- Add a composed-tool health check used by list/show/approve/run.
- Return health states such as healthy, hidden_by_access, feature_disabled, invalid_spec, missing_native_tool, unresolved_discord_target, cyclic_dependency, rate_limited, and paused_after_failures.
- Show drift warnings in list/show and audit events.
- Revalidate before run and record a blocked run with a clear health reason.

### Run Debugging Is Too Thin

Run records store input, output, transcript, status, and error, but show/list surfaces only expose a small recent-run summary. This makes simulation and failure diagnosis much harder than it needs to be.

Impact:

- Admins cannot easily see which step failed.
- The stored transcript is not available through a safe, redacted admin UX.
- Iterating on a draft requires exporting JSON rather than using a normal preview or lint loop.

Fix direction:

- Add a run detail action that returns redacted input, output, transcript, version, timing, and step errors.
- Add a compare action for two versions.
- Add a lint action that can run without saving.
- Improve simulate so it validates sample input, renders templates, shows the native calls it would make, and never posts Discord writes.

### Some Surfaces Look Unused Or Legacy

A composed run job kind and handler exist, but current app wiring registers event jobs and scheduler jobs only. No enqueue path was found for the standalone composed run job.

Impact:

- Unused lifecycle surfaces make future maintenance riskier.
- A future engineer may wire the path without understanding why it was dormant.

Fix direction:

- Confirm whether standalone composed run jobs are legacy.
- Remove them if unused.
- If they are needed, wire them deliberately with tests, metrics, and clear enqueue ownership.

### Retention And Local Hardening Need A Policy

Composed runs can store user IDs, Discord content, rendered message bodies, and tool outputs. Transcript arguments are partly redacted, but the retention and local file-hardening story is not explicit.

Impact:

- Debug value is good, but long-lived run content can become a privacy burden.
- Local database, WAL, and SHM files should be owner-readable only when possible.

Fix direction:

- Define retention for composed run inputs, outputs, and transcripts.
- Redact or truncate sensitive fields before persistence, not only before model-visible messages.
- Tighten local database file permissions where the storage layer can do so safely.
- Include composed run data in export/delete support expectations.

## Target Admin Experience

### Draft

An admin asks Panda to create a composed tool. Panda calls the management tool and receives a structured draft preview:

- purpose,
- invocation modes,
- event trigger and filters,
- target channel, role, user, or voice channel,
- native tools and required permissions,
- write actions,
- sample input shape,
- safety limits,
- warnings and errors,
- whether approval is available.

If the request asks for unsupported message-create behavior, Panda should explain the limitation and suggest supported alternatives through the structured result. It should not silently invent a message-create automation and it should not use keyword fallbacks.

### Approve

The approval button preview should show the same capability envelope in human language. After approval, Panda should say whether the tool is enabled but private or exposed to specific users/roles.

### Expose

Panda should be able to answer:

- "Who can run this?"
- "Let mods use this tool."
- "Keep this private."
- "Open this to everyone."

This should reuse the existing tool-access system instead of inventing composed-tool-specific access rules.

### Simulate

Before a tool runs for real, an admin can simulate:

- chat invocation input,
- scheduled invocation input,
- event payload input,
- template rendering,
- native step arguments,
- nested composed-tool calls.

Simulation should never post writes. It should return a redacted transcript and warnings about what would have happened.

### Diagnose

Panda should be able to answer:

- "Why did this tool not run?"
- "Show the last failed run."
- "What changed between version 2 and version 3?"
- "Why is this not visible to users?"
- "Why did the schedule stop?"

The answer should come from composed health, run detail, schedule state, and audit records.

## Implementation Slices

### Slice 1: Lifecycle Safety

Implement first because it fixes the largest risk.

- Make permanent delete return a confirmation-required payload.
- Add composed-tool delete to the confirmation token encoder, decoder, copy, feature mapping, and router handler.
- Prefer archive for natural "remove" language unless the user explicitly asks for permanent delete.
- Keep archive immediate only if existing permission policy allows it; otherwise consider confirmation for archive too.
- Add tests for dry-run delete, natural delete, button delete, and archive-vs-delete wording.

Acceptance:

- A non-confirmed model call cannot hard-delete a composed tool.
- Approval, rollback, and delete all use a consistent confirmation path.
- Existing hard-delete repository behavior remains available only behind the confirmed action.

### Slice 2: No Hidden Draft Fallbacks

Remove model-bypassing draft behavior.

- Delete the hard-coded policy/moderator-note draft branch after confirming it is legacy.
- Replace the existing test that expects that branch with a test proving the LLM draft path is called.
- Use a strict structured-output path for composed specs. If the current provider client lacks a response-format contract, model the draft as a required tool/function call that returns one spec object.
- Reject prose-wrapped JSON instead of extracting the first JSON object from arbitrary text.
- Fail validation for missing required intent-bearing fields instead of silently selecting runner behavior.
- Preserve safe normalization that does not infer intent: trimming names, lowercasing enum-like fields, and clamping invalid negative numeric safety values.

Acceptance:

- Natural-language draft output always comes from structured model output or explicit `spec_json`.
- Invalid model structure returns validation errors and repair guidance.
- No keyword or regex branch creates a composed spec.

### Slice 3: First-Class Lint And Health

Make validation actionable and reusable.

- Split validation into parse, schema validation, capability validation, invocation validation, safety validation, dependency graph validation, and health checks.
- Return stable issue codes alongside human text.
- Include severity: error, warning, info.
- Include suggested fix text that Panda can summarize.
- Add health states for hidden, blocked, invalid, drifted, paused, rate-limited, and healthy tools.
- Use the same health check in draft preview, approval, list, show, dynamic advertisement, run, and schedule creation.

Acceptance:

- A tool skipped from model advertisement has a visible reason in show/list.
- Failed runs record a health reason when blocked before execution.
- Tests cover missing native tools, cyclic dependencies, disabled features, unresolved references, and explicit access hiding.

### Slice 4: Approval And Exposure UX

Make the admin approval step understandable.

- Add a structured approval summary object to draft and approval confirmation results.
- Include trigger summary, target summary, native tools, write actions, Discord permission requirements, safety limits, and risk reasons.
- Add exposure summary to list/show/approve results.
- Add recommended next actions after approval: keep private, allow role, allow user, open to everyone, schedule.
- Reuse existing tool-access management for exposure changes.

Acceptance:

- Approval UI has enough information for an admin to understand what will run, when, and with which capabilities.
- After approval, Panda can explain why regular users can or cannot see the tool.
- Tests assert approval summary content for event, scheduled, write, and read-only composed tools.

### Slice 5: Narrow Runtime Capability Envelope

Reduce the amount of ambient power granted to approved specs.

- Compute the exact native tool definitions used by the spec.
- Build runtime access from those definitions instead of granting a broad static set of admin and moderation permissions.
- Require explicit approval-time visibility for unattended event or schedule workflows that use admin read/write classes.
- Keep owner operations blocked.
- Keep unsupported Discord writes blocked until each write has a composed-safe execution policy.
- Carry denied tool rules through assistant request, execution context, and tool access.

Acceptance:

- Runtime access contains only permissions needed by the approved native tools.
- Explicit denied tool rules hide and block matching native, management, and composed tools.
- Event and scheduled tools cannot gain broader admin capability just because a broad approved access envelope exists.

### Slice 6: Safer Scheduling

Prevent noisy or runaway schedules.

- Enforce minimum first-run delay for composed schedules.
- Enforce minimum recurrence interval for composed schedules.
- Reject past times or require an explicit reschedule target.
- Record skipped, deduped, blocked, and rate-limited schedule results in schedule state.
- Add exponential or bounded backoff for repeated skipped runs.
- Surface backoff state in schedule list/show results.

Acceptance:

- A composed schedule cannot run every second.
- Repeated rate-limited or blocked executions do not churn continuously.
- Schedule list tells admins why a scheduled composed tool is not firing.

### Slice 7: Simulation And Run Diagnostics

Make iteration practical.

- Add run detail with redacted input, output, transcript, timings, version, trigger, and error.
- Add version compare for specs and validation reports.
- Improve simulate so it renders templates and resolves native-step arguments without posting writes.
- Support event simulation using the same input mapping and filters used by real events.
- Include nested composed run detail links or IDs in transcript entries.

Acceptance:

- An admin can inspect the last failed run and see the failing step without exporting raw JSON.
- Simulate produces a useful transcript for deterministic, agentic, hybrid, and nested runs.
- Dry-run behavior is tested for Discord message sends and music steps.

### Slice 8: Observability, Retention, And Cleanup

Make the system easier to operate.

- Add metrics for composed draft count, approval count, run count, run duration, blocked health reason, auto-pause count, schedule skips, and event dedupe.
- Add audit metadata for approval summary, exposure changes, delete confirmations, and auto-pauses.
- Define retention for run inputs, outputs, transcripts, and dedupe records.
- Redact and truncate sensitive run persistence fields consistently.
- Tighten local database file permissions where feasible.
- Confirm whether the standalone composed run job path is legacy. Remove it if unused, or wire it intentionally with tests and app registration.

Acceptance:

- Operators can see whether composed tools are healthy from metrics and audit events.
- Privacy-sensitive run data has an explicit retention policy.
- Confirmed legacy surfaces are removed rather than left dormant.

## Test Plan

Add focused tests at the service, executor, router, scheduler, and assistant layers:

- Delete requires confirmation and does not mutate before button confirmation.
- Delete confirmation performs the hard delete and records audit.
- Natural "remove" prefers archive unless permanent deletion is explicit.
- Drafting policy/mod-note requests uses the structured model output path.
- Prose-wrapped JSON draft output is rejected.
- Missing intent-bearing fields produce validation errors.
- Approval summary includes invocation, native tools, writes, permissions, and safety limits.
- Approval result explains enabled-but-private exposure.
- Denied tools propagate into assistant tool access and block execution.
- Dynamic tool listing exposes health reasons for skipped composed tools.
- Schedule minimum delay and recurrence are enforced.
- Repeated blocked/rate-limited scheduled composed runs back off.
- Run detail returns redacted transcript and step errors.
- Simulate does not post Discord writes.
- Runtime access is narrowed to the approved spec's native tools.
- Legacy run job path is removed or deliberately wired.

## Rollout Order

1. Ship lifecycle safety first: delete confirmation and archive-vs-delete behavior.
2. Remove hidden draft fallbacks and harden structured draft output.
3. Add lint and health as reusable service primitives.
4. Improve approval and exposure summaries.
5. Narrow runtime access and propagate denied tools.
6. Harden scheduling and backoff.
7. Add simulation, run detail, and version compare.
8. Add metrics, retention, and legacy cleanup.

This order fixes destructive behavior before expanding the feature, then improves the admin workflow without weakening the safety model.

## Success Criteria

- No composed tool can be permanently deleted by an unconfirmed model tool call.
- No natural-language draft path creates a spec through a keyword or regex fallback.
- Every approved tool has a readable capability envelope.
- Admins can tell whether a tool is approved, exposed, scheduled, healthy, and recently failing.
- Runtime access is derived from the approved spec, not from a broad ambient permission set.
- Explicit denied tool rules are honored in natural assistant execution.
- Composed schedules cannot create high-frequency queue churn.
- Simulation and run detail make most failures diagnosable from Discord.
- Confirmed legacy code is removed instead of preserved.
