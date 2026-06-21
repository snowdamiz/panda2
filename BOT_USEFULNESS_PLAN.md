# Panda Bot Usefulness Plan

## Reader And Outcome

This plan is for the engineer improving Panda after the current production-ready bot foundation. After reading it, they should be able to prioritize and implement the next usefulness layer: proactive automations, setup clarity, operational visibility, reminders, moderation alerts, music quality-of-life, feedback loops, and automatic behind-the-scenes knowledge capture.

## Scope

Included:

- Scheduled automations.
- First-run setup wizard.
- Admin status command.
- Automatic knowledge and memory curation.
- Real reminders and follow-ups.
- Moderation and server-log alert packs.
- Music quality-of-life improvements.
- Feedback buttons on assistant responses.

Excluded:

- User-facing knowledge capture shortcuts.
- Personal opt-in memory profiles.
- Per-channel behavior profiles.

Automatic knowledge capture replaces manual save shortcuts. Panda should decide what is worth remembering behind the scenes, store only durable server-useful knowledge, and avoid turning every chat message into memory.

## Guiding Principles

- Prefer Discord-native flows over dashboards for this phase.
- Keep powerful actions confirmation-gated.
- Make useful behavior discoverable without requiring admins to remember tool names.
- Treat saved knowledge as curated server context, not raw chat history.
- Build on the existing command, job, composed-tool, audit, permissions, and repository patterns.
- Keep each feature independently shippable.

## Phase 1: Scheduling Foundation

### Goal

Make Panda able to do useful work later, repeatedly, or after a delay.

### Capabilities

- Enqueue scheduled composed-tool runs from approved scheduled invocations.
- Support one-time reminders and recurring schedules.
- Store schedule metadata, next run time, last run status, owner, guild, target channel, and disabled state.
- Add a scheduler loop that claims due schedules and creates durable jobs.
- Prevent duplicate runs with dedupe windows and job leases.
- Surface failures in audit logs and admin status.

### Acceptance Criteria

- A scheduled composed automation can run without a Discord event.
- A one-time reminder survives process restart.
- A recurring reminder advances to the next run after success.
- Failed scheduled work retries safely and becomes visible to admins.

## Phase 2: Real Reminders And Follow-Ups

### Goal

Let users and admins ask Panda to remember future actions without writing workflow specs.

### Capabilities

- Natural requests like "remind me tomorrow", "remind us every Friday", and "follow up if nobody answers this in 2 hours".
- Slash command fallback for precise reminders.
- User, channel, and role-targeted reminder delivery with mention safety.
- Follow-up watchers for unresolved questions or stale threads.
- Snooze, complete, cancel, and list flows.
- Confirmation before creating reminders that mention roles or post publicly.

### Acceptance Criteria

- Panda can create, list, cancel, and deliver one-time reminders.
- Panda can create and deliver recurring reminders.
- Follow-up reminders can watch a message or thread and skip themselves when activity resolves the need.
- Public reminder creation uses safe mentions and clear confirmation.

## Phase 3: First-Run Setup Wizard

### Goal

Turn installation into a guided checklist instead of a runbook scavenger hunt.

### Capabilities

- New setup flow for owners and guild admins.
- Check Discord permissions, role hierarchy, privileged intents, webhook configuration, OpenRouter, Brave Search, music readiness, data storage, command registration, and default model.
- Recommend least-permission fixes with exact Discord-side actions.
- Offer to configure admin role, moderator role, allowed channels, model policy, and budget defaults.
- Use dry-runs before saving changes.
- Record setup actions in audit logs.

### Acceptance Criteria

- A newly installed server can run the setup flow and get a pass/fail checklist.
- Missing permissions are explained in terms a Discord admin can act on.
- Successful setup leaves the server with usable admin roles, safe default tool policy, and clear next steps.

## Phase 4: Admin Status Command

### Goal

Give admins one place to understand how Panda is configured and why something may not work.

### Capabilities

- New status view for guild configuration.
- Show assistant enabled state, default model, fallback models, tool policy, memory setting, budget limits, role mappings, tool access grants, channel access rules, configured integrations, queue state, and recent failures.
- Show Discord permission gaps for the current guild.
- Show music readiness and sidecar status.
- Include warnings before expensive or risky settings become problems.

### Acceptance Criteria

- Admins can answer "is Panda set up correctly?" from one command.
- Status output highlights actionable warnings first.
- Status does not leak secrets or hidden config values.

## Phase 5: Automatic Knowledge And Memory Curation

### Goal

Let Panda quietly preserve useful server knowledge without requiring users to press "save" or run knowledge commands.

