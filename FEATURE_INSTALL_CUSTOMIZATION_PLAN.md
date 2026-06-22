# Feature-Based Discord Install Customization Plan

## Reader And Outcome

This plan is for a Panda engineer implementing feature-based bot installs from a cold start.

After reading it, they should be able to build the full path where a server admin chooses Panda features before install, Panda generates the minimum Discord permission invite for those choices, stores the selected feature set for the installed guild, and gates bot behavior behind that stored feature set at runtime.

## Goal

Let a server admin customize Panda before adding it to a Discord server.

The admin should choose feature groups such as chat, threads, polls, reminders, music, web search, knowledge, and moderation assistance. Panda should calculate the required Discord bot permission bitfield, generate a Discord add link, and remember which features were selected so the bot only exposes and runs those features in that guild.

Discord permissions are necessary but not sufficient. The requested permission bitfield controls what Discord grants to the bot role. Panda's saved guild feature configuration controls what Panda is allowed to do.

## Non-Goals

- Do not infer enabled features from the bot role's current Discord permissions.
- Do not add deterministic fallback behavior that bypasses the feature-selection path.
- Do not silently enable unselected features just because Discord permissions happen to exist.
- Do not require browser testing in the implementation workflow.
- Do not keep legacy install behavior around once the new flow is confirmed, unless a temporary rollout flag is explicitly chosen.

## Current State

Panda already has useful building blocks:

- A landing app with a single install link.
- A signed Discord webhook endpoint for application authorization events.
- An install service that records guild installs, ownership metadata, trial creation, and audit events.
- A guild model with a feature-flags field that is not currently the source of runtime gates.
- Role and channel access controls.
- Tool definitions that declare Panda permissions, tool classes, and Discord permission requirements.
- Discord permission preflight before executing tools that need Discord-side permissions.
- Privileged Discord write controls are currently disabled in the runtime surface, along with admin, ops, schedule, and composed-tool controls. These include stricter Discord permission-gated actions such as role management, channel management, message management, webhook management, invites, and moderation writes. The disabled surfaces are filtered out of public command registration or return a disabled response even though much of the service and test scaffolding exists.

The missing pieces are:

- A feature catalog that maps user-facing feature choices to Discord permissions and Panda runtime gates.
- A generated install URL API.
- A short-lived install intent that binds feature selections to a future Discord install.
- An OAuth callback that validates `state` and associates the intent with the installed guild.
- Runtime gates that intersect current role/channel/tool access with the guild's enabled feature set.
- Admin UX for viewing and changing feature selections after install.
- Restoration of currently disabled privileged Discord command and tool surfaces behind explicit feature gates.

## Core Principle

Feature availability must be checked in two layers:

1. Panda feature gate: is this feature enabled for the guild?
2. Discord permission gate: does the bot currently have the permissions needed to perform this operation?

The Panda feature gate prevents unselected product behavior. The Discord permission gate catches changed role permissions, channel overwrites, missing scopes, and Discord-side drift.

## End-To-End User Flow

1. A server admin opens Panda's install/setup page.
2. The page shows feature groups with short permission explanations and a live "Discord permissions requested" summary.
3. The admin selects features.
4. The landing page sends selected feature IDs to Panda's backend.
5. Panda validates the feature IDs against the server-side feature catalog.
6. Panda calculates the required Discord permission bitfield from the selected features.
7. Panda creates a short-lived install intent with selected features, calculated permissions, a random `state`, expiration, and audit metadata.
8. Panda returns a generated Discord OAuth URL.
9. The admin follows the generated URL and chooses a guild in Discord.
10. Discord redirects back to Panda's OAuth callback.
11. Panda validates `state`, exchanges the authorization code, obtains the authorized guild and permissions, and consumes the install intent.
12. Panda stores the enabled feature set for the guild.
13. Panda records the install as active and starts or preserves the trial.
14. Panda audits the selected features, requested permissions, granted permissions, installer, and guild.
15. When users interact with Panda, runtime access is filtered by the saved guild feature set before tools or commands are exposed.
16. If a user tries to use a disabled feature, Panda gives a clear "feature not enabled for this server" response.
17. If a feature is enabled but Discord permissions are missing, Panda gives a clear reauthorization or role-permission repair message.

