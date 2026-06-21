# Panda SaaS Product Conversion Plan

## Reader And Outcome

This plan is for the engineer turning Panda from a self-hosted, model-choice Discord bot into a hosted SaaS product. After reading it, they should be able to implement billing, plan entitlements, usage controls, model secrecy, support operations, and the required product copy changes without accidentally preserving legacy "bring your own model" behavior.

This is an internal implementation plan. Do not publish provider names, model IDs, internal routing rules, cost math, or vendor fallback details in customer-facing docs, bot responses, Discord command help, or normal admin surfaces.

## Product Decision

Panda becomes a hosted, per-server Discord assistant. Customers buy a subscription for a Discord server. Panda owns the LLM provider account, model selection, fallback routing, search budget, abuse controls, and support obligations.

The old customer promise was "choose any model through OpenRouter." The SaaS promise should be "install Panda, configure server behavior, and get a reliable Discord-native assistant with predictable usage limits." Model choice becomes an internal operations concern, not a customer feature.

Core principles:

- Server admins configure what Panda may do, not which model Panda uses.
- Normal users never see model names, provider names, fallback names, token prices, or model diagnostics.
- Usage is sold as plan credits: AI responses, web searches, knowledge storage, schedules, and retention.
- Costs are guarded by quotas, rate limits, and overage gates before provider spend can run away.
- Every paid entitlement must have a clear degraded state when unpaid, over quota, or payment-failed.
- Legacy model-choice code should be confirmed as legacy during implementation and removed, not hidden behind unused flags.

## Current State

Panda already has several SaaS-ready foundations:

- Discord install ownership and guild metadata.
- Per-guild configuration and admin role mapping.
- Usage events with token counts, latency, success, and command dimensions.
- Durable request budgets for global, guild, user, and channel scopes.
- Audit events for privileged changes.
- Health, readiness, and metrics endpoints.
- A landing site.
- Web search as an optional Brave-backed tool.
- Memory, knowledge, schedules, alerts, feedback, music, and composed tools.

The main SaaS gaps are billing, entitlements, plan limits, customer-facing onboarding, multi-tenant support workflows, public legal docs, cost reporting, and removal of customer-visible model controls.

## Cost Baseline

Verified on 2026-06-21.

Sources:

