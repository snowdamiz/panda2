# Coupon Discount Implementation Plan

## Reader and Outcome

Reader: a Panda maintainer implementing billing changes.

Post-read action: add owner-created coupon codes that discount any paid plan tier, let a guild billing admin apply a coupon while creating billing, and support coupons that reduce the due amount to zero without bypassing the normal activation/audit path.

## Goals

- Bot owners can create, list, and revoke coupon codes.
- Each coupon applies to one paid plan tier and stores a fixed integer discount amount in lamports.
- The same plan can have many coupons with different amounts, expiration dates, and redemption limits.
- A guild billing admin can enter a coupon code when preparing billing from the landing page.
- Invalid, expired, revoked, exhausted, or wrong-plan coupons fail clearly. They must not silently fall back to full-price orders.
- A coupon can reduce the amount due to zero. Free activations still create an auditable order, redemption, activation key, subscription, entitlement snapshot, and payment event.
- Paid discounted orders must verify the discounted native SOL amount, not the list price.

## Non-Goals

- Percentage coupons.
- Multi-plan or all-plan coupons.
- Automatic recurring discounts after the current one-month activation period.
- Browser-based verification by the implementer. Unit and integration tests are enough; the user will do browser checks.

## Current Billing Shape

Panda currently has a SOL-only purchase spine:

- The billing service creates a payment order with guild, plan, exact lamports, treasury wallet, memo/reference, expiration, and status.
- The landing page creates the order, signs/sends a native SOL transfer, submits the transaction signature, and reveals a one-time activation key after backend verification.
- Discord `/billing action:activate api_key:<key>` consumes the activation key and applies the plan.
- Activation keys are hashed at rest, revealed once, consumed atomically, and audited.
- Operators can revoke unused activation keys.

Coupons should extend this spine instead of adding a separate entitlement grant path.

## Target Flow

1. Bot owner creates a coupon for a paid plan tier.
2. The service returns the raw coupon code once. Store only a hash and short prefix.
3. Guild billing admin selects a plan on the landing page and optionally enters the coupon code.
4. Backend validates the coupon against the selected plan and creates a quoted billing order:
   - list lamports
   - discount lamports
   - due lamports, capped at zero
   - coupon prefix when present
5. If due lamports are greater than zero, the existing SOL flow continues with the discounted due amount.
6. If due lamports are zero, the order becomes ready for activation immediately and the landing page can reveal an activation key without wallet/signature steps.
7. Discord activation consumes the key and applies the subscription exactly as it does today.

## Data Model

Add coupon tables:

- `billing_coupons`
  - stable coupon id
  - code hash
  - code prefix
  - plan
  - discount lamports
  - max redemptions, where zero means unlimited
  - status: active, revoked, expired
  - optional owner note
  - created by user id
  - expires at
  - revoked at
  - timestamps

- `billing_coupon_redemptions`
  - coupon id
  - order id
  - guild id
  - billing owner user id
  - plan
  - list lamports
  - discount lamports
  - due lamports
  - status: pending, consumed, released
  - expires at
  - consumed at
  - released at
  - timestamps

Refactor the existing SOL payment order concept into a provider-neutral billing order:

- Keep SOL transaction verification as SOL-specific detail.
- Move plan, guild, quoted amounts, coupon metadata, status, expiration, and activation readiness into a neutral order model.
- Keep one activation-key table and point keys at the neutral order id.
- Migrate existing SOL orders forward and remove obsolete SOL-only order structs once all call sites use the neutral order model.

This avoids leaving a misleading zero-lamport "SOL payment order" path for free coupons.

## Quoting Rules

- Normalize the selected plan with the existing paid-plan validation.
- Reject trial coupon orders.
- Load the list price from configured plan lamports.
- If no coupon code is supplied, quote list price as the due amount.
- If a coupon code is supplied, hash the trimmed code and load the coupon in a transaction.
- Reject a coupon when it is not active, expired, not for the selected plan, or out of redemptions.
- Discount is `min(coupon.discount_lamports, list_lamports)`.
- Due amount is `list_lamports - discount`.
- A discount larger than the list price is allowed but never creates a negative payment amount.

## Redemption Rules

- Creating a coupon order reserves a redemption while the order is pending.
- Pending redemptions count against `max_redemptions` so limited coupons cannot be oversold.
- Expiring an unpaid order releases its pending redemption.
- Revealing an activation key consumes the redemption.
- Revoking an unused activation key does not automatically restore a consumed coupon redemption unless a later explicit owner operation is added.
- All redemption updates happen in the same transaction as order/key status changes.

