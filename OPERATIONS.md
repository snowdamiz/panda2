# Panda Operator Runbook

This runbook is for Panda operators. It includes deployment, provider, billing, backup, restore, and incident details that should not be copied into customer-facing docs or Discord support responses.

## Local Verification

```bash
go test ./...
go test -tags sqlite_fts5 ./...
go vet ./...
```

The default test run covers the SQLite fallback search path. The tagged run covers the production FTS5 path used by the Docker image.

## Production Deployment

1. Run one primary writable database for v1. Keep SQLite attached to one primary Machine unless the storage design moves to LiteFS, Postgres, or another explicitly owned single-writer design.
2. Create one Fly volume mounted at `/data`.
3. Configure Discord application ID, public key, owner user IDs, public landing URL, Discord install callback URL, SOL payment settings, and runtime settings in `panda.config.json` or environment overrides.
4. Store secrets with the deployment secret manager:
   - `DISCORD_BOT_TOKEN`
   - `OPENROUTER_API_KEY`
   - `BRAVE_SEARCH_API_KEY`
   - `SOLANA_RPC_URL`
   - `SOLANA_TREASURY_WALLET`
   - `SOLANA_PACK_LAMPORTS` (or the per-pack `SOLANA_PACK_STARTER_LAMPORTS`, `SOLANA_PACK_PLUS_LAMPORTS`, `SOLANA_PACK_PRO_LAMPORTS`, and `SOLANA_PACK_BUSINESS_LAMPORTS`)
5. Store a GitHub Actions secret named `FLY_API_TOKEN` with permission to deploy both `panda-assistant` and `panda2-landing`.
6. Deploy by merging to the `release` branch. The `CI` GitHub Actions workflow deploys both Fly apps with `flyctl deploy --remote-only`.
7. In the Discord Developer Portal Webhooks page, set the endpoint to `https://<app-host>/discord/webhook-events`, enable events, and subscribe to `APPLICATION_AUTHORIZED`.
8. Confirm the landing build args point at the API origin with `PUBLIC_PANDA_API_BASE_URL`. Do not expose Solana RPC endpoints to the static landing app.
9. Check rollout with `fly status`, `fly releases`, `fly logs`, `/readyz`, `/metrics`, and owner-ops health through Panda chat.

Production validation fails when Discord credentials, the managed AI service key, public app URL, SOL RPC URL, treasury wallet, or paid-pack lamport mappings are missing.

## Billing Environment

Required for SOL self-serve billing:

- `PUBLIC_APP_URL`: the hosted landing origin used in Discord billing links and payment CORS.
- `DISCORD_INSTALL_REDIRECT_URI`: API callback URL registered in the Discord Developer Portal OAuth2 redirects, for example `https://<api-host>/discord/install/callback`. Do not point this at the static landing host unless that host serves the API callback.
- `BILLING_ALLOWED_ORIGINS`: comma-separated origins allowed to call `/billing/*` and `/admin/*`; defaults effectively include `PUBLIC_APP_URL`. Development also allows the local Astro origins `http://localhost:4321` and `http://127.0.0.1:4321`.
- `SOLANA_RPC_URL`: backend-only RPC endpoint used to prepare, submit, and verify Solana transactions. Do not publish this value to browser builds.
- `SOLANA_CLUSTER`: `devnet`, `testnet`, `mainnet`, or `mainnet-beta`; defaults to `devnet`.
- `SOLANA_TREASURY_WALLET`: treasury wallet receiving native SOL transfers. The landing `/admin` page also requires this wallet to sign the admin login challenge.
- `SOLANA_CONFIRMATION`: `confirmed` or `finalized`; defaults to `finalized`.
- `SOLANA_ORDER_EXPIRATION`: order lifetime; defaults to `30m`.
- `SOLANA_ACTIVATION_KEY_TTL`: one-time key lifetime after reveal; defaults to `48h`.
- `SOLANA_PACK_LAMPORTS`: comma-separated `pack:lamports` entries (for example `starter:268779177,plus:693167350,pro:1400480973,business:3522421842`), the preferred way to set all paid-pack prices in one value. Per-pack `SOLANA_PACK_STARTER_LAMPORTS`, `SOLANA_PACK_PLUS_LAMPORTS`, `SOLANA_PACK_PRO_LAMPORTS`, and `SOLANA_PACK_BUSINESS_LAMPORTS` override individual packs.

