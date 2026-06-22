# Panda Discord Assistant

Panda is a hosted Discord assistant sold per server. Server admins install Panda, start a trial, configure behavior and permissions in Discord, and upgrade when the server needs more included usage.

Customers do not need provider, search, hosting, or database accounts. Panda operates those services and enforces plan limits before paid work begins.

## What Panda Does

- Answers natural Discord messages when members mention Panda.
- Summarizes, explains, rewrites, and translates messages through slash commands or context menu actions.
- Uses server knowledge, memory consent, web search, schedules, reminders, composed tools, and music within the server's plan.
- Lets admins control channels, roles, tool access, response length, memory, billing, and audit history.
- Shows plan, renewal, AI response usage, web search usage, storage, and quota state through `/billing` and `/admin status`.

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
5. Run `/admin status` to review setup, usage, memory, web search, and permission state.

The installer becomes the billing owner for that server. The Discord server owner retains management access.

## Buy A Plan

The billing owner purchases from Discord:

- `/billing action:upgrade plan:starter|plus|pro|business` creates a Stripe Checkout session for the selected monthly plan.
- `/billing action:portal` opens Stripe Customer Portal after the first completed Stripe checkout records the server's customer ID.

Panda grants plans only from verified Stripe webhooks or Discord Premium App entitlement events. Client-side success redirects never grant access.

## Admin Setup

Common setup commands:

- `/admin behavior answer_length:brief|standard|detailed tool_policy:<policy>` changes response length and tool access.
- `/admin channel action:list|allow|deny|remove channel:#channel` limits where Panda can answer.
- `/admin role action:list|set|remove profile:admin|moderator role:@Role` maps Panda admin and moderator profiles.
- `/admin tool action:list|add|remove tool_name:<tool> role:@Role` narrows tool access.
- `/admin prompt` sets server instructions.
- `/admin billing` or `/billing` shows plan, renewal, usage, quota, checkout, and portal actions.
- `/admin audit` shows recent privileged changes.
- `/support` creates a safe support bundle without raw prompts or raw Discord messages.
- `/data export` shows a safe data export summary.
- `/data delete scope:knowledge|memory|conversations|billing|all` deletes scoped Panda data after confirmation.

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

This repository also contains the operator runtime for Panda. Deployment, backups, billing webhooks, spend alerts, restore drills, and internal provider configuration are documented in `OPERATIONS.md`.

Do not send operator runbooks, provider configuration, model routing, cost math, or hidden diagnostics to customers unless an approved enterprise contract explicitly requires it.
