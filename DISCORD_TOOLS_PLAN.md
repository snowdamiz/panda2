# Discord Tool Coverage Plan

This plan tracks the missing Discord-facing tools Panda should add before any self-extension work. The goal is broad Discord coverage within reasonable safety limits: the bot should be able to inspect Discord state it is allowed to see, draft or propose risky changes, and only execute privileged writes through explicit permissions, audit logs, dry runs, and confirmations.

## Current Tool Surface

Panda uses `github.com/disgoorg/disgo` and currently connects to Discord with `GuildMessages` and `MessageContent` gateway intents. It listens for application commands, message context commands, component interactions, modal submits, and normal message creates.

Current Discord/user surfaces:

- Natural message handling for bot-directed chat.
- Slash commands: `/ping`, `/help`, `/search-memory`, `/memory-consent`, `/admin ...`, `/ops ...`, and `/mod ...`.
- Message context menu commands: `Explain with Panda` and `Summarize with Panda`.
- Thread creation for chat continuation.
- Attachment capture and safe text extraction.
- Deferred interaction updates and queued long summarization jobs.

Registered LLM tool definitions:

- `fetch_recent_messages`
- `fetch_message`
- `search_knowledge`
- `summarize_text_file`
- `draft_moderator_note`
- `read_config`
- `generate_workflow_json`

Important current gap: the registry defines seven tools, but assistant tool exposure is currently much narrower than the registry. `toolPermissions` only grants memory read when guild memory is enabled and config read under `admin_only`; it does not yet pass request-level permissions such as `assistant.use`, `assistant.attachments`, or `moderation.use` into the assistant tool executor. Fixing that permission propagation is the first missing-tool milestone.

## Design Boundaries

- Keep all Discord SDK calls behind `internal/discord` adapter interfaces. LLM tools should call service interfaces, not DisGo directly.
- Use Discord permission preflight before every Discord API call. Check the bot's permissions and the requesting user's configured Panda permissions.
- Prefer read-only tools first. For writes, require explicit tool class, dry-run support, audit entry, rate limit, and confirmation for destructive or moderation-impacting operations.
- Treat fetched Discord content as untrusted model context. Preserve citations with guild, channel, message, author, and timestamp metadata.
- Do not index or bulk retain channel history by default. Use per-guild privacy settings and opt-in retention.
- Never expose arbitrary Discord REST calls to the model. Self-extension should register reviewed, typed tool manifests, not raw endpoint access.
- Use idempotency keys for write tools that can be retried.
- Suppress mass mentions and unsafe link spam in generated message content.

## Tool Policy Upgrade

Before adding more tools, replace the current coarse `tool_policy` behavior with a capability-aware policy:

- `off`: no LLM tools.
- `read_only`: safe read-only tools for users who pass `assistant.use` and channel rules.
- `assistive`: read-only tools plus non-destructive draft/generate tools.
- `admin_only`: read-only/admin read tools only for admins or users with explicit admin read roles.
- `moderator`: moderation read/draft tools for users with `moderation.use`.
- `write_confirmed`: selected write tools, always with dry-run and confirmation.
- `owner_ops`: owner-only operational tools.

Implementation tasks:

- Pass per-request allowed Panda permissions from `commands.Router` into `assistant.Service`.
- Add Discord permission checks in the Discord adapter for every tool execution.
- Add `ToolClass` to tool definitions: `discord_read`, `discord_write`, `moderation_write`, `admin_read`, `admin_write`, `memory`, `workflow`.
- Add `RequiresConfirmation`, `SupportsDryRun`, `MaxLimit`, and `DiscordPermissions` fields to tool definitions.
- Record tool call audit events with request ID, actor ID, guild ID, channel ID, target IDs, dry-run flag, and redacted arguments.
- Add tests proving unavailable tools are not advertised to the model.

## Phase 1: Read Previous Chats And Context

These tools unlock the user's main request: reading previous chats that the bot is allowed to see.

- `discord.fetch_message`
  - Replace or extend existing `fetch_message`.
  - Inputs: `guild_id`, `channel_id`, `message_id`.
  - Output: message content, embeds summary, attachment metadata, author ID, timestamps, edited flag, jump URL.
  - Requires: `assistant.use`, `VIEW_CHANNEL`, `READ_MESSAGE_HISTORY`, and Message Content intent for content fields.

- `discord.fetch_messages`
  - Generalize existing `fetch_recent_messages`.
  - Inputs: `channel_id`, `limit`, optional `before`, `after`, `around`, `include_author_ids`, `include_attachments`, `purpose`.
  - Hard caps: default 25, max 100 per call, model context packing max separate from REST fetch max.
  - Supports channel and thread IDs.

