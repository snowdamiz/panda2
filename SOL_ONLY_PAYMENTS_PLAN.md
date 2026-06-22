# SOL-Only Payment And Bot API Key Plan

## Reader And Outcome

This plan is for the engineer implementing the temporary payment change. After reading it, they should be able to remove the active Stripe payment path, make the landing page the only purchase entry point, accept only SOL payments, and activate Panda in Discord through an API key flow without weakening entitlement checks.

## Decision

Stripe self-serve billing and Discord Premium paid entitlements are legacy for this phase. Do not keep either one as a hidden fallback or secondary payment option. The only paid self-serve path should be:

1. Customer starts from the landing page.
2. Customer connects a Solana wallet through Wallet Standard-compatible extension or mobile deeplink flows.
3. Customer reviews, signs, and sends a native SOL transfer transaction for the selected Panda plan.
4. Panda verifies the Solana payment server-side.
5. Panda issues a one-time, server-scoped activation API key.
6. The Discord billing owner uses that key with the bot to activate or renew the server plan.
7. Panda grants entitlements only after the key is validated and consumed.

The bot must not create payment sessions, accept raw transaction claims as proof, or grant access from landing-page success UI alone. Wallet connection is a signing and payment UX only; it does not identify a Discord billing owner or grant entitlements by itself.

## Goals

- Disable and remove active Stripe runtime code for now.
- Remove Discord bot checkout and portal flows.
- Make the landing page the only place users can begin a paid purchase.
- Let users choose and connect their Solana wallet through extension or mobile deeplink-compatible flows before payment.
- Accept SOL only. Reject all other chains, tokens, wrapped assets, or payment providers.
- Add a payment verification flow that confirms a real Solana transfer before issuing access.
- Add an API key activation flow that lets the bot authenticate a paid purchase and bind it to a Discord server.
- Preserve existing plan tiers, limits, quota checks, read-only behavior, and entitlement snapshots.
- Keep payment verification and activation handling idempotent.

## Non-Goals

- Do not build a generic multi-crypto abstraction.
- Do not keep Stripe available behind config.
- Do not make a Phantom-only integration. Use wallet discovery/standard flows where available and keep mobile deeplinks compatible with multiple Solana wallets when practical.
- Do not add deterministic hard-coded fallbacks that grant plans when payment verification or structured data is unavailable.
- Do not browser-test the landing page in this repo workflow. Leave browser QA to the user.
- Do not change plan limits unless the user asks for pricing or package changes.

## Current State Summary

Panda currently has a provider-neutral entitlement core, but Stripe is wired into several active surfaces:

- The billing service creates Stripe Checkout and Customer Portal sessions.
- The HTTP server receives Stripe webhooks and converts them into billing events.
- The Discord `/billing` command exposes upgrade, checkout, and portal actions.
- Config and env loading understand Stripe keys, webhook secrets, API base URLs, and price mappings.
- Docs, operator runbooks, landing pricing copy, and legal copy refer to Stripe checkout, Discord entitlement events, or payment processors.
- The data model stores provider, external subscription IDs, entitlement snapshots, and invoice/payment events. That part can be reused for SOL payments.

The useful foundation is that subscription rows already have a payment provider, external identifiers, period dates, status, grace state, and entitlement snapshots. SOL support should use those provider-neutral fields instead of inventing a parallel entitlement system.

## Target Product Flow

### Landing Page Purchase

The landing page should show the existing plans and a SOL-only purchase action for each paid tier. The UI should not mention Stripe checkout, Discord Premium Apps, credit cards, or alternate crypto rails.

The primary payment UX should be wallet-connected:

- Discover extension wallets through Wallet Standard-compatible APIs when the user is on desktop.
- Support mobile wallet handoff through Solana Pay/deeplink-compatible transaction flows where possible.
- Prefer wallet-choice UI over a single hard-coded wallet brand.
- Show the connected wallet address, selected plan, exact SOL amount, treasury wallet, cluster, and memo/reference before requesting a signature.
- Never ask for or store private keys, seed phrases, wallet export files, or custodial credentials.

The purchase form should collect the minimum data needed to bind payment to a Discord server:

- Discord guild ID or install context.
- Billing owner Discord user ID when available.
- Plan.
- Optional support email.

The landing page should create a server-side payment order before showing payment instructions. The payment order response should include:

- Order ID.
- Plan.
- SOL amount.
- Destination wallet address.
- Unique memo, reference, or nonce.
- Expiration time.

The landing page should build or request the exact native SOL transfer transaction for the connected wallet to sign. The transaction must include the server-created destination wallet, exact lamports, and memo/reference. After the wallet sends the transaction, the landing page submits the transaction signature to Panda and can poll order status. Successful UI status is informational only. Entitlement grant still happens only from server-side verification and activation key consumption.

Manual transaction signature entry may exist as a recovery/support path for interrupted wallet redirects, but it must not bypass the same order matching and server verification rules.

### SOL Pricing Policy

