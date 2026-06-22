# Panda Discord Assistant

Panda is a hosted Discord assistant sold per server. Server admins install Panda, start a trial, configure behavior and permissions in Discord, and upgrade when the server needs more included usage.

Customers do not need provider, search, hosting, or database accounts. Panda operates those services and enforces plan limits before paid work begins.

## What Panda Does

- Answers natural Discord messages when members mention Panda.
- Summarizes, explains, rewrites, and translates messages through natural chat or context menu actions.
- Uses server knowledge, memory consent, web search, schedules, reminders, composed tools, and music within the server's plan.
- Lets admins control channels, roles, tool access, response length, memory, billing, and audit history.
- Shows plan, renewal, AI response usage, web search usage, storage, and quota state through `/billing` and natural admin chat.

## Plans

| Plan | Price | AI responses | Web searches | Knowledge storage | Retention |
| --- | ---: | ---: | ---: | ---: | ---: |
| Trial | $0 for 14 days | 250 total | 20 total | 25 MB | 14 days |
| Starter | $19/server/month | 2,000/month | 100/month | 100 MB | 30 days |
| Plus | $49/server/month | 5,000/month | 400/month | 500 MB | 90 days |
| Pro | $99/server/month | 10,000/month | 1,000/month | 2 GB | 180 days |
| Business | $249/server/month | 25,000/month | 2,000/month | 10 GB | 365 days |

Trials do not auto-convert without payment approval. Servers use the included limits for their active subscription tier; when a limit is exhausted, paid usage pauses until renewal or a plan upgrade.

## Install Panda

1. Open the Panda install link from the landing page or Discord app directory.
2. Choose the Discord server.
3. Grant the requested Discord permissions.
4. Run `/billing` to confirm the trial and billing owner.
5. Ask Panda to review setup, usage, memory, web search, and permission state.

The installer becomes the billing owner for that server. The Discord server owner retains management access.

Implementation planning for feature-based pre-install customization lives in [Feature-Based Discord Install Customization Plan](FEATURE_INSTALL_CUSTOMIZATION_PLAN.md).

## Buy A Plan

The billing owner purchases from the Panda landing page:

- Connect a Solana wallet from a browser extension, or open the Solana Pay link in a mobile wallet.
- Panda creates a server-side payment order with the exact plan, treasury wallet, native SOL amount, memo, reference, and expiration.
- After the transaction is verified, the landing page reveals a one-time activation key.
- Run `/billing action:activate api_key:<key>` in the Discord server to apply the plan.

Panda grants plans only after the backend verifies the native SOL transaction against Solana RPC. Wallet connection, client-side UI state, redirects, and copied signatures never grant access by themselves.

## End-To-End Flow

The production path has three separate identities: the wallet payer, the Discord billing actor, and the Discord end user.

1. A server admin installs Panda from the landing page or Discord app directory.
2. Panda creates a trial for the Discord server. Members can use Panda within trial limits after admins finish setup.
3. When the server needs a paid plan, the billing owner opens the landing page and chooses a plan.
4. The landing page calls the Panda API to create a SOL payment order. The API returns the exact plan, lamports, treasury wallet, memo/reference, cluster, and expiration.
5. The payer connects a Solana wallet through Wallet Standard-compatible extension discovery, or opens the Solana Pay/mobile wallet link.
6. The wallet signs and sends the exact native SOL transfer. The landing page submits the resulting transaction signature to Panda.
7. Panda verifies the transaction server-side against Solana RPC. It must match the treasury wallet, native SOL amount, memo/reference, order, guild, plan, expiration, and confirmation threshold.
8. After verification, the landing page reveals a one-time activation key. The key is displayed once, stored only as a hash, scoped to the payment order, and can be revoked before use.
9. The billing owner, a guild admin claiming an unclaimed server, or a Panda operator runs `/billing action:activate api_key:<key>` in Discord.
10. Panda consumes the key atomically, records the SOL payment event, upserts the server subscription, writes a fresh entitlement snapshot, and marks the order activated.
11. End users keep using Panda in Discord. Every paid AI, web search, schedule, composed tool, storage, and music path checks the active entitlement and quota before provider spend.

The wallet proves payment only. Discord permissions decide who can activate the payment for a server, and entitlement checks decide what end users can do after activation.

## Admin Setup

Setup and administration are handled through natural Discord messages. Ask Panda what you want changed, review the prepared action, then use the confirmation button for sensitive writes.

- Ask Panda to set answer length, tool policy, channel rules, role profiles, tool access, server prompt, or personality.
- Ask Panda for billing status, usage, quota, audit history, setup warnings, support context, or safe data summaries.
- Use `/billing action:activate api_key:<key>` only for one-time activation keys, so secrets stay out of normal chat.
- Destructive or privileged changes use confirmation buttons before Panda acts.

Panda does not expose model names, provider names, fallback routing, token prices, or provider diagnostics to normal users or guild admins.

## Data And Safety

- User memory is off by default and requires consent.
- Server knowledge is admin-managed and counted against the active plan.
- Conversation retention follows the active plan unless a shorter policy is configured.
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