## Discord OAuth Strategy

Use a generated authorization URL instead of the static Developer Portal URL.

The URL should include:

- `client_id`: Panda's Discord application ID.
- `scope`: `bot applications.commands`.
- `permissions`: decimal Discord permission bitfield calculated by Panda.
- `integration_type`: `0` for guild install.
- `state`: short-lived install intent nonce.
- `response_type`: `code`.
- `redirect_uri`: Panda's install callback URL.
- `prompt`: `consent`, when explicit reauthorization is needed.

Enable Discord's bot OAuth code grant requirement or otherwise ensure the flow returns to Panda's callback with `state`, `code`, and install metadata. A plain bot invite is not enough because it does not reliably return the custom `state` needed to bind selected features to the installed guild.

After the callback exchanges the code, Panda should store only the guild/install information it needs. If Discord returns user tokens as part of the flow, do not retain them unless a future feature has a specific user-token requirement. Prefer revoking or discarding them after install binding.

The `APPLICATION_AUTHORIZED` webhook remains useful for reconciliation and audit, but it should not be the only source for feature binding because it does not carry Panda's install intent state.

## Scopes Versus Bot Permissions

Keep Discord OAuth scopes separate from Discord bot role permissions.

Scopes authorize installation capabilities. Panda should always request `bot` for guild installation and `applications.commands` for slash and context menu commands.

Bot permissions authorize what the bot role can do in the guild or channel. These become the decimal `permissions` bitfield. Examples include View Channels, Send Messages, Read Message History, Create Polls, Create Public Threads, Connect, Speak, and Manage Messages.

Do not add a bot permission bit just because a feature uses slash commands. Slash and context menu command installation comes from the `applications.commands` scope. Only request a bot permission when Panda's bot user actually needs that guild or channel capability.

## Feature Catalog

Create a server-side feature catalog. The frontend can render from an API response or a mirrored generated artifact, but the backend must be authoritative.

Each feature should define:

- Stable feature ID.
- User-facing label.
- User-facing description.
- OAuth scopes needed beyond the default install scopes, if any.
- Discord permission names needed for install.
- Required Panda permission names.
- Native slash commands or message commands affected.
- Tool names or tool classes affected.
- Whether it needs privileged gateway intent support.
- Whether it consumes plan quota.
- Whether it requires confirmation for writes.
- Whether it is selectable in public install UI.
- Dependencies on other features.

Example catalog sketch:

| Feature | Purpose | Discord permissions | Panda gates |
| --- | --- | --- | --- |
| `assistant_chat` | Mention/chat, ask, explain, summarize, rewrite, translate | View Channels, Send Messages, Read Message History, Embed Links | `assistant.use` |
| `threads` | Dedicated chat threads | View Channels, Send Messages, Create Public Threads, Send Messages in Threads, Manage Threads if needed for lifecycle actions | `assistant.use_threads` |
| `polls` | Native Discord polls | View Channels, Send Messages, Create Polls | poll command and poll tool |
| `reminders` | User/channel/role reminders | View Channels, Send Messages | reminder workflow |
| `music` | Voice playback and queue controls | View Channels, Connect, Speak, Use Voice Activity | music workflow |
| `knowledge` | Server knowledge search and citations | no additional Discord permissions beyond assistant read path | memory read/search tools |
| `attachments` | Summarize safe uploaded text files | View Channels, Read Message History | attachment tool |
| `web_search` | Current public web answers | no additional Discord permissions beyond assistant response path | web search tool |
| `admin_setup` | Setup checklist, behavior, status, prompt, soul, and billing views | no additional Discord permissions beyond command install unless setup creates Discord roles | admin setup and config commands |
| `admin_access_control` | Panda role, channel, tool, and budget access settings | Manage Roles only when Panda creates or assigns Discord roles; otherwise no extra Discord permissions | admin access commands |
| `admin_audit` | Audit and usage visibility | no additional Discord permissions | admin audit and usage tools |
| `composed_tools` | Draft, approve, run, schedule, and audit composed tools | permissions required by the approved composed tool actions | composed tool and schedule commands |
| `privileged_discord_tools` | Confirmed Discord write actions outside moderation | Manage Roles, Manage Channels, Manage Messages, Manage Webhooks, Manage Events, Manage Nicknames, Create Instant Invite, or other action-specific permissions | confirmed privileged Discord tools |
| `owner_ops` | Owner-only health, guild, incident, drain, and resume operations | no additional Discord permissions | owner ops commands and tools |
| `moderation_assist` | Drafts and confirmed moderation actions | Moderate Members, Manage Messages, Kick Members, Ban Members as selected subfeatures | moderation tools |