## Backend Service Changes

- Add coupon code generation using secure randomness and a readable prefix.
- Add owner-only service methods:
  - create coupon
  - list coupons
  - revoke coupon
- Add a single quote helper used by order creation and tests.
- Extend order creation request/response with `coupon_code`, `list_lamports`, `discount_lamports`, `due_lamports`, and `coupon_prefix`.
- Rename internal "expected lamports" logic to "due lamports" where it represents the amount the customer must pay.
- For paid discounted orders, verification must compare the native SOL transfer against due lamports.
- For free coupon orders, mark the order ready for activation without calling Solana RPC.
- Activation must record payment events with coupon metadata in raw payload:
  - paid discounted order: provider `sol`, amount equal to due lamports
  - free coupon order: provider `coupon`, amount zero, status `comped`
- Subscription payment provider should be `sol` for paid discounted orders and `coupon` for free coupon orders.
- Audit coupon create, revoke, redemption reserve, redemption consume, and free activation key reveal.

## HTTP and Landing Page Changes

- Accept `coupon_code` on order creation.
- Return quote fields on order responses.
- Add an optional coupon input beside plan selection.
- Show list price, discount, and amount due after the order is created.
- If amount due is zero:
  - hide or disable wallet send, signature, and verify controls
  - enable reveal activation key
  - show clear "discount covers this plan" status text
- If amount due is positive:
  - build the SOL transaction with due lamports
  - keep the existing signature verification and reveal flow
- Do not create a full-price order when coupon validation fails. Show the backend error instead.

## Discord Command Changes

Extend owner/operator billing actions:

- `coupon_create`
  - owner-only
  - options: plan, discount_lamports, optional code, optional max_redemptions, optional expires_at, optional note
  - returns the raw coupon code once
- `coupon_list`
  - owner-only
  - shows prefix, plan, discount, status, expiration, and redemption counts
- `coupon_revoke`
  - owner-only
  - option: coupon id or prefix

Keep guild admin activation unchanged:

- `/billing action:activate api_key:<key>`

The admin uses the coupon on the landing page, then activates the resulting key in Discord.

## Edge Cases

- Wrong-plan coupon: reject during order creation.
- Expired coupon: reject during order creation.
- Coupon expires after order creation: existing pending order remains governed by its order expiration.
- Paid order underpayment: reject verification against due lamports.
- Paid order overpayment: keep current behavior and accept if transfer is at least due lamports.
- Free coupon order: never requires SOL settings or RPC.
- Duplicate custom coupon code: reject instead of regenerating silently.
- Coupon code lookup: hash exact trimmed code; do not store plaintext.
- Coupon listing: show prefix and metadata only, never full code.

## Implementation Phases

1. Add neutral billing order and coupon migrations, models, and repository methods.
2. Refactor SOL order creation/verification to use neutral orders and due lamports.
3. Add coupon quote, reservation, release, and consume logic.
4. Add free coupon activation-key reveal support.
5. Add owner-only Discord coupon commands.
6. Add coupon input and quote rendering to the landing payment UI.
7. Update operations docs and environment examples only if new runtime settings are introduced.
8. Remove obsolete SOL-only order code after all call sites are converted.

## Verification Plan

- Coupon creation stores hash/prefix and returns raw code once.
- Custom duplicate code is rejected.
- Wrong-plan, expired, revoked, and exhausted coupons fail order creation.
- Limited coupon cannot be oversold by concurrent pending orders.
- Pending redemption is released when an order expires.
- Paid coupon order verifies against discounted due lamports.
- Underpaying the discounted due amount is rejected.
- Free coupon order can reveal an activation key without SOL RPC.
- Free coupon activation creates active entitlement, coupon payment event, audit events, and usage limits for the selected plan.
- Existing no-coupon SOL purchase flow still passes.
- Discord owner-only gates prevent non-owners from creating/listing/revoking coupons.
- No browser automation; leave browser testing to the user.

## Reader-Test Notes

A maintainer should be able to start with migrations, build the neutral order and coupon repository methods, then move through service, HTTP, Discord command, and landing UI changes without inventing a second activation path. The critical invariant is that every paid, discounted, or free plan grant still comes from a persisted order plus a one-time activation key.