Optional billing overrides:

- Legacy `SOLANA_PLAN_LAMPORTS` and `SOLANA_<TIER>_LAMPORTS` variables are still accepted and merged into the pack lamports for backward compatibility. Prefer the `SOLANA_PACK_*` variables for new deployments.
- Prefer a fresh SOL/USD quote with `SOLANA_USD_CENTS_PER_SOL` so pack lamports are priced from the public USD targets; treat manual lamport overrides as an emergency control.

Landing build-time values:

- `PUBLIC_PANDA_API_BASE_URL`: API origin used by the static landing payment module.

SOL payment setup:

- Pack prices must map to integer lamports and match the public pack table.
- The treasury wallet should be controlled outside the app runtime. Do not store private keys on the Panda host.
- The landing page creates orders through `POST /billing/sol/orders`, asks the backend to prepare an unsigned transaction through `POST /billing/sol/orders/:order_id/transaction`, asks the user's wallet to sign the server-built transaction bytes, then submits the signed transaction to `POST /billing/sol/orders/:order_id/submit`.
- The backend fetches the latest blockhash, serializes the native SOL transfer plus memo/reference, submits signed transaction bytes through Solana RPC, verifies structured Solana RPC responses, rejects token transfers, requires one matching native SOL transfer to the treasury wallet, requires the order memo/reference, and only accepts transactions at or above the configured confirmation threshold.
- Verified orders reveal one activation key once. The key is stored hashed, consumed by `/billing action:activate api_key:<key>`, and cannot be re-revealed.
- Operators can revoke an unused activation key by payment order through internal operator tooling backed by the billing revocation service. Revocation, creation, one-time viewing, consumption, and expiration are recorded in audit events.
- Operators manage coupon creation, listing, and revocation from the landing admin page at `/admin`. Admin access requires signing a short login challenge with the configured `SOLANA_TREASURY_WALLET`; coupon management is intentionally not exposed through Discord bot commands.
- Operators repair credit accounts from the same `/admin` page: assign a pack (which grants its credits), set account status (active, trialing, grace, read-only, suspended, canceled, depleted), adjust the pack expiry, and review available, reserved, and granted credits. Credit grants, reservations, and the credit ledger are the source of truth for a guild's balance; never hand-edit balances outside the credit-account tooling. Exports and deletions run through `/data` in Discord and the data summary covers credit accounts, grants, and historical billing records.

## Health Checks

- `GET /healthz` reports configuration, Fiber, Discord gateway credentials, Discord webhook public key, the managed AI service key, SQLite, and local storage.
- `GET /readyz` returns unavailable when SQLite is not ready.
- `GET /metrics` emits Prometheus-style gauges for SQLite, configured integrations, schema version, queue depth, usage totals, and AI-service configuration.
- Bot owners can ask Panda for owner-ops health, including shard status. V1 reports `single-gateway-v1` when Discord credentials are configured and `disabled` when the gateway is not running.
- Ask Panda to drain the worker, then confirm, to stop claiming new background jobs.
- Ask Panda to resume the worker, then confirm, to resume background job processing.
- Ask Panda to enable or disable incident mode, then confirm, to update runtime incident state.
- Ask Panda to reload owner ops to recheck runtime dependencies.

Long Discord summaries are queued as `discord.interaction` jobs. If queue depth grows, inspect logs, AI-service health, entitlement checks, SOL verifier health, worker drain state, and incident state before scaling.

## Billing Operations

Panda grants entitlements only from verified SOL payment orders activated with one-time keys. Do not grant paid access from wallet connection state, landing UI state, pasted signatures that have not passed backend verification, or support screenshots.