- `discord.fetch_thread_context`
  - Inputs: `thread_id`, optional `limit`, `before`, `include_starter`.
  - Include parent channel, starter message, active/archived status, participants where available.

- `discord.fetch_reply_chain`
  - Inputs: `channel_id`, `message_id`, `depth`.
  - Walk referenced messages where available and cite each source.

- `discord.list_pins`
  - Inputs: `channel_id`, optional `before`, `limit`.
  - Useful for summarizing canonical channel state without indexing history.

- `discord.search_messages`
  - Inputs: `guild_id`, query filters, optional channel IDs, author IDs, before/after.
  - Gate behind admin opt-in because search can reveal broad historical context.
  - Fall back to local saved summaries if Discord search is unavailable for the bot/runtime.

Exit criteria:

- The model can fetch cited prior messages and bounded channel/thread history.
- All fetched content is packed with source labels and prompt-injection warning text.
- Channel rules and Discord permissions are enforced before any fetch.

## Phase 2: Discord Metadata Read Tools

These tools let Panda understand server structure before acting.

- `discord.get_guild`
- `discord.list_channels`
- `discord.get_channel`
- `discord.list_active_threads`
- `discord.list_archived_threads`
- `discord.list_roles`
- `discord.get_role`
- `discord.get_member`
- `discord.list_members`
- `discord.list_bans`
- `discord.get_invite`
- `discord.list_invites`
- `discord.list_webhooks`
- `discord.list_scheduled_events`
- `discord.get_audit_logs`
- `discord.list_auto_moderation_rules`
- `discord.list_emojis`
- `discord.list_stickers`
- `discord.list_soundboard_sounds`

Special requirements:

- `discord.list_members` and member chunking require Guild Members privileged intent for broad member access.
- `discord.get_audit_logs`, invites, webhooks, bans, and auto-moderation rules require elevated Discord permissions and should be admin-only.
- Return summaries by default; only include full records when the user asks and policy allows it.

Exit criteria:

- Panda can answer structural questions like which channels exist, which roles exist, and what permissions/configuration are relevant.
- Privileged reads are audited.

## Phase 3: Safe Message And Collaboration Write Tools

These are useful but should be carefully constrained.

- `discord.send_message`
  - Requires `SEND_MESSAGES`.
  - Always uses `allowed_mentions` with no broad mentions by default.
  - Supports dry-run preview.

- `discord.reply_message`
  - Requires target message visibility.
  - Supports reply mention disabled by default.

- `discord.edit_own_message`
  - Only edits messages sent by Panda.

- `discord.delete_own_message`
  - Only deletes Panda messages unless moderation write permission is granted.

- `discord.add_reaction`
- `discord.remove_own_reaction`
- `discord.create_thread`
- `discord.rename_thread`
- `discord.archive_thread`
- `discord.add_thread_member`
- `discord.remove_thread_member`
- `discord.pin_message`
- `discord.unpin_message`

Confirmation rules:

- Send/reply/edit can be dry-run only until a user confirms.
- Pin/unpin and thread membership changes require confirmation.
- Any action that mentions users or roles must show the resolved mention targets before confirmation.

Exit criteria:

- Panda can collaborate in Discord without requiring slash commands for every message action.
- The bot cannot accidentally mass mention, spam, or edit/delete non-owned messages.

## Phase 4: Moderation And Admin Action Tools

These tools should be opt-in and disabled by default. The model may draft recommendations freely, but execution requires explicit moderator/admin permission plus confirmation.

- `discord.timeout_member`
- `discord.remove_timeout`
- `discord.kick_member`
- `discord.ban_member`
- `discord.unban_member`
- `discord.bulk_ban_members`
- `discord.add_member_role`
- `discord.remove_member_role`
- `discord.set_member_nick`
- `discord.delete_message`
- `discord.bulk_delete_messages`
- `discord.set_channel_slowmode`
- `discord.lock_thread`
- `discord.modify_channel_permissions`
- `discord.create_auto_moderation_rule`
- `discord.update_auto_moderation_rule`
- `discord.delete_auto_moderation_rule`
- `discord.create_invite`
- `discord.delete_invite`
- `discord.create_webhook`
- `discord.update_webhook`
- `discord.delete_webhook`
- `discord.create_scheduled_event`
- `discord.update_scheduled_event`
- `discord.delete_scheduled_event`

Safety requirements:

