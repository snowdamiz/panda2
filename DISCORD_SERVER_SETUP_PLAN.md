# Discord Server Setup Automation Plan

## Reader And Goal

This plan is for an internal engineer adding first-class Discord server setup automation to Panda. After reading it, they should be able to implement the missing pieces that let a guild admin choose or customize a preset and have Panda build a usable server from scratch, including roles, channels, permissions, onboarding, support tickets, and Panda-specific access controls.

## Audit Summary

Panda already has a strong foundation for admin-controlled Discord automation:

- Feature-based install exists. The landing install flow fetches a public feature catalog, lets users choose Panda capabilities, calculates Discord permission bits, creates an install intent, and persists enabled guild features after OAuth install.
- Runtime feature gates exist. Tools are tied to Panda permissions, Discord permissions, confirmation requirements, and guild feature flags.
- Admin access control exists. Panda can manage Panda role/user permissions, tool allow/deny rules, channel allow/deny rules, budgets, prompt, soul, safety, usage, audit, and knowledge.
- Discord reads are broad. Panda can inspect guild metadata, channels, roles, members, pins, active and archived threads, invites, webhooks, scheduled events, automod, bans, and audit logs when allowed.
- Confirmed Discord writes already exist for messages, polls, threads, basic role creation, member role assignment, nicknames, channel permission overwrites, slowmode, thread locks, invites, webhooks, scheduled events, automod, and moderation actions.
- The confirmation model is mature. Sensitive writes support dry runs, confirmation buttons, audit policy, Discord permission preflight, and queue-backed execution.
- Composed tools can create event-triggered and scheduled automations. Existing support includes welcome-style member-join flows and many Discord gateway event types.
- The bot already handles buttons, selects, and modals, but only for confirmations, temporary selections, feedback, and a small set of admin modals.

The current gaps are not a lack of individual Discord primitives. The gaps are the product layer, persistence layer, and several missing REST operations needed to build and operate an entire server setup.

## Missing Capabilities

### Server Structure

- No first-class create/update/delete tools for text channels, voice channels, stage channels, forum/media channels, or categories.
- No bulk channel/category creation with ordering, parent category assignment, topic, NSFW flag, slowmode, bitrate/user limits, or default permission overwrites.
- No channel position planner.
- No channel deletion/archive workflow for failed or replaced setup resources.
- No local resource mapping from a Panda-managed template resource to the Discord object it created.

### Roles And Permissions

- Current role creation intentionally creates zero-permission roles only.
- No role update/delete/reorder support.
- No template-level role permissions, color, hoist, mentionability, or hierarchy checks.
- No high-level permission matrix that can express category defaults, private staff areas, support ticket visibility, onboarding gates, or verified-member access.
- No full preview of which permissions Panda will grant or deny for each channel/category.

### Templates

- The install feature preset is a Panda capability preset, not a Discord server setup template.
- No template schema, versioning, preset catalog, custom variables, or template validation.
- No diff engine that compares an existing guild to a proposed template.
- No idempotent apply engine that can rerun safely without duplicating roles/channels.
- No "customize before apply" flow for channel names, role names, support team roles, colors, onboarding copy, ticket categories, or enabled modules.

### Ticketing

- No persistent ticket panel model.
- No persistent component routing for ticket buttons/selects/modals.
- No ticket lifecycle state: open, claimed, waiting, closed, reopened, archived.
- No private ticket channel/thread creation flow with requester, staff roles, and optional observers.
- No close reason, transcript export, claim/assign/escalate, priority/tags, SLA reminders, or audit trail.
- No admin UX to create multiple ticket departments, each with distinct staff roles and destinations.

### Onboarding

- No guided onboarding flow after install beyond a generic success page and suggested setup prompts.
- No member verification/rules acknowledgement flow.
- No role selection menus for interests, pronouns, regions, notifications, or access groups.
- No onboarding session state, completion tracking, retry path, or admin analytics.
- No automatic assignment of a verified/member role after onboarding completion.
- No generated welcome/rules copy tied to a selected setup template.

### User Experience

- No template picker in the landing page or portal.
- No setup preview/diff screen showing "Panda will create X roles, Y categories, Z channels, one ticket panel, and one onboarding gate."
- No progress UI for long-running setup jobs.
- No recovery UX when partial setup succeeds and a later step fails.
- No natural-language "set up my server for X" flow that converts admin intent into a reviewed template application.
- No post-install "continue setup" state that remembers selected features, desired template, and customization choices.

## Product Target

Panda should support two entry points:

- Template path: an admin selects a preset, customizes fields, previews the plan, confirms, and Panda applies it.
- Conversational path: an admin asks Panda to set up a server for a community, support desk, creator, gaming group, course, or product team. Panda drafts a setup plan, asks focused follow-ups only where needed, previews the diff, and applies after confirmation.