- [OpenRouter pricing](https://openrouter.ai/pricing): pay-as-you-go has a 5.5% platform fee, input/output tokens are billed per model at posted rates, and fallback routing bills only the successful run.
- [OpenRouter Models API](https://openrouter.ai/docs/api/api-reference/models/get-models): the API returns per-model `pricing.prompt` and `pricing.completion` fields. The current internal default returned by the live API at verification time: prompt `$0.00000009` per token, completion `$0.00000018` per token, cached input `$0.00000002` per token.
- [Brave Search API pricing](https://api-dashboard.search.brave.com/documentation/pricing): Search is `$5.00` per 1,000 requests and includes `$5` monthly credits.
- [Fly.io pricing](https://fly.io/docs/about/pricing/) and [Fly cost management](https://fly.io/docs/about/cost-management/): started machines are usage-billed; Fly's examples put one shared-1x 256MB machine in `sjc` at `$2.32/month` full-time, and three shared-1x 1GB machines in `sjc` at `$20.37/month`.
- [Stripe pricing](https://stripe.com/pricing): domestic online cards are `2.9% + 30c` per successful transaction.
- [Discord Premium Apps monetization requirements](https://support-dev.discord.com/hc/en-us/articles/23810643331735-Premium-Apps-Required-Support-for-Monetizing-Apps): paid app benefits must be purchasable through Premium Apps where supported at a final price no higher than other payment options. Discord's Growth Tier platform fee is 15% for the first $1M, with a 30% standard tier after that, plus payment processing and transaction fees.

Cost unit for planning:

- One normal AI response is modeled as 5,000 input tokens plus 1,000 output tokens.
- Raw model cost for that response is about `$0.00063`.
- Use `$0.00079` as the planning cost after adding 25% buffer for natural-trigger classification, retries, provider platform fees, and prompt overhead.
- That makes 1,000 normal AI responses cost about `$0.79`.
- One Brave Search call costs `$0.005`.

These assumptions should be replaced with measured p50, p90, and p99 production cost per response once real SaaS traffic exists. Pricing should be reviewed any time model/provider pricing changes by more than 20%.

## Recommended Launch Pricing

Use the same public price through Discord Premium Apps and any off-platform checkout to satisfy Discord price parity. Prefer monthly plans first because Premium Apps support varies by SKU type and cadence.

| Plan | Price | Included AI responses | Included web searches | Knowledge storage | Retention | Target customer |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| Trial | `$0` for 14 days | 500 total | 25 total | 25 MB | 14 days | Evaluation only |
| Starter | `$9/server/month` | 2,000/month | 100/month | 100 MB | 30 days | Small communities |
| Plus | `$29/server/month` | 10,000/month | 500/month | 500 MB | 90 days | Active communities |
| Pro | `$79/server/month` | 25,000/month | 1,000/month | 2 GB | 180 days | Large or work-heavy servers |
| Business | `$199/server/month` | 75,000/month | 3,000/month | 10 GB | 365 days | High-volume teams |

Overages:

- Do not enable automatic overages by default.
- Let admins buy usage packs before hard cutoff: `$5` for 2,500 AI responses, `$5` for 500 web searches, `$10` for 5 GB additional knowledge storage.
- Business can request invoice billing and custom limits after a usage review.
- Free trials should never auto-convert without explicit payment approval.

Approximate gross margin using the conservative response unit:

| Plan | Public price | AI cost | Search cost | Allocated infra | Stripe direct processing | Estimated direct cost | Direct gross margin | Estimated Discord net | Discord-channel margin |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Starter | `$9` | `$1.57` | `$0.50` | `$0.50` | `$0.56` | `$3.14` | 65% | `$7.15` | 64% |
| Plus | `$29` | `$7.87` | `$2.50` | `$1.25` | `$1.14` | `$12.77` | 56% | `$23.05` | 50% |
| Pro | `$79` | `$19.69` | `$5.00` | `$3.00` | `$2.59` | `$30.28` | 62% | `$62.80` | 56% |
| Business | `$199` | `$59.06` | `$15.00` | `$8.00` | `$6.07` | `$88.13` | 56% | `$158.19` | 48% |

The "Discord net" column uses Discord's representative 79.49% net from its monetization example. Actual payout can vary by taxes, processing, refunds, chargebacks, and growth-tier eligibility.

## Model Secrecy And Routing

The SaaS product must remove customer control over model routing.

Remove from customer/admin surfaces:

- `/admin model` arguments for model slug and fallback models.
- Help text that documents model slugs, fallback routing, OpenRouter, or model provider names.
- Admin status lines that display default model or fallback model counts.
- Usage reports grouped by model.
- LLM tool outputs that expose `default_model`.
- Feedback reports that show model IDs to guild admins.
- Landing site sections that market "choose any model."
- README/setup language that tells customers to create an OpenRouter key.

Replace with customer-safe controls:

- "Response style" or "Panda personality" instead of model selection.
- "Answer length" presets instead of raw max token fields.
- "Tool access policy" and "web search allowed" settings.
- "Reliability mode" only if it maps to plan entitlements and does not name models.
- "Usage remaining" and "web searches remaining" instead of token/model detail.

Internal routing design:

- Store internal model policy in operator-only config, not guild config.
- Use plan-aware inference profiles such as `standard`, `priority`, and `restricted`, but never expose their provider details.
- Keep provider/model names in internal logs and metrics only when access is operator-only and logs are not shared with customers.
- If a model fails, user-facing copy should say "Panda is having trouble with AI responses right now" rather than naming the provider.
- Customer export data may include token counts and costs, but should not include model IDs unless this becomes a contractual enterprise feature.
- Provider and model IDs should be redacted from normal support screenshots and Discord-visible diagnostics.

Implementation touch points:

- Discord slash command registration.
- Command router help and admin command handling.
- Admin service model configuration methods.
- Guild config schema and migration strategy.
- Assistant model sequence and fallback routing.
- Usage breakdown dimensions.
- Admin status, setup checks, ops output, and health copy.
- Tool registry/executor config-reading tools.
- Feedback and curation metadata views.
- Composed tool spec validation if specs can currently select a model.
- Landing content, metadata, and navigation.
- README, operations docs, and environment examples.

## Billing And Entitlements

Add a billing domain that resolves one active subscription per guild.

Entities:

- Customer account: billing owner, email, tax country, support contact.
- Guild subscription: guild ID, plan, status, renewal date, trial end, cancel-at-period-end flag.
- Entitlement snapshot: limits for AI responses, web searches, storage, schedules, retention, music, and premium tools.
- Invoice/payment event: provider, external ID, amount, status, idempotency key.
- Usage period: plan period start/end, consumed AI responses, consumed web searches, consumed storage, overage packs.
- Grace state: active, trialing, past_due, grace, read_only, suspended, canceled.

Payment channels:

- Implement Discord Premium Apps support for in-Discord purchases where available.
- Optionally implement Stripe Checkout/Customer Portal for web purchases, but keep prices and benefits identical to Discord where Discord support is required.
- Webhooks must be idempotent, replay-safe, and auditable.
- Do not grant entitlements directly from client-side success redirects. Grant only from verified webhook events or Discord entitlements.

Access rules:

- Trial, active, and grace subscriptions can answer until quotas are exhausted.
- Past-due accounts get a short grace window, then read-only admin access.
- Suspended/canceled guilds can run `/help`, billing/status, export/delete, and support commands, but cannot call paid AI/search features.
- Guild owner and installer can manage billing, but local Discord admins should not be able to steal billing ownership without a verified handoff.

## Usage Metering

Current token usage is a good start, but SaaS needs a cost ledger.

Add metering for:

- Customer-visible AI response count.
- Internal LLM API call count, including natural trigger classification and tool follow-up calls.
- Prompt, completion, cached input, and total token counts.
- Estimated internal cost at time of request.
- Final provider cost when available.
- Web search calls.
- Attachment extraction size and text tokenization cost.
- Knowledge embedding cost if embeddings are enabled.
- Storage bytes by guild.
- Scheduled/composed tool runs.
- Music playback minutes if music remains in paid plans.

Rules:

- Customer quotas should count user-visible value, not every internal retry.
- Internal cost ledgers should count every provider call.
- Failed provider attempts should be tracked separately even when not billed.
- Web search should have a separate quota because it is materially more expensive than a normal AI response.
- Long attachment or knowledge operations should consume multiple AI response units based on token size.
- Monthly quota reset follows the billing period, not calendar month.

Admin-visible reporting:

- Show plan, renewal date, AI responses used/remaining, web searches used/remaining, storage used, and retention.
- Show top users/channels/commands by response count.
- Do not show model/provider breakdown.
- Show "cost risk" only to owner/operators, not guild admins, unless an enterprise contract asks for it.

## Product Surface Changes

Landing site:

- Replace open-source/model-choice positioning with hosted SaaS positioning.
- Add pricing, install CTA, privacy, terms, support, and status links.
- Remove model carousel and model-provider language.
- Explain plans in terms of server usage, not tokens.
- Add a "security and data" section that names retention controls, export/delete, and admin-managed knowledge.

Discord onboarding:

- Install flow records owner, installer, guild, and trial status.
- First-run setup explains plan, remaining trial credits, recommended channels, role mapping, memory, web search, and billing owner.
- `/admin status` becomes the main server health and subscription page.
- Add `/billing` or `/admin billing` for plan, upgrade, cancel, portal, and quota pack links.
- Add clear over-quota messages with upgrade links.

Public docs:

- Split self-hosting docs from SaaS customer docs.
- SaaS customer docs must not mention OpenRouter, model slugs, API keys, fallback models, or provider costs.
- Self-hosting docs may remain only if the product intentionally keeps an open-source/self-hosted edition. If not, confirm that path is legacy and remove or archive it.

## Privacy, Security, And Legal

Must-have launch docs:

- Terms of service.
- Privacy policy.
- Data processing addendum template for Business customers.
- Refund/cancellation policy.
- Acceptable use policy covering abuse, spam, harassment, illegal content, and automated mass messaging.
- Security contact and vulnerability disclosure route.

Controls:

- Keep Discord bot token, provider API keys, webhook secrets, and billing secrets in the deployment secret manager.
- Separate production, staging, and local provider keys with hard spend caps.
- Configure provider privacy settings to disallow training/retention where available.
- Treat Discord content, attachments, web results, and tool results as untrusted model context.
- Keep per-guild tenant isolation in every repository query.
- Add export/delete flows for guild knowledge, user memory consent, conversation metadata, and billing account deletion.
- Store only previews/hashes for conversation content unless retention requires more.
- Keep audit logs longer than message content.
- Add admin-visible retention controls by plan.
- Add owner/operator-only incident mode for provider outages and abuse spikes.

## Reliability And Operations

MVP can run on the current single-primary architecture while traffic is small, but the SaaS plan should include an explicit scale path.

MVP operations:

- One production bot app with one primary writable database.
- One landing app.
- Scheduled SQLite backups copied off the Fly volume.
- Spend alerts on OpenRouter, Brave, Fly, Stripe/Discord, and any email/support tooling.
- Provider budget caps per environment.
- Daily usage/cost reconciliation job.
- Alert when gross margin drops below 45% for any plan cohort.
- Alert when a guild consumes more than 50% of its included quota in the first 20% of a billing period.

Scale path:

- Move from SQLite to Postgres or a clearly owned single-writer SQLite/LiteFS design before horizontal writers.
- Split gateway, HTTP, scheduler, and worker processes.
- Add shard-aware Discord gateway operations.
- Add queue backpressure, dead-letter queues, and per-guild concurrency caps.
- Add object storage for durable attachments if attachment retention becomes a paid feature.
- Add internal admin dashboard for customer support, entitlement inspection, refunds, and abuse controls.

## Support And Customer Success

Support surfaces:

- In-Discord support command that creates a support bundle without raw content by default.
- Email support for paid customers.
- Status page for provider/search/platform incidents.
- Operator-only "guild diagnostic" command for configuration, quota, payment state, and recent failures.

Support bundles may include:

- Guild ID, plan, subscription status, quota usage, command failure counts, recent error codes, queue depth, and Discord permission gaps.
- They must not include raw prompts, raw Discord messages, provider model names, API keys, billing secrets, or hidden admin-only tools.

## Implementation Phases

### Phase 1: Lock Product Policy

- Decide whether self-hosting remains a supported edition.
- Confirm customer model choice is legacy for SaaS.
- Define plan entitlements and quota names.
- Define exact public pricing and whether annual discounts exist.
- Define refund, trial, cancellation, and grace-period policy.

Exit criteria:

- Product policy is written.
- No new customer-facing work mentions model/provider choice.
- Pricing and limits match this plan or have an explicitly recorded replacement.

### Phase 2: Remove Model Exposure

- Remove or replace `/admin model` customer controls.
- Remove model/fallback lines from help, admin status, setup, tools, and reports.
- Replace guild model settings with internal inference profiles.
- Keep operator-only model routing and fallback config.
- Update tests to assert model names are not present in normal responses.

Exit criteria:

- A guild admin cannot configure, list, infer, or ask Panda to reveal the active model.
- Normal usage reports cannot group by model.
- Landing and customer docs do not mention model providers.

### Phase 3: Add Entitlements And Metering

- Add subscription, entitlement, invoice event, usage period, and quota pack storage.
- Add entitlement resolver to every assistant/search/tool/schedule entry point.
- Add monthly quota windows by billing period.
- Add customer-visible usage summaries.
- Add internal cost ledger and spend alerts.

Exit criteria:

- A trial guild can use included credits.
- An unpaid or over-quota guild is denied before any provider call.
- Operators can reconcile provider bills against internal cost records.

### Phase 4: Billing Channels

- Add Discord Premium Apps entitlement ingestion.
- Add Stripe Checkout/Portal only if off-platform billing is still desired.
- Add idempotent webhook processing.
- Add billing owner flows and verified ownership transfer.
- Add upgrade/downgrade/cancel/grace/suspend behavior.

Exit criteria:

- Payment success grants the correct guild entitlement.
- Payment failure eventually suspends paid AI/search access.
- Discord and off-platform prices stay in parity.

### Phase 5: SaaS Onboarding And Public Copy

- Replace landing copy with hosted SaaS copy.
- Add pricing page sections.
- Add install CTA and setup flow.
- Add terms/privacy/support/status links.
- Update README/docs split between SaaS and any remaining self-hosted edition.

Exit criteria:

- A new customer can discover, install, trial, upgrade, and see usage without operator help.
- Public docs do not ask customers to create provider API keys.

### Phase 6: Operations And Launch Readiness

- Add backups, restore drills, spend alerts, abuse limits, support bundles, incident mode, and status page.
- Add production monitoring for quota checks, provider cost, payment webhooks, Discord gateway health, and queue depth.
- Run a closed beta with hard provider caps before opening paid self-serve.

Exit criteria:

- A provider outage, webhook replay, quota spike, failed payment, and restore drill have been tested.
- Support can answer "why did this guild stop working?" without database spelunking.
- Gross margin is measured weekly against actual provider invoices.

## Acceptance Checklist

- Customers can pay for Panda without creating OpenRouter, Brave, or Fly accounts.
- Normal users and guild admins cannot change or see the model.
- Admins can configure server behavior, permissions, memory, web search, billing, and quotas.
- Every provider-spend path checks entitlement and quota before calling the provider.
- Usage and cost ledgers reconcile with provider dashboards.
- Public pricing is profitable under Stripe direct billing and Discord Premium Apps payouts.
- Web search has its own quota and cannot silently dominate COGS.
- Trial abuse is limited by guild, installer, payment method, and provider spend caps.
- Suspended guilds retain export/delete/billing/support access.
- Public docs, landing copy, help text, and setup no longer market model choice.
- Operator docs preserve the internal model/cost details needed to run the business.