- Every moderation/admin write must support dry-run.
- Every destructive action must require a confirmation component tied to the actor and request.
- Every eligible Discord API call must include an audit log reason.
- Bulk actions need per-target results and hard caps lower than Discord's max unless explicitly raised.
- Moderator tools must never claim an action succeeded until the Discord API confirms it.

Exit criteria:

- Moderators can ask Panda to perform routine actions with previews and confirmation.
- Server owners can audit every privileged Discord mutation performed by Panda.

## Phase 5: Gateway Event Coverage And Local State

To let Panda reason about recent activity without repeatedly fetching REST endpoints, add an event cache.

Gateway events to consider:

- Message create/update/delete and bulk delete.
- Reaction add/remove/remove-all.
- Channel create/update/delete and pins update.
- Thread create/update/delete/list sync/member update.
- Guild member add/update/remove, gated by Guild Members intent.
- Role create/update/delete.
- Ban add/remove.
- Invite create/delete.
- Webhooks update.
- Auto moderation rule create/update/delete and action execution.
- Scheduled event create/update/delete/user add/user remove.
- Voice state update.
- Presence update only if there is a strong product need; keep disabled by default.
- Poll vote add/remove.

Implementation tasks:

- Add a bounded SQLite-backed recent event table with retention settings.
- Store IDs and metadata by default; store raw content only when guild retention settings allow it.
- Add `discord.recent_events` and `discord.channel_activity_summary` read tools.
- Add metrics for event lag, dropped events, cache size, and disabled privileged intents.

Exit criteria:

- Panda can summarize recent server activity without unbounded history fetches.
- Privacy settings control whether content is retained.

## Phase 6: Voice And Rich Media

Keep this optional unless there is a clear product need.

- `discord.join_voice`
- `discord.leave_voice`
- `discord.voice_state`
- `discord.play_audio`
- `discord.stop_audio`
- `discord.get_voice_regions`
- `discord.list_stage_instances`
- `discord.create_stage_instance`
- `discord.update_stage_instance`
- `discord.delete_stage_instance`

Constraints:

- Voice requires additional operational review, shard/session awareness, and stronger abuse controls.
- Do not add audio capture or transcription until consent, retention, and disclosure rules are designed.

## Phase 7: Self-Extension Preparation

Self-extension should mean "Panda can propose and register reviewed tool manifests", not "Panda can run arbitrary code".

Add these internal tools only after the Discord tool framework is hardened:

- `tool_catalog.list_capabilities`
  - Lists available adapter capabilities and their permission classes.

- `tool_catalog.describe_capability`
  - Returns schemas, permissions, risk class, rate limits, and example calls.

- `tool_catalog.propose_tool`
  - Produces a typed manifest and test checklist for a new tool. Does not install or execute it.

- `tool_catalog.validate_manifest`
  - Validates schema, permission class, timeout, redaction, audit policy, dry-run behavior, and confirmation rules.

- `tool_catalog.register_manifest`
  - Owner-only. Registers reviewed manifests backed by existing adapter methods.

Guardrails:

- No arbitrary HTTP endpoints.
- No arbitrary SQL.
- No dynamic Go plugin loading in production.
- New tools must map to pre-existing adapter methods or reviewed code.
- New write tools start disabled and owner-only.

## Suggested Build Order

1. Fix per-request tool permission propagation and tool definition metadata.
2. Expand message read tools for previous chat access.
3. Add guild/channel/thread/role/member read tools.
4. Add audit, rate-limit, dry-run, and confirmation infrastructure for write tools.
5. Add safe message/thread write tools.
6. Add moderation/admin action tools behind opt-in feature flags.
7. Add gateway event cache and recent activity tools.
8. Add self-extension manifest tools.

## Reference Links

- Discord API reference: https://docs.discord.com/developers/reference
- Discord gateway and intents: https://docs.discord.com/developers/events/gateway
- Discord gateway events: https://docs.discord.com/developers/events/gateway-events
- Discord privileged intents support article: https://support-dev.discord.com/hc/en-us/articles/6207308062871-What-are-Privileged-Intents
- Discord message resource: https://docs.discord.com/developers/resources/message
- Discord channel resource: https://docs.discord.com/developers/resources/channel
- Discord guild resource: https://docs.discord.com/developers/resources/guild
- Discord audit log resource: https://docs.discord.com/developers/resources/audit-log
- Discord webhook resource: https://docs.discord.com/developers/resources/webhook
- DisGo documentation: https://disgoorg-disgo.mintlify.app/