The outcome should feel like a guided server builder, not a bag of Discord tools.

## Preset Template Catalog

Ship a curated starter catalog:

- Minimal Community: rules, announcements, general chat, media, feedback, staff area, member role, moderator/admin roles.
- Creator Hub: announcements, clips, collab, fan chat, content requests, events, media showcase, mod queue.
- Gaming Server: welcome, roles, LFG, voice lobbies, clips, announcements, rules, mod logs.
- Support Desk: public help, ticket panel, private ticket category, staff role, triage role, FAQ/knowledge channel.
- SaaS/Product Community: announcements, changelog, support tickets, feedback, beta testers, docs/FAQ, roles by customer tier.
- Study/Course: syllabus/resources, questions, study groups, office hours, assignments, verified/student roles.

Each preset should expose editable variables rather than raw JSON first: server purpose, role names, support team role, channel prefix style, ticket categories, welcome copy, rules copy, verification strictness, and which Panda features should be enabled.

## Architecture Plan

### 1. Template Schema

Add a versioned setup template schema with these resource types:

- Roles: name, color, hoist, mentionable, permissions, display order, managed alias.
- Categories: name, overwrites, position, managed alias.
- Channels: type, name, parent alias, topic, slowmode, NSFW, overwrites, starter messages, managed alias.
- Forums/media channels: tags, guidelines, default reaction, default sort/order where supported.
- Voice/stage channels: user limits, bitrate, parent alias, overwrites.
- Panda config: prompt overlay, channel rules, role/user profiles, tool access, feature gates, budgets.
- Ticket panels: panel channel, message copy, departments, staff roles, target category/thread mode, tags, transcript policy.
- Onboarding flows: welcome channel, rules acknowledgement, role menu steps, verified role, intro prompt, completion message.
- Automations: composed tools to install, approve, pause by default, or bind to events.

Keep templates declarative. Runtime services should own Discord API calls and state transitions.

### 2. Persistence

Add project-local state for setup and templates:

- Setup templates: built-in template metadata, schema version, default variables, and release state.
- Guild setup projects: selected template, variables, preview JSON, status, actor, confirmation metadata, created resources, failed steps.
- Guild setup resources: stable managed aliases mapped to Discord object IDs, object type, template version, last applied hash.
- Ticket panels: panel message/channel, departments, staff roles, category/thread mode, enabled state.
- Tickets: requester, department, channel/thread, status, assignee, priority, tags, close reason, timestamps.
- Ticket events: append-only ticket lifecycle audit.
- Onboarding flows: steps, target roles/channels, enabled state.
- Onboarding sessions: member progress, assigned roles, completion timestamps.

The resource mapping is critical for idempotency and cleanup.

### 3. Discord REST Surface

Extend the Discord tool provider and registry with confirmed admin tools:

- Create, update, delete, and position guild channels.
- Create, update, delete, and position categories.
- Create, update, delete, and position roles.
- Set full channel overwrites from a structured permission matrix.
- Send setup starter messages and ticket panel messages with persistent component IDs.
- Create private ticket channels or private threads with correct overwrites.
- Export ticket transcripts from visible ticket history.

Every write should support dry run, confirmation, audit metadata, permission preflight, and clear error output.

### 4. Plan And Diff Engine

Build a setup planner that:

- Validates template variables and required Discord/Panda permissions.
- Reads current guild roles/channels.
- Matches existing managed resources by stored aliases first, then by safe name heuristics with explicit conflict warnings.
- Produces a human-readable diff grouped by roles, categories, channels, Panda settings, ticketing, onboarding, and automations.
- Blocks dangerous changes unless the admin explicitly opted in.
- Produces a deterministic apply plan with dependency ordering.

The preview should be the primary UX artifact. Admins should understand the blast radius before pressing confirm.

### 5. Apply Engine

Apply setup in a background job:

- Persist the plan before execution.
- Execute in dependency order: roles, categories, channels, overwrites, messages/panels, Panda config, composed tools, onboarding state.
- Record every created or updated resource immediately.
- Retry rate-limited or transient Discord failures safely.
- Stop on hard failures and return a recovery plan.
- Support rerun/resume using stored setup resources.
- Support cleanup of resources created by the failed job when the admin asks for rollback.

Do not attempt a fake transaction over Discord. Use durable steps, idempotency keys, and compensation.

### 6. Ticketing System

Implement tickets as a first-class module, not only as composed tools:

- Admin creates one or more ticket panels from a template or natural language.
- Panel buttons/selects use persistent custom IDs that route to a ticket service.
- Opening a ticket can show a modal for reason/category when configured.
- Ticket service creates a private channel or private thread, grants requester and staff access, posts a starter message, and records state.
- Staff can claim, assign, add/remove users, tag, change priority, close, reopen, and archive.
- Closing asks for an optional reason and can generate a transcript.
- Ticket events are visible in Panda audit/support views.
- Optional SLA reminders can be backed by existing schedule jobs.