For the temporary SOL-only flow, plan prices should be configured by the server as exact lamports per plan. The landing page must display the server-created order amount, not calculate its own conversion.

Required behavior:

- Store the expected lamports on each order at creation time.
- Treat underpayment as not verified.
- Decide explicitly whether overpayment is accepted, rejected, or sent to support review.
- Expire orders so stale SOL amounts cannot be reused.
- Do not add a live USD-to-SOL quote fallback unless the quote provider, stale-quote behavior, and failure mode are explicitly designed.

### Payment Verification

Payment verification should run on the server. It must verify:

- The transaction is finalized or otherwise meets the chosen confirmation threshold.
- The recipient wallet is Panda's configured SOL treasury wallet.
- The transferred asset is native SOL only.
- The transferred amount satisfies the selected plan price after any required tolerance rules.
- The order reference, memo, or nonce matches the payment order.
- The submitted signature came from the wallet-signed transaction for the server-created order, not from arbitrary client-supplied payment metadata.
- The order has not expired.
- The transaction signature has not already been consumed.
- The payment is for the same plan and guild context recorded on the order.

If any field is missing or ambiguous, fail closed and surface a recoverable support state. Do not map failed verification to a default plan.

### API Key Activation

After a payment is verified, Panda should issue a one-time activation API key. This key is the bridge between the landing-page payment and the Discord bot.

Key requirements:

- Generated with cryptographic randomness.
- Displayed once to the payer.
- Stored only as a hash, with a short visible prefix for support lookup.
- Scoped to one guild ID when known, one plan, one payment order, and one payment period.
- Expires if unused.
- Consumed atomically so replay does not reactivate or extend plans twice.
- Revocable by an operator.
- Audited when created, viewed, consumed, expired, or revoked.

The Discord bot should accept the key through a billing activation action. Only the current billing owner, a guild admin claiming an unclaimed server, or a Panda operator can consume a key. After successful consumption, Panda should upsert the guild subscription with payment provider `sol`, record an invoice/payment event with the payment signature or order ID as the external ID, and create a fresh entitlement snapshot.

Recommended command shape:

```text
/billing action:activate api_key:<one-time-key>
```

`/billing` without an activation key should continue to show subscription status, quota, renewal period, and a landing-page purchase link. It should not create payment sessions from Discord.

## Implementation Plan

### 1. Confirm Stripe As Legacy And Remove Active Surfaces

- Remove the Stripe client, checkout request types, portal request types, Stripe webhook parsing, Stripe signature verification, and Stripe event handling from active runtime code.
- Remove Stripe-specific config fields, env vars, validation branches, and app wiring.
- Remove the Stripe webhook route and the billing success/cancel pages that only exist for Stripe redirects.
- Remove checkout and portal actions from the Discord command registration and router behavior.
- Replace Discord billing upgrade guidance with a landing-page purchase link.
- Remove Stripe-specific tests and replace them with SOL payment and API key tests.
- Update customer docs, operator docs, env examples, landing copy, and legal copy so they describe SOL-only payment.
- For existing deployed databases, use forward migrations to stop depending on Stripe-specific columns. If this database has not shipped to production, remove Stripe-only schema columns from the original billing migration instead of preserving them.
- Disable paid Discord entitlement mapping as a paid-plan grant path. Keep the Discord install webhook only if it is still needed for owner-only install or trial creation.

### 2. Add SOL Payment Domain Types

Add provider-specific types around the existing billing core:

- Payment order.
- Payment transaction.
- Activation API key.
- SOL verification result.
- SOL payment provider config.

Store order state separately from entitlement state. A payment order should move through explicit states such as pending, verified, expired, failed, and activated. A subscription should only change when a verified order is converted through an activation key.

Use provider value `sol` in subscription and payment event records. Keep provider values `trial` and `manual` only if those paths remain intentionally active. Remove `stripe` and do not let `discord` grant paid plans during SOL-only mode.

### 3. Add Storage And Migration Support

Add tables for:

- SOL payment orders.
- SOL payment transactions or verification attempts.
- Activation API keys.

Minimum order fields:

- Order ID.
- Guild ID.
- Billing owner user ID.
- Plan.
- Expected lamports or SOL amount.
- Destination wallet.
- Reference or memo.
- Status.
- Expiration time.
- Verified transaction signature.
- Created and updated timestamps.

Minimum API key fields:

- Key ID.
- Key hash.
- Key prefix.
- Payment order ID.
- Guild ID.
- Plan.
- Status.
- Expires at.
- Consumed at.
- Consumed by Discord user ID.
- Created and updated timestamps.

Use unique constraints on transaction signatures, order references, and key hashes to make retries safe.

### 4. Build SOL Verification Service

Add a service that verifies Solana transactions against a configured RPC endpoint. The verifier should use structured RPC responses, not string scraping.

Required config:

- Solana RPC URL.
- Solana cluster name.
- Panda SOL treasury wallet address.
- Plan-to-lamports map.
- Confirmation threshold.
- Activation key TTL.
- Optional order expiration duration.