### Capabilities

- Add a background curator that evaluates completed assistant interactions, durable decisions, repeated questions, resolved moderation guidance, stable server rules, and high-value summaries.
- Save only knowledge that is likely to help future server answers.
- Prefer concise generated summaries over raw Discord content.
- Deduplicate against existing knowledge before saving.
- Attach provenance metadata such as source type, confidence, source message IDs where appropriate, created time, and reason saved.
- Expire or down-rank low-confidence memory.
- Keep audit records for automatic saves and deletes.
- Allow admin deletion/export through existing management surfaces, but do not add user-facing save shortcuts.

### Candidate Save Rules

- Save a server rule, policy, FAQ, workflow, or decision when a user clearly confirms it.
- Save repeated answers when the same question appears multiple times and the answer stabilizes.
- Save summaries of long-lived threads only when they contain durable decisions or instructions.
- Do not save secrets, private user details, transient jokes, ordinary chatter, or unresolved speculation.
- Do not create personal preference profiles as part of this phase.

### Acceptance Criteria

- Panda can automatically create a knowledge entry after a confirmed durable decision.
- Panda can avoid saving low-value or sensitive content.
- Repeated facts are deduplicated instead of creating many near-identical entries.
- Future answers can use curated knowledge without exposing raw source text unnecessarily.

## Phase 6: Moderation And Server-Log Alert Packs

### Goal

Make existing Discord event awareness useful to admins without forcing them to hand-build every automation.

### Capabilities

- Preset alert packs for high-value events:
  - New webhook created or changed.
  - Invite created or deleted.
  - Role created, deleted, or permission-sensitive role changed.
  - Auto-moderation action triggered.
  - Member banned or unbanned.
  - Scheduled event changed.
  - Channel or thread settings changed.
- Route alerts to a configured admin or mod channel.
- Include event summary, risk level, actor when available, and suggested next action.
- Support enable, disable, preview, and test flows.
- Avoid alert storms with batching and cooldowns.

### Acceptance Criteria

- Admins can enable a recommended alert pack without writing a composed-tool spec.
- Alerts are useful, concise, and include enough context for action.
- Noisy events are rate-limited and batched.

## Phase 7: Music Quality-Of-Life

### Goal

Make Panda feel like a real daily-use music bot, not only a single-track player.

### Capabilities

- Persistent queue per guild.
- Loop track, loop queue, shuffle, remove, move, and clear.
- Favorites or saved playlists.
- Vote skip with configurable threshold.
- DJ role or permission gate for disruptive controls.
- Default volume and per-guild music settings.
- Better "now playing" with duration, requester, source, and queue position.
- Search result selection when a query is ambiguous.

### Acceptance Criteria

- Queue survives bot restarts where practical.
- Multiple users can manage queue safely without accidental disruption.
- Admins can configure who can skip, clear, or stop playback.

## Phase 8: Feedback Buttons

### Goal

Give Panda a lightweight quality signal after assistant responses.

### Capabilities

- Add feedback controls to assistant responses.
- Capture helpful, not helpful, too long, wrong, and unsafe/problematic feedback.
- Store feedback with response metadata, model, command, guild, user, and optional reason.
- Include feedback summaries in usage/admin reports.
- Use feedback to identify prompt, model, retrieval, and tool issues.
- Keep feedback private from normal users.

### Acceptance Criteria

- Users can rate a Panda answer with one click.
- Admins can see aggregate feedback trends.
- Feedback records do not store unnecessary raw message content.

## Suggested Implementation Order

1. Build the scheduling foundation.
2. Add reminders and follow-ups on top of scheduling.
3. Add admin status so new behavior is inspectable.
4. Add setup wizard using the same checks as admin status.
5. Add automatic knowledge curation.
6. Add moderation alert packs.
7. Add feedback buttons.
8. Add music quality-of-life improvements in focused slices.

## Risks And Guardrails

- Automatic memory can feel invasive if it saves too much. Keep it summary-based, conservative, auditable, and deletable.
- Reminders and alert packs can spam channels. Use cooldowns, batching, and confirmation for broad mentions.
- Scheduled jobs can duplicate work after restarts. Use durable leases, dedupe keys, and idempotent run records.
- Music features can become state-heavy. Persist only the queue and settings needed for a good restart experience.
- Feedback can become another form of sensitive content. Store compact signals and redact free text.

## Completion Definition

This plan is complete when Panda can proactively help a server through scheduled work, reminders, setup guidance, admin status, curated automatic knowledge, useful moderation alerts, improved music controls, and response feedback, without adding personal memory profiles or per-channel behavior profiles.