Start with private channels for reliability, then add thread mode once permissions and transcript behavior are proven.

### 7. Onboarding Flow

Implement onboarding as a reusable flow:

- Post a welcome/rules panel in the configured channel.
- Let admins choose verification mode: rules acknowledgement only, role selection only, or rules plus role selection.
- On completion, assign the configured verified/member role and optionally remove a newcomer/quarantine role.
- Support role selection menus for interest and notification roles.
- Record member progress and expose simple admin status.
- Allow admins to pause onboarding without deleting the flow.

The default should be low-friction. Avoid making every community manage complex verification unless they ask for it.

### 8. User Experience

Improve both web and Discord UX:

- Add a setup template picker to the install or post-install experience.
- Save desired template and variables in the install intent metadata when selected before install.
- On install success, show a direct "finish server setup" path and suggested Discord prompt.
- Add a portal setup wizard for admins who prefer forms.
- In Discord, support prompts like "Panda set up this server as a support community" and "Panda show me the setup templates."
- Always show a preview/diff before confirmed application.
- Show progress and final summary with links/mentions to created channels and roles.
- Provide "customize" controls for common variables before exposing raw JSON.

### 9. Security And Safety

Guardrails:

- Require guild owner, Discord admin, or Panda admin config write permission for setup application.
- Require explicit confirmation for every template application.
- Preflight bot permissions and role hierarchy before role/member/channel writes.
- Refuse Administrator permission in templates by default.
- Separate Discord permissions from Panda access controls in previews.
- Prevent accidental broad mentions in starter messages and ticket panels.
- Keep audit events for previews, confirmations, applications, failures, ticket lifecycle, onboarding changes, and rollbacks.
- Add per-guild rate limits for setup applications and ticket creation.

### 10. Testing And Verification

Add Go tests for:

- Template validation, defaults, and variable substitution.
- Diff matching with existing roles/channels and conflict detection.
- Apply plan ordering and idempotent reruns.
- Discord REST adapters using fake REST clients.
- Confirmation IDs and persistent component routing.
- Ticket lifecycle state transitions.
- Onboarding session completion and role assignment.
- Permission preflight failures and role hierarchy failures.
- Install intent metadata carrying template selections.

Do not rely on browser testing for this work; keep web behavior covered with unit tests around data shaping and server APIs.

## Implementation Milestones

### Milestone 1: Server Builder Foundations

- Add template schema, built-in preset definitions, and validation.
- Add setup project/resource persistence.
- Add planner and preview output.
- Add create/update/delete channel/category tools.
- Add richer role create/update/delete tools.
- Add setup apply job with idempotency and progress.

Acceptance: an admin can apply a Minimal Community template to an empty test guild through a confirmed dry-run/apply flow, rerun it without duplicates, and see a summary of created resources.

### Milestone 2: Template UX

- Add setup template list/preview endpoints.
- Add template picker and customizer to the post-install or portal flow.
- Store selected template variables in install intent metadata.
- Add Discord natural-language template discovery and setup drafting.
- Add final setup summaries with recovery guidance.

Acceptance: an admin can choose a Support Desk template before or after install, customize names and roles, preview the diff, approve it, and watch a progress summary complete.

### Milestone 3: Ticketing

- Add ticket panel, ticket, and ticket event persistence.
- Add persistent component routing for ticket buttons/selects/modals.
- Add ticket open, claim, add/remove participant, close, reopen, archive, and transcript export.
- Add ticket setup blocks to templates.
- Add ticket audit and admin status views.

Acceptance: the Support Desk template creates a working ticket panel. Members can open tickets; staff can claim and close them; transcripts and lifecycle events are retained.

### Milestone 4: Onboarding

- Add onboarding flow/session persistence.
- Add rules acknowledgement and role selection components.
- Add verified role assignment and optional newcomer role removal.
- Add onboarding setup blocks to templates.
- Add admin pause/status controls.

Acceptance: a Community template can gate access behind rules acknowledgement, assign a verified role, and let members select optional interest roles.

### Milestone 5: Polish And Expansion

- Add more presets and template variants.
- Add setup rollback/cleanup UX.
- Add setup analytics: completion rate, open tickets, onboarding drop-off.
- Add template export/import for power users.
- Add safe natural-language custom template generation.

Acceptance: admins can start from a preset, customize deeply, export/import templates, and recover from partial setup without support intervention.

## Recommended First Slice

Start with the Minimal Community template and the Discord REST primitives it requires: role create/update, category create/update, text channel create/update, channel positioning, full overwrite setting, setup project persistence, preview, confirmation, and idempotent apply.

That slice proves the architecture without taking on ticketing and onboarding complexity too early. Once resource mapping and apply/resume are solid, ticketing and onboarding can build on the same template and component foundations.