The verifier must fail closed when RPC is unavailable, responses are malformed, the transaction is not final enough, or the transfer cannot be unambiguously matched to the payment order.

### 5. Build Payment API Endpoints

Add landing-facing payment endpoints to the app API:

- Create SOL payment order.
- Read payment order status.
- Submit or refresh transaction verification.
- Provide transaction request details for wallet/deeplink flows when the frontend needs server-authored payment metadata.
- Retrieve activation key once after verified payment.

These endpoints should rate-limit writes and avoid leaking whether a guild has private billing metadata. The activation key response should be shown once; later calls should show status and support instructions, not the full key.

If the landing site is deployed separately from the bot API, add an internal API credential for server-to-server calls. That internal credential is separate from customer activation keys and should not grant entitlements by itself.

### 6. Update Bot Billing Flow

Change `/billing` to support:

- `status`: current plan, quota, renewal, provider, and landing-page purchase link.
- `activate`: consume an activation API key and apply the paid plan.

Remove:

- `upgrade`, `checkout`, and `portal` as active payment actions.
- Optional Stripe billing email input.
- Messaging that says Stripe will apply a plan after a webhook.

Activation must:

- Check the actor is allowed to manage billing for the guild.
- Hash and look up the supplied key.
- Confirm key status is unused and unexpired.
- Confirm key scope matches the guild or is safely claimable for that guild.
- Record the payment event.
- Upsert the subscription and entitlement snapshot.
- Mark the key consumed in the same transaction.

If activation fails, return a precise error without exposing key existence to unauthorized users.

### 7. Update Landing Page

Replace checkout commands in pricing with SOL purchase controls. Each paid plan should start the order flow from the landing page and guide the user through wallet connection, transaction review, signing, verification, and activation key reveal.

Landing page states:

- Plan selected.
- Wallet selection and connected wallet.
- Payment order created.
- Review exact native SOL transfer.
- Awaiting wallet signature / mobile wallet return.
- Verifying payment.
- Payment verified and activation key ready.
- Key copied/viewed.
- Expired or failed with support path.

Remove copy that says upgrades happen from Discord checkout. The Discord bot can show status and activate keys, but the purchase starts from the landing page.

### 8. Update Docs And Operations

Update customer and operator documentation to reflect:

- SOL-only payments.
- Landing-page purchase entry point.
- Activation API key handling.
- Solana RPC configuration.
- Treasury wallet rotation procedure.
- Reconciliation against Solana transactions.
- Support procedure for paid but unactivated orders.
- Refund and cancellation language for SOL payments.

Also update env examples and runtime validation so production requires SOL payment config when paid self-serve billing is enabled.

### 9. Tests And Verification

Add or update tests for:

- Config validation with SOL payment settings and no Stripe settings.
- Payment order creation, expiration, and idempotent status reads.
- Wallet-facing payment metadata generation that uses the server-created order amount, wallet, cluster, and memo/reference.
- Solana transaction verification success and failure cases.
- Rejection of wrong wallet, wrong token, wrong amount, missing reference, duplicate signature, expired order, and unconfirmed transaction.
- Activation key generation, hashing, prefix lookup, expiration, revocation, and one-time consumption.
- Bot billing status and activation command behavior.
- Entitlement snapshot creation with provider `sol`.
- Docs or text fixtures no longer claiming Stripe checkout is active.

Run normal automated tests and builds. Do not do browser testing as part of this workflow.

## Security And Abuse Controls

- Never store activation API keys in plaintext.
- Redact activation keys from logs, audit details, support bundles, and Discord responses after initial submission.
- Never request or persist wallet private keys, seed phrases, or keypair files.
- Do not treat wallet connection, wallet address, or landing-page success UI as entitlement proof.
- Rate-limit payment order creation and activation attempts.
- Use constant-time hash comparison where practical.
- Make activation transactional to prevent double spend or double entitlement extension.
- Treat Solana RPC results as untrusted until every expected field is verified.
- Never accept client-supplied plan, amount, wallet, or transaction metadata without matching it to a server-created order.
- Keep operator override paths manual and audited; do not make them fallback behavior.

## Acceptance Criteria

- No active Stripe checkout, portal, webhook, config, env, or docs path remains.
- No active Discord Premium paid entitlement path remains.
- `/billing` cannot create a payment session and instead points users to the landing page.
- Paid self-serve purchase starts only from the landing page.
- The landing page lets users connect a Solana wallet and sign/send the server-created native SOL transfer.
- Only native SOL payments to Panda's configured wallet can verify an order.
- A verified payment issues a one-time activation API key.
- The bot can activate a server plan only by consuming a valid API key through authorized billing management.
- Entitlements are still enforced before paid AI/search/storage/schedule/music spend.
- Replays, duplicate transactions, expired orders, invalid keys, and wrong-chain or wrong-token payments fail closed.