The phased credit-pack refactor proposal, including provider cost math, implementation order, and terminology cleanup, is in `CREDIT_PACKS_REFACTOR_PLAN.md`.

Required billing checks:

- Payment order creation is rate limited and records exact pack, credits, amount, treasury wallet, memo/reference, expiration, and support contact.
- SOL transaction verification is idempotent by signature and records successful and failed verification attempts.
- Activation keys are revealed once, hashed at rest, scoped to the order guild, and consumed atomically.
- Every paid assistant, web search, schedule, composed-tool, storage, and music path checks entitlement before provider spend.
- Past-due accounts enter grace, then read-only or suspended states according to billing policy.
- Suspended and canceled guilds retain help, billing, export, delete, and support access.

Weekly reconciliation:

- Compare internal SOL payment events against treasury wallet transactions and RPC history.
- Compare internal credit usage, web search counts, storage bytes, and cost ledger records against provider dashboards.
- Alert when gross margin drops below 45% for any pack cohort.
- Alert when a guild consumes more than 50% of its purchased credits in the first 20% of the pack expiration window.

## Sharding And Scale Path

V1 intentionally runs one Discord gateway worker with one SQLite writer. Keep Fly Machines at a single primary app instance while SQLite is stored on one attached volume.

Before enabling multiple gateway shards or multiple Machines:

1. Move durable state to a multi-writer-safe plan: LiteFS with a single writable primary, Fly Postgres, or another explicit storage architecture.
2. Split background workers from gateway workers so only the intended process claims long-running jobs.
3. Add shard IDs and counts to structured logs and metrics.
4. Add queue backpressure, dead-letter queues, and per-guild concurrency caps.
5. Confirm owner-ops health reports shard count, connected shard IDs, worker drain state, and queue state before scaling.

## SQLite Backup

Use the store backup path from a maintenance command or one-off shell. The implementation uses SQLite `VACUUM INTO`, which creates a consistent backup file without requiring the app to stop.

Recommended destination pattern:

```text
/data/backups/panda-YYYYMMDD-HHMMSS.db
```

Copy backups off the Fly volume after creation. Keep the main database, WAL, and backup retention policy aligned with pack-based server data retention.

## SQLite Restore

1. Ask Panda to drain work and confirm the owner-ops action.
2. Stop or scale down the app so SQLite has no active writer.
3. Copy the selected backup back to the Fly volume.
4. Move the existing database, WAL, and SHM files aside with timestamped names.
5. Place the backup at the configured SQLite path.
6. Start the app and check `/readyz`, owner-ops health, `/metrics`, and `fly logs`.
7. Reconcile credit grants, credit reservations, and cost ledger rows created near the restore point.
8. Ask Panda to resume workers and confirm after the restored database passes health checks.

## Rollback

1. Inspect `fly releases`.
2. Roll back with `fly releases rollback <version>`.
3. Watch `fly logs`.
4. Confirm `/readyz`, `/metrics`, owner-ops health, and SOL payment verification on the configured cluster.

Schema migrations are forward-only. Restore from a SQLite backup if a bad migration corrupts data.

## Incident Mode

For managed AI trouble, ask Panda to enable incident mode, drain background work if needed, and disable assistant responses in affected guilds; confirm each privileged change. Removing the AI service key and redeploying is a last-resort global stop; health checks will report the AI service as missing and assistant responses will stop before provider calls.

For SOL payment verification trouble, pause new purchases from the landing page if needed, keep existing credit accounts intact, preserve submitted signatures and order IDs, retry verification after RPC recovery, and verify idempotency before resuming self-serve purchases.

For credit-burn spikes or abuse, suspend the guild, revoke trial credits when warranted, preserve audit logs, and keep export/delete/support paths available.

## Customer Support Boundaries

Customer-facing support may include guild ID, pack, account status, credit usage, command failure counts, recent error codes, queue depth, and Discord permission gaps.

Do not include raw prompts, raw Discord messages, provider model names, API keys, billing secrets, hidden tools, internal cost math, or vendor fallback details in normal support responses or screenshots.
