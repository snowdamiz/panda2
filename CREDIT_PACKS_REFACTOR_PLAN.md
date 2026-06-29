# Credit Packs Refactor Plan

Pricing inputs were checked on 2026-06-28. This plan converts Panda from plan/subscription quota buckets to prepaid server credit packs paid through SOL. The goal is predictable user-facing billing, fewer provider line-item surprises, and no hard-coded intent or model fallbacks.

## Reader And Outcome

Reader: internal Panda engineer/operator.

After reading this, the reader should be able to implement the credit ledger in phases, migrate existing paid guilds, rename subscription language to packs in one clustered UI pass, and set initial pack prices with a clear cost basis.

## Current Billing Shape

Panda currently sells monthly per-server plans with separate quotas for AI responses, web searches, image generations, knowledge storage, schedules, and music. SOL checkout creates a billing order for a plan, reveals one activation key, then creates or updates a guild subscription.

The runtime already has useful primitives:

- Usage reservations protect paid work before execution.
- A cost ledger records provider, model, token counts, estimated cost, final cost, success, and error code.
- OpenRouter chat/image calls and Brave search already record provider costs in several paths.
- Image generation already receives provider-returned final cost in micros.

The missing piece is that user-facing quotas are independent buckets while provider costs are not. A single Discord request can burn several assistant model rounds, a web search, image inspection or generation, YouTube transcription, clip planning, render CPU, storage writes, and a final response. Credits should reserve and settle at the action boundary.

## Pricing Inputs

Sources:

- [OpenRouter Models API](https://openrouter.ai/docs/api/api-reference/models/get-models) and [OpenRouter image generation docs](https://openrouter.ai/docs/guides/overview/multimodal/image-generation)
- Live OpenRouter endpoint lookups:
  - `https://openrouter.ai/api/v1/models/openai/gpt-oss-120b/endpoints`
  - `https://openrouter.ai/api/v1/models/google/gemini-3.1-flash-lite/endpoints`
  - `https://openrouter.ai/api/v1/models/google/gemini-3.5-flash/endpoints`
  - `https://openrouter.ai/api/v1/images/models/google/gemini-3.1-flash-image/endpoints`
- [Brave Search API](https://brave.com/search/api/)
- [Lemonfox Whisper API pricing](https://www.lemonfox.ai/whisper-api)
- [Fly.io pricing](https://fly.io/docs/about/pricing/)
- [Cloudflare R2 pricing](https://developers.cloudflare.com/r2/pricing/)
- SOL quote: 1 SOL = $70.70 at the time of calculation.

Model and provider rates used:

| Workload | Configured provider/model | Cost input |
| --- | --- | --- |
| Assistant chat and tool routing | OpenRouter `openai/gpt-oss-120b`, provider order `cerebras` | $0.35 / 1M input tokens, $0.75 / 1M output tokens |
| Image generation | OpenRouter image API `google/gemini-3.1-flash-image` | $0.50 / 1M input tokens, $3.00 / 1M text output tokens, $0.00006 / output-image token |
| Image inspection | Same configured image model through chat analysis | Token-priced, final cost available from provider usage |
| YouTube clip detection | `google/gemini-3.1-flash-lite` when configured | $0.25 / 1M input tokens, $1.50 / 1M output tokens |
| YouTube clip composition | `google/gemini-3.5-flash` | $1.50 / 1M input tokens, $9.00 / 1M output tokens, image input charged separately |
| Web search | Brave Search API search plan | $5 / 1,000 requests |
| Transcription | Lemonfox speech-to-text | $0.17 / audio hour |
| Generated clip storage | R2 standard | $0.015 / GB-month, free direct R2 egress, operations mostly covered by free tier at current scale |
| Bot hosting | Fly Machines and volumes | Allocate as fixed platform overhead; do not expose as a customer action |

Representative cost estimates:

| Estimate | Calculation | Provider cost |
| --- | --- | --- |
| Simple assistant model round | 4,000 input tokens * $0.35/M + 900 output tokens * $0.75/M | $0.002075 |
| Brave search call | $5 / 1,000 requests | $0.005000 |
| 10 minutes transcription | 10 * $0.17 / 60 | $0.028333 |
| Clip detection, large transcript | 45,000 input tokens on Flash Lite + 8,192 output tokens | $0.023538 |
| One composition planning call | 12,000 input tokens + 4,096 output tokens on Gemini 3.5 Flash, before image-token variability | $0.054864 |
| Default 1K image generation assumption | 1,290 output-image tokens * $0.00006 | $0.077400 |

The image generation estimate is intentionally conservative. Final image costs should still be recorded from provider usage and used to recalibrate rates.

## Credit Unit And Pack Catalog

Credits are an internal prepaid unit, not a USD peg. Pack size sets the effective retail value per credit. This keeps the largest pack profitable while still giving larger servers a volume discount.

Proposed packs:

| Pack | USD target | Credits | Effective price / credit | SOL at $70.70 | Lamports at $70.70 | Retention cap | Knowledge cap |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Trial Pack | $0 | 1,500 | n/a | 0 | 0 | 14 days | 25 MB |
| Starter Pack | $19 | 10,000 | $0.001900 | 0.268779 SOL | 268,779,177 | 30 days | 100 MB |
| Plus Pack | $49 | 30,000 | $0.001633 | 0.693167 SOL | 693,167,350 | 90 days | 500 MB |
| Pro Pack | $99 | 75,000 | $0.001320 | 1.400481 SOL | 1,400,480,973 | 180 days | 2 GB |
| Business Pack | $249 | 220,000 | $0.001132 | 3.522422 SOL | 3,522,421,842 | 365 days | 10 GB |

Current configured lamports under-price the public USD labels at the checked SOL price:

| Current item | Configured SOL | USD at $70.70 |
| --- | ---: | ---: |
| starter | 0.25 | $17.67 |
| plus | 0.65 | $45.95 |
| pro | 1.35 | $95.45 |
| business | 3.35 | $236.85 |

Recommendation: order creation should price packs from a USD target plus a fresh SOL/USD quote, then lock lamports on the order. Keep manual `SOLANA_PACK_LAMPORTS` overrides for emergency operation, but do not treat fixed lamports as the product catalog source of truth.

## Action Credit Schedule

Credits should be reserved before work begins, committed on success, released on failure, and optionally adjusted when provider final cost proves a rate is wrong.

| Action | User-facing debit | Why |
| --- | ---: | --- |
| Assistant model round | 4 credits | Covers a normal `gpt-oss-120b` Cerebras round with about 54 percent gross margin even in Business Pack pricing. |
| Long-context assistant surcharge | +1 credit per additional 1,000 input tokens beyond 4,000, +1 per additional 500 output tokens beyond 900 | Prevents very long Discord context/tool loops from being subsidized by small prompts. |
| Natural wake/routing check that produces no visible reply | 1 credit | Covers silent model gating without making invisible work as expensive as a full reply. Show it as "routing check" in usage. |
| Web search | 8 credits plus assistant rounds | Brave costs $0.005/search; 8 credits keeps roughly 45 percent margin in Business Pack pricing. |
| Image inspection | 25 credits plus assistant rounds | Vision can include image tokens and a final answer round, but is much cheaper than generation. |
| Image generation, 512 | 75 credits | Resolution-sensitive reservation for cheaper outputs. |
| Image generation, 1K/default | 150 credits | Covers about $0.077 provider image-output cost with margin at all pack tiers. |
| Image generation, 2K | 500 credits | Protects higher image-token output. Recalibrate from live final costs. |
| Image generation, 4K | 1,200 credits | Protects large image-token output. Recalibrate from live final costs. |
| YouTube search/options | 3 credits | Local/ytdlp and HTTP work; no paid provider unless followed by summary/clip. |
| YouTube summary transcription | 20 credit base + 4 credits/audio minute, rounded up | Covers Lemonfox, download/ffmpeg CPU, and operational overhead. Assistant summary rounds are charged separately. |
| YouTube clip generation | 250 credit base + 5 credits/audio minute + 200 credits/rendered clip | Covers transcription, clip detection, composition planning, ffmpeg render CPU, R2 upload, and failures/retries. |
| Knowledge write | 2 credits per 10 KB added, plus storage rent | Covers chunking and optional embeddings when enabled. |
| Knowledge storage rent | 1 credit per MB-month, charged daily, rounded up per guild/day | Converts storage from a static quota into an ongoing resource. |
| Scheduled/composed tool run | 2 credit base plus any actions it invokes | Keeps background automation visible and bounded. |
| Music playback | 10 credits/hour, charged in 5-minute increments | Covers long-lived voice session CPU/bandwidth without dominating normal chat use. |

Margin checks at the largest pack price:

| Action | Credits | Revenue at Business Pack rate | Representative provider cost | Gross margin before platform overhead |
| --- | ---: | ---: | ---: | ---: |
| Assistant model round | 4 | $0.004528 | $0.002075 | 54 percent |
| Web search | 8 | $0.009055 | $0.005000 | 45 percent |
| Default image generation | 150 | $0.169773 | $0.077400 | 54 percent |

## Refactor Architecture

Replace "subscription as entitlement" with "server credit account plus pack grants".

New domain objects:

- Credit account: one per Discord guild, owns available, reserved, depleted, read-only, suspended, and support/export state.
- Pack catalog: static code/config catalog for purchasable packs, trial grants, display copy, credits, USD target, retention cap, knowledge cap, and expiry policy.
- Credit grant: immutable credit addition from trial, SOL order, coupon, admin adjustment, or migration.
- Credit reservation: pre-execution hold for one action. It has action type, request ID, expected credits, max credits, status, expiration, and metadata.
- Credit ledger entry: immutable credit movement. Types: grant, reserve, commit, release, adjustment, refund, expiry, storage_rent.
- Cost ledger event: keep the existing provider-cost trail and link it to request ID/reservation ID/action type.

Credit calculation should live in one billing package service:

- `QuoteAction(action, metadata) -> CreditQuote`
- `BeginCreditUsage(guild, quote) -> Reservation`
- `CommitCreditUsage(reservation, finalCost) -> LedgerEntry`
- `ReleaseCreditUsage(reservation, reason)`
- `GrantPack(order)`
- `ExpireCredits(now)`
- `ChargeStorageRent(now)`

Do not scatter credit math in tools. Tools should ask billing for a quote and reservation, then pass provider usage/cost back when done.

## Phased Implementation Play

The implementation should delay most UI work until the credit API, action names, and pack terminology are stable. Do backend, ledger, runtime, and migration work first. Then run the Discord, landing, checkout, and admin UI phases consecutively so visible wording changes happen in one focused sweep.

### Phase 0: Freeze Decisions And Names

Purpose: lock the credit vocabulary and prevent half-renamed billing concepts.

Work:

- Confirm the initial pack catalog, action credit schedule, expiration policy, storage-rent policy, and SOL pricing source.
- Create a single terminology map for code, API, database, Discord copy, landing copy, and admin copy.
- Decide whether the runtime will support both old quotas and new credits during a short migration window, or whether migration happens during a deploy maintenance window.

Exit criteria:

- Engineers can name every new domain object without using subscription language.
- Pack/action prices are accepted as launch defaults.
- Any old plan/subscription term that remains after this phase is explicitly marked for removal in a later phase.

### Phase 1: Credit Domain And Schema

Purpose: add the durable credit primitives without changing user-facing behavior.

Work:

- Add credit accounts, credit grants, credit reservations, and credit ledger entries.
- Add pack identity to payment orders, coupons, activation keys, and admin-facing billing records.
- Keep the existing provider cost ledger, but add request/reservation/action linkage.
- Add the central billing service methods: quote action, begin usage, commit usage, release usage, grant pack, expire credits, and charge storage rent.
- Add unit tests for ledger idempotency, reservation expiry, grant uniqueness, and negative-balance prevention.

Exit criteria:

- Credit tables and services exist behind tests.
- No paid action is enforced by credits yet.
- Existing subscription/quota behavior remains unchanged while the new data model proves itself.

### Phase 2: Pack Catalog And SOL Checkout Backend

Purpose: make packs purchasable and grantable before swapping runtime enforcement.

Work:

- Move product catalog authority to pack definitions with USD target, credits, retention cap, knowledge cap, and optional expiry.
- Change order creation to accept `pack`, lock lamports on the order, and return credits plus pack metadata.
- Support operator lamport overrides for emergencies, but keep USD target plus fresh SOL quote as the preferred source.
- Activation grants credits exactly once and records a payment event.
- Add tests for pack pricing, lamport locking, activation idempotency, duplicate payment handling, and coupon/admin grants.

Exit criteria:

- New orders can create credit grants in test paths.
- The backend no longer needs plan quota definitions to price a new purchase.
- Old checkout terminology is still allowed internally only where needed to keep current UI/API clients working.

### Phase 3: Runtime Credit Reservations For Core AI

Purpose: move the highest-volume paths to credits first while the UI still looks familiar.

Work:

- Replace AI response quota checks with assistant model round reservations.
- Add long-context surcharge calculation at the billing service boundary.
- Charge silent wake/routing checks as their own visible usage action.
- Attach provider model, tokens, and final cost ledger data to the reservation.
- Release reservations on model/request/tool failures; commit exactly once on success.

Exit criteria:

- Normal assistant replies, long-context replies, and silent routing checks use credits.
- Provider cost and user credit usage can be joined by request ID.
- Existing plan/subscription UI can still display old labels temporarily, but enforcement is now credit-backed for core AI.

### Phase 4: Runtime Credit Reservations For Tools

Purpose: move expensive and variable-cost actions onto the same credit engine.

Work:

- Add credit quotes and reservations for web search, image inspection, image generation, YouTube search, summaries, clip workflows, scheduled/composed tool runs, knowledge writes, storage rent, and music.
- Use max-cost reservations for multi-step tool workflows, then settle to the configured action price after success.
- Keep model/provider retry behavior covered by the reservation rather than bypassing failed model decisions.
- Add tests that successful tool calls commit once and failed/canceled calls release reservations.

Exit criteria:

- Every paid runtime action uses credit reservations.
- Metric-specific quota checks are no longer needed for enforcement.
- The cost ledger still records provider-level detail, but user-facing billing only exposes credits.

### Phase 5: Data Migration And Legacy Removal

Purpose: convert existing guilds and delete legacy quota enforcement.

Work:

- Create credit accounts for all guilds.
- For trial guilds, grant Trial Pack credits expiring at the existing trial end.
- For paid guilds, grant credits from remaining old quota value:
  - remaining AI responses * 4
  - remaining web searches * 8
  - remaining image generations * 150
  - remaining knowledge storage room is not converted into credits; start storage rent after migration with a 7-day grace.
- Cap migration grants at the new pack credit amount for the matching old tier unless an operator opts into a manual make-good.
- Preserve read-only, suspended, canceled, export, and support states as account status.
- Delete old subscription quota code, old metric-specific reservation paths, and old plan limit definitions after migration verification.

Exit criteria:

- All guilds have credit accounts and ledger grants.
- Runtime enforcement does not read old quota buckets.
- Legacy subscription/quota code is removed except immutable historical migrations.

### Phase 6: Discord And Command UI Terminology

Purpose: update the highest-frequency customer-visible surfaces first.

Work:

- Rename Discord responses from plans, quotas, subscriptions, renewals, and invoices to packs, credits, account status, payment orders, and buy credits.
- Update depleted/read-only messages to explain credits without exposing provider costs.
- Update admin/support commands that users may see in screenshots or support threads.
- Keep usage copy action-oriented: "Image generation used 150 credits" instead of model/provider detail.

Exit criteria:

- Discord no longer presents the product as a subscription.
- Credit balance, depleted state, and buy-credit calls to action are understandable without reading the landing page.

### Phase 7: Landing, Pricing, And Checkout UI

Purpose: update the public buying flow after Discord terminology is settled.

Work:

- Change public "Pricing / Plans" surfaces to "Packs".
- Show pack price, credits received, retention cap, knowledge cap, and representative action costs.
- Update checkout copy so SOL payment is framed as activating a prepaid server pack.
- Remove monthly/subscription/renewal wording from public copy.
- Keep provider names and gross margin assumptions out of customer-facing UI.

Exit criteria:

- A new buyer understands that packs are prepaid credits for one Discord server.
- The landing page, checkout page, payment confirmation, activation key copy, and error states all use pack language.
- No browser testing is required in this plan; leave visual browser verification to the user.

### Phase 8: Admin, Operations, And Observability UI

Purpose: update internal surfaces after the customer flow is complete.

Work:

- Rename admin billing pages and routes from subscription concepts to pack/account concepts.
- Show credit balance, reserved credits, grants, usage ledger, payment events, account status, storage rent, and provider-cost margin.
- Add operator filters for depleted guilds, high-cost guilds, failed reservations, low-margin pack cohorts, and storage-rent grace periods.
- Update operations docs and runbooks to use pack terminology.
- Add alerts for pack cohorts below 45 percent gross margin and guilds consuming more than 50 percent of purchased credits in the first 20 percent of the pack expiration window.

Exit criteria:

- Operators can investigate a billing issue without old subscription language.
- Cost ledger detail remains internal.
- Ops docs point at credit-account repair, grant, refund, and export flows.

### Phase 9: Launch Verification And Cleanup Sweep

Purpose: prove the system is coherent and remove leftover terminology.

Work:

- Run the full backend test suite plus targeted migration and billing tests.
- Search code, tests, docs, landing copy, Discord copy, config, and operations docs for forbidden subscription terminology.
- Verify credit quotes exist for every paid action.
- Verify reservation release behavior for failed provider calls and canceled tool workflows.
- Recalculate pack margins using the latest provider prices before launch.

Exit criteria:

- No runtime path uses legacy plan quota enforcement.
- No customer-visible surface uses subscription terminology.
- Remaining historical database migrations are the only place old names are allowed.

## Product Copy Rules

Use these terms:

- pack
- credits
- credit balance
- account status
- buy credits
- activate pack
- credit grant
- credit usage
- payment order
- trial pack

Avoid these terms:

- subscription
- subscriber
- monthly plan
- renewal
- current billing period
- invoice
- plan quota
- upgrade plan

Acceptable nuanced copy:

- "Packs are prepaid credits for one Discord server."
- "Credits are consumed by actions. Expensive actions cost more credits."
- "Storage and retention are controlled by the highest active pack on the server."
- "When credits run out, Panda stays available for billing, export, delete, and support, but paid actions pause until another pack is activated."

## Verification Checklist

- Unit tests cover credit quote math for every action.
- Unit tests cover reservation commit, release, expiration, and idempotency.
- Migration tests convert active trial, active paid, depleted, read-only, suspended, canceled, coupon, and unactivated order cases.
- SOL order tests prove pack lamports are locked per order and activation grants credits once.
- Tool tests prove failures release reservations and successful calls commit exactly once.
- Cost ledger tests prove provider/model details remain internal and link to credit reservations.
- No browser testing is required for this plan.

## Open Questions Before Implementation

- Should paid credits expire after 12 months, or only become unusable when a server is deleted?
- Should storage rent pause when a server is depleted, or should depleted servers enter read-only after a grace period?
- Should pack lamports always float with SOL/USD, or should operators prefer manual lamports updated during launches?
- Should image generation expose resolution prices in Discord before the model call, or keep one default image price and restrict resolution until usage data is stable?