Keep this catalog granular enough to request minimal permissions. For high-risk areas, split features into smaller choices rather than one broad "moderation" bucket.

## Currently Disabled Privileged Discord Surfaces

Privileged Discord tools must be treated as first-class selectable features, not as permanently disabled legacy paths. These are the tools that require stricter Discord bot permissions, such as managing roles, managing channels, deleting or pinning messages, managing webhooks, managing scheduled events, creating invites, changing nicknames, kicking users, banning users, or timing out members.

The implementation should restore currently disabled surfaces only when the corresponding guild feature is enabled:

- `admin_setup`: setup checklist, status, behavior, prompt, soul, billing status, and support-oriented admin views.
- `admin_access_control`: Panda role profile management, channel allow/deny rules, tool role access, and budget limits.
- `admin_audit`: audit and usage reporting.
- `composed_tools`: composed tool draft, approval, invocation, scheduling, and audit flows.
- `owner_ops`: owner-only operational commands such as health, guild count, incident mode, drain, resume, and reload.
- `privileged_discord_tools`: confirmed Discord write actions such as creating roles, assigning or removing member roles, changing channel permissions, managing webhooks, creating invites, managing events, changing nicknames, and other managed guild changes.
- `moderation_assist`: confirmed moderation actions such as deleting messages, timing out members, kicking members, and banning members.

The current hard-disable behavior should be removed once each surface is protected by feature gates, role/admin checks, confirmation requirements, and Discord permission preflight. Do not keep both the hard-disable path and the feature-gated path after the new behavior is confirmed.

Privileged Discord tools should not be selected as one broad all-or-nothing option. Separate low-risk configuration/status features from high-risk role management, channel management, message management, moderation, composed automation, and owner operations.

Public install UI should not expose `owner_ops` by default. Owner operations are a bot-owner capability, not a guild-admin product feature. They still need to be represented in the catalog so runtime gates and command registration can be consistent.

## Permission Calculation

Permission calculation should be deterministic from the selected feature set, but not a fallback for LLM behavior. It is normal product logic, not an LLM decision.

Algorithm:

1. Normalize and validate selected feature IDs.
2. Expand dependencies.
3. Collect Discord permission names from all selected features.
4. Convert names to Discord permission bit values.
5. OR all bits into one integer.
6. Return the decimal integer string used in the OAuth URL.
7. Store both the selected features and calculated permission names/bitfield on the install intent.

The frontend may show a live preview, but the backend must recompute and verify the final bitfield. Do not accept client-supplied permission integers as authoritative.

## Install Intent Persistence

Add a persistence model for install intents.

Suggested fields:

- Intent ID.
- Random `state` value, stored hashed if practical.
- Selected feature IDs.
- Expanded feature IDs.
- Requested Discord permission names.
- Requested Discord permission bitfield.
- Source, such as landing page, app directory custom URL, or admin reauthorization.
- Optional desired plan.
- Optional referrer or campaign metadata.
- Installer session metadata, if available.
- Status: pending, consumed, expired, canceled.
- Guild ID after callback.
- Installer user ID after callback.
- Expiration timestamp.
- Created and updated timestamps.

