# Panda Discord Assistant

Panda is a hosted Discord assistant sold per server. Server admins install Panda, start a trial, configure behavior and permissions in Discord, and buy credit packs when the server needs more paid usage.

Customers do not need provider, search, hosting, or database accounts. Panda operates those services and reserves server credits before paid work begins.

## What Panda Does

- Answers natural Discord messages when members mention Panda.
- Summarizes, explains, rewrites, and translates messages through natural chat or context menu actions.
- Uses server knowledge, memory consent, web search, schedules, reminders, composed tools, and music within the server's credit balance.
- Lets admins control channels, roles, tool access, response length, memory, billing, and audit history.
- Shows pack, credits, storage, retention, and billing state through `/billing` and natural admin chat.

## Credit Packs

| Pack | Price | Credits | Knowledge storage | Retention | Credit expiry |
| --- | ---: | ---: | ---: | ---: | ---: |
| Trial | $0 | 1,500 | 25 MB | 14 days | 14 days |
| Starter | $19 | 10,000 | 100 MB | 30 days | 30 days |
| Plus | $49 | 30,000 | 500 MB | 90 days | 90 days |
| Pro | $99 | 75,000 | 2 GB | 180 days | 180 days |
| Business | $249 | 220,000 | 10 GB | 365 days | 365 days |

Trials do not auto-convert without payment approval. Servers spend prepaid credits for paid actions; when credits are exhausted, paid usage pauses until the billing owner activates another verified SOL payment.

## Install Panda

1. Open the Panda install link from the landing page or Discord app directory.
2. Choose the Discord server.
3. Grant the requested Discord permissions.
4. Run `/billing` to confirm the trial and billing owner.
5. Ask Panda to review setup, usage, memory, web search, and permission state.

The installer becomes the billing owner for that server. The Discord server owner retains management access.

Implementation planning for full Discord server setup automation lives in [Discord Server Setup Automation Plan](DISCORD_SERVER_SETUP_PLAN.md).

## Buy A Pack

The billing owner purchases from the Panda landing page:

- Connect a Solana wallet through a Wallet Standard-compatible browser extension.
- Panda creates a server-side payment order with the exact pack, credits, treasury wallet, native SOL amount, memo, reference, and expiration.
- After the transaction is verified, the landing page reveals a one-time activation key.
- Run `/billing action:activate api_key:<key>` in the Discord server to grant the pack credits.

Panda grants pack credits only after the backend verifies the native SOL transaction against Solana RPC. Wallet connection, client-side UI state, redirects, and copied signatures never grant access by themselves.

## End-To-End Flow

The production path has three separate identities: the wallet payer, the Discord billing actor, and the Discord end user.

1. A server admin installs Panda from the landing page or Discord app directory.
2. Panda creates a trial for the Discord server. Members can use Panda within trial limits after admins finish setup.
3. When the server needs more paid usage, the billing owner opens the landing page and chooses a pack.
4. The landing page calls the Panda API to create a SOL payment order. The API returns the exact pack, credits, lamports, treasury wallet, memo/reference, cluster, and expiration.
5. The payer connects a Solana wallet through Wallet Standard-compatible extension discovery.
6. Panda builds the exact native SOL transfer, the wallet signs it, and the backend submits it through Solana RPC.
7. Panda tracks the submitted signature and verifies the finalized transaction server-side. It must match the treasury wallet, native SOL amount, memo/reference, order, guild, pack, expiration, and confirmation threshold.
8. After verification, the landing page reveals a one-time activation key. The key is displayed once, stored only as a hash, scoped to the payment order, and can be revoked before use.
9. The billing owner, a guild admin claiming an unclaimed server, or a Panda operator runs `/billing action:activate api_key:<key>` in Discord.
10. Panda consumes the key atomically, records the SOL payment event, grants the pack credits once, and marks the order activated.
11. End users keep using Panda in Discord. Every paid AI, web search, schedule, composed tool, storage, and music path reserves credits before provider spend.

The wallet proves payment only. Discord permissions decide who can activate the payment for a server, and entitlement checks decide what end users can do after activation.

## Admin Setup

Setup and administration are handled through natural Discord messages. Ask Panda what you want changed, review the prepared action, then use the confirmation button for sensitive writes.

- Ask Panda to set answer length, tool policy, channel rules, role profiles, tool access, server prompt, or personality.
- Ask Panda for billing status, credit usage, audit history, setup warnings, support context, or safe data summaries.
- Use `/billing action:activate api_key:<key>` only for one-time activation keys, so secrets stay out of normal chat.
- Destructive or privileged changes use confirmation buttons before Panda acts.

Configured bot owners from `OWNER_USER_IDS` or `discord.owner_user_ids` are Panda admins in every server where Panda is installed. Use Discord user IDs, not usernames or display names.

Panda does not expose model names, provider names, fallback routing, token prices, or provider diagnostics to normal users or guild admins.

## Data And Safety

- User memory is off by default and requires consent.
- Server knowledge is admin-managed and bounded by the active pack.
- Conversation retention follows the active pack unless a shorter policy is configured.
- Suspended or canceled servers keep help, billing, export, delete, and support access while paid AI/search features are disabled.
- Support bundles avoid raw prompts, raw Discord messages, provider model names, API keys, and billing secrets by default.

## Legal And Support

- Terms: `/terms`
- Privacy: `/privacy`
- Data Processing Addendum: `/dpa`
- Refund and Cancellation Policy: `/refunds`
- Acceptable Use Policy: `/acceptable-use`
- Security and Vulnerability Disclosure: `/security`
- Status and incidents: `/status`
- Support: `/support`

## Operator Notes

This repository also contains the operator runtime for Panda. Deployment, backups, SOL payment verification, spend alerts, restore drills, and internal provider configuration are documented in `OPERATIONS.md`.

Do not send operator runbooks, provider configuration, model routing, cost math, or hidden diagnostics to customers unless an approved enterprise contract explicitly requires it.