Intent consumption must be atomic. A `state` value can only bind one successful guild install.

## Guild Feature Persistence

Use a normalized representation that can support future UI and audits.

Preferred model:

- `guild_features` table with one row per guild and feature ID.
- Enabled boolean or status enum.
- Source install intent ID.
- Enabled by user ID.
- Created and updated timestamps.

Alternative:

- JSON field on guild config for enabled feature IDs.

The normalized table is better for audit, queries, and future feature-management UI. If the existing guild feature-flags field is confirmed as legacy or insufficient, migrate away from it rather than building a second unclear meaning around it.

## OAuth Callback Behavior

The callback must:

1. Validate `state`.
2. Verify the install intent exists, is pending, and is not expired.
3. Exchange the OAuth code with Discord.
4. Verify the authorization is for guild install.
5. Extract guild ID, installer user ID, scopes, and granted permissions from the callback/token response where available.
6. Record or update the guild install.
7. Start or preserve the trial.
8. Persist enabled guild features from the consumed intent.
9. Store requested and granted permission metadata for audit/debugging.
10. Mark the intent consumed.
11. Redirect the admin to a success page with next steps.

If the callback succeeds but the webhook arrives later, the webhook should reconcile metadata without changing the selected feature set. If the webhook arrives first, it should record the install but leave feature binding pending until the callback consumes the intent.

## App Directory And Landing Entry Points

The landing page should make feature selection the default "Start a trial" path.

For Discord App Directory or app profile installs, configure a custom install URL that points to Panda's hosted install page instead of using only Discord's provided link. This lets Panda collect feature choices before generating the final Discord authorize URL.

If a static fallback install URL must exist during rollout, it should map to an explicit default feature preset and still create an install intent before sending the user to Discord.

## Runtime Gating

Every feature must be gated by saved guild features, not only by role permissions.

Runtime access should be calculated as:

1. Resolve whether the guild has the feature enabled.
2. Resolve whether the caller has the relevant Panda role/channel/admin permission.
3. Resolve whether the server's tool policy allows the tool class.
4. Expose only tools and commands that pass all Panda gates.
5. Execute only after Discord permission preflight passes for operations that require Discord permissions.

Apply gates in these areas:

- Natural-language tool exposure.
- Slash command handlers.
- Message context menu actions.
- Background jobs spawned by interactions.
- Scheduled jobs and reminders.
- Music manager actions.
- Composed tool invocation.
- Admin setup/status displays.

Do not rely only on hiding commands in Discord. Global commands may still be visible depending on Discord behavior and timing. Handlers must enforce gates server-side.

## Command And Tool UX

When a feature is disabled:

- Reply ephemerally for slash commands where possible.
- Say the feature is not enabled for this server.
- Offer the admin path to enable it.
- For features requiring more Discord permissions, include a reauthorization link or direct the admin to generate one.

When Discord permissions are missing:

- Say the feature is enabled but Panda lacks Discord permissions.
- Identify missing permissions by friendly name.
- Offer a reauthorization link with the exact missing feature set.

When the user lacks role/channel permission:

- Keep existing role/channel denial behavior.
- Do not imply the feature is disabled server-wide if it is only restricted for that user.

## Admin Feature Management After Install

Admins need a way to view and update enabled features after install.

Minimum surface:

- Status view showing enabled features.
- Missing Discord permissions for enabled features.
- Reauthorization link for features that need additional Discord permissions.
- Audit record for feature changes.

Recommended commands or UI:

- Feature list.
- Enable feature.
- Disable feature.
- Generate reauthorization link.

Feature enabling must not become active until required Discord permissions are either already present or the admin completes reauthorization. Features that need no additional Discord permissions may activate immediately.

Feature disabling should take effect immediately in Panda runtime gates. It does not need to remove Discord permissions from the bot role, because Discord does not need to be the source of truth for Panda's enabled behavior.

## Reauthorization Flow

When an admin enables additional features:

1. Compare required permission bitfield for new enabled feature set against known/granted/current permissions.
2. If additional Discord permissions are required, create a reauthorization intent.
3. Generate a Discord OAuth URL with the union of currently enabled and newly requested feature permissions.
4. Validate callback state.
5. Update guild features only after successful callback.
6. Audit previous features, requested features, requested permissions, and actor.

Do not downgrade a bot role through OAuth when disabling features. Panda gates should disable behavior immediately, and the admin can manually adjust Discord role permissions if desired.

## Billing And Plans

Feature selection and billing entitlements are separate gates.

A feature can be enabled for a guild but still unavailable because the current plan lacks quota or entitlement. The runtime order should be:

1. Feature enabled for guild.
2. User/channel/role allowed.
3. Plan entitlement and quota available.
4. Tool policy allowed.
5. Discord permissions available.

Install-time feature choices should not grant paid entitlements. They only configure the bot behavior surface and Discord permissions.

## Security And Abuse Controls

Use these invariants:

- `state` must be random, single-use, and short-lived.
- The backend recomputes permission bitfields from feature IDs.
- Client-provided permission bitfields are ignored or treated only as display hints.
- Callback guild ID must be the guild that receives the features.
- Install intent consumption must be atomic.
- Intent expiration must fail closed.
- Feature changes must be audited.
- Do not store user OAuth tokens unless a specific feature requires them.
- If tokens are returned only for install binding, discard or revoke them after use.
- Do not allow unknown feature IDs.
- Do not allow hidden/internal feature IDs from public install UI.

## Data Migration

Migration work:

1. Add install intent storage.
2. Add guild feature storage.
3. Add OAuth client secret and callback URL configuration.
4. Backfill existing installed guilds to a named default feature preset.
5. Audit the backfill as a migration event.
6. Confirm whether the old guild feature-flags field is legacy.
7. Remove or repurpose legacy feature flag code only after confirming ownership and semantics.

Default preset should be explicit. For example:

- `assistant_chat`
- `polls`
- `reminders`
- `web_search`
- `knowledge`
- `attachments`
- `music`

High-risk moderation or admin-write features should not be included by accident.

## Implementation Slices

### Slice 1: Feature Catalog And Permission Calculator

Build a backend catalog with tests for feature normalization, dependency expansion, permission collection, and bitfield calculation.

Acceptance:

- Unknown feature IDs are rejected.
- Dependencies are included.
- Permission bitfield is stable.
- Public and internal features are distinguishable.

### Slice 2: Install Intent API

Add an API that accepts feature IDs and returns a generated Discord OAuth URL.

Acceptance:

- Backend recomputes all permissions.
- Intent is stored pending with expiration.
- URL contains `client_id`, `scope`, `permissions`, `integration_type`, `state`, `response_type`, and `redirect_uri`.
- Expired intents cannot be used.

### Slice 3: Landing Feature Picker

Replace the direct install link with a feature picker that calls the intent API.

Acceptance:

- User can select feature groups.
- UI shows requested Discord permissions.
- Start/install button uses backend-generated URL.
- No static permission bitfield is hard-coded in the UI.

### Slice 4: OAuth Callback

Add the callback that validates state, exchanges code, records guild install, persists selected features, and redirects to success/failure pages.

Acceptance:

- Invalid state fails.
- Expired intent fails.
- Intent can only be consumed once.
- Guild features are persisted only after successful Discord authorization.
- Trial and install audit still happen.

### Slice 5: Webhook Reconciliation

Update authorization webhook handling to cooperate with callback-based install binding.

Acceptance:

- Webhook can record install metadata.
- Webhook does not overwrite selected features.
- Callback and webhook are safe in either order.

### Slice 6: Privileged Discord Tool Restoration

Restore currently disabled privileged Discord commands and tools behind explicit feature gates. Keep lower-risk admin setup and audit surfaces separate from Discord permission-gated write tools.

Acceptance:

- Public command registration no longer hard-filters privileged Discord/admin commands once they are feature-gated.
- Router paths no longer return a generic disabled response for enabled privileged Discord features.
- Each restored privileged command checks guild feature status before existing role/admin checks.
- High-risk Discord write actions still require confirmation and Discord permission preflight.
- Existing skipped tests for disabled role management, channel management, moderation, ops, composed, and Discord-write controls are updated or replaced with active tests for enabled and disabled feature states.
- Any hard-disable code confirmed as legacy is removed after the feature-gated replacement is active.

### Slice 7: Runtime Feature Gates

Add feature checks to tool access, command handlers, background tasks, scheduled jobs, reminders, music, and composed tools.

Acceptance:

- Disabled features are not exposed to the model.
- Disabled slash-command paths return a clear denial.
- Existing role/channel/tool policy gates still apply.
- Discord permission preflight still runs for enabled Discord actions.
- Restored privileged Discord features are exposed only when selected for the guild and allowed for the caller.

### Slice 8: Admin Status And Reauthorization

Expose enabled features, missing Discord permissions, and generated reauthorization links.

Acceptance:

- Admins can see enabled and disabled features.
- Missing Discord permissions are listed clearly.
- Reauthorization link includes the union of enabled feature permissions.
- Feature changes are audited.
- Admin feature changes show whether additional Discord permissions or command-surface restoration are required.

### Slice 9: Existing Guild Migration

Backfill existing guilds to a deliberate default preset.

Acceptance:

- Existing guilds keep expected current behavior.
- Migration is auditable.
- Legacy feature flag meaning is removed or documented.

## Verification Plan

Use automated and local non-browser checks.

Test coverage:

- Feature catalog validation.
- Permission bitfield calculation.
- Install intent creation.
- OAuth callback state validation.
- Intent single-use semantics.
- Expired intent behavior.
- Guild feature persistence.
- Webhook/callback ordering.
- Runtime tool exposure with enabled and disabled features.
- Command denial for disabled features.
- Restored privileged Discord command behavior with feature enabled and disabled.
- Restored ops, schedule, composed-tool, role-management, channel-management, moderation, and Discord-write behavior with feature gates and caller permissions.
- Reauthorization URL generation.
- Existing guild migration/backfill.

Manual verification can be left to the user for browser and Discord UI behavior.

## Rollout Plan

1. Build behind a server-side config flag if production rollout needs staging.
2. Add callback URL and OAuth client secret configuration.
3. Deploy backend catalog, intent API, callback, and persistence.
4. Backfill existing guild feature sets.
5. Switch landing install button to feature picker.
6. Configure Discord custom install URL to Panda's install page.
7. Monitor install intent creation, callback success, webhook success, and missing permission rates.
8. Remove the old static install path after confirming the new path is stable.

## Open Decisions

- Which feature preset should existing guilds receive?
- Should "music" be selected by default or require explicit opt-in?
- Should moderation be one feature or several subfeatures?
- Should command visibility be adjusted with Discord command permissions, or should server-side handler gates be the only enforcement for V1?
- Should install intents be stored with hashed `state` values?
- Should Panda revoke returned OAuth tokens immediately after callback exchange?
- What exact success/failure pages should the landing app show after install?

## Done Definition

This feature is complete when:

- A user can choose features before install.
- Panda generates the Discord add link from those features.
- The installed guild is bound to the selected feature set through a validated install intent.
- Runtime behavior is gated by the saved guild feature set.
- Privileged Discord, admin setup, ops, schedule, and composed-tool surfaces are available when selected and denied when not selected.
- Discord permission drift is detected and reported.
- Existing guilds have an explicit migrated feature set.
- The old static install path is removed or clearly mapped to a default preset.
