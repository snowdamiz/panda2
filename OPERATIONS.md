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

1. Run one primary writable database for v1. Keep SQLite attached to one primary Machine unless the storage plan moves to LiteFS, Postgres, or another explicitly owned single-writer design.
2. Create one Fly volume mounted at `/data`.
3. Configure Discord application ID, public key, owner user IDs, public app URL, billing redirects, SKU or price mappings, and runtime settings in `panda.config.json` or environment overrides.
4. Store secrets with the deployment secret manager:
   - `DISCORD_BOT_TOKEN`
   - `OPENROUTER_API_KEY`
   - `BRAVE_SEARCH_API_KEY`
   - `STRIPE_SECRET_KEY`
   - `STRIPE_WEBHOOK_SECRET`
   - billing SKU and price IDs when not committed as non-secret config
5. Deploy with `fly deploy`.
6. In the Discord Developer Portal Webhooks page, set the endpoint to `https://<app-host>/discord/webhook-events`, enable events, and subscribe to `APPLICATION_AUTHORIZED` plus entitlement events used by Premium Apps.
7. In Stripe, set the webhook endpoint to `https://<app-host>/billing/stripe/webhook` and subscribe to `checkout.session.completed`, `customer.subscription.created`, `customer.subscription.updated`, `customer.subscription.deleted`, `invoice.payment_succeeded`, and `invoice.payment_failed`.
8. Check rollout with `fly status`, `fly releases`, `fly logs`, `/readyz`, `/metrics`, and `/ops health`.

Production validation fails when Discord credentials, the managed AI service key, public app URL, or billing SKU/price mappings are missing. If any Stripe price mapping is configured, production also requires `STRIPE_SECRET_KEY` and `STRIPE_WEBHOOK_SECRET`.

## Billing Environment

Required for Stripe self-serve billing:

- `PUBLIC_APP_URL`: the hosted app origin used for Stripe return URLs.
- `STRIPE_SECRET_KEY`: server-side Stripe key used to create Checkout and Customer Portal sessions.
- `STRIPE_WEBHOOK_SECRET`: Stripe endpoint signing secret for `/billing/stripe/webhook`.
- `STRIPE_STARTER_PRICE_ID`, `STRIPE_PLUS_PRICE_ID`, `STRIPE_PRO_PRICE_ID`, `STRIPE_BUSINESS_PRICE_ID`: recurring monthly Price IDs mapped to Panda plans.

Optional billing overrides:

- `BILLING_SUCCESS_URL`: defaults to `PUBLIC_APP_URL/billing/success`.
- `BILLING_CANCEL_URL`: defaults to `PUBLIC_APP_URL/billing/cancel`.
- `STRIPE_API_BASE_URL`: defaults to `https://api.stripe.com`; use only for local tests against a fake Stripe server.
- `STRIPE_PRICE_PLAN_MAP`: comma-separated `price_id:plan` entries when using custom plan price names.

Required for Discord Premium Apps billing, when that channel is enabled:

- `DISCORD_STARTER_SKU_ID`, `DISCORD_PLUS_SKU_ID`, `DISCORD_PRO_SKU_ID`, `DISCORD_BUSINESS_SKU_ID`: Discord SKU IDs mapped to Panda plans.
- `DISCORD_SKU_PLAN_MAP` is available as a comma-separated map alternative.

Stripe Price setup:

- Plan prices must be recurring monthly prices matching public plan prices.
- Stripe Customer Portal must be configured in Stripe Dashboard before `/billing action:portal` can open successfully.
- Checkout sessions are created from `/billing action:upgrade ...`; do not expose unauthenticated public checkout endpoints.

## Health Checks

- `GET /healthz` reports configuration, Fiber, Discord gateway credentials, Discord webhook public key, the managed AI service key, SQLite, and local storage.
- `GET /readyz` returns unavailable when SQLite is not ready.
- `GET /metrics` emits Prometheus-style gauges for SQLite, configured integrations, schema version, queue depth, usage totals, and AI-service configuration.
- `/ops health` gives bot owners a Discord-side operational summary, including shard status. V1 reports `single-gateway-v1` when Discord credentials are configured and `disabled` when the gateway is not running.
- `/ops drain` stops the background worker from claiming new jobs.
- `/ops resume` resumes background job processing.
- `/ops incident action:enable` records incident mode in runtime state.
- `/ops reload` rechecks runtime dependencies.

Long Discord summaries are queued as `discord.interaction` jobs. If queue depth grows, inspect logs, AI-service health, entitlement checks, worker drain state, and incident state before scaling.

## Billing Operations

Panda grants entitlements only from verified Discord entitlement events or verified Stripe webhooks. Do not grant paid access from client-side redirects.

Required billing checks:

- Webhook handlers are idempotent and record every payment event.
- Every paid assistant, web search, schedule, composed-tool, storage, and music path checks entitlement before provider spend.
- Past-due accounts enter grace, then read-only or suspended states according to billing policy.
- Suspended and canceled guilds retain help, billing, export, delete, and support access.
- Discord Premium Apps and Stripe prices must stay in parity where Discord requires it.

Weekly reconciliation:

- Compare internal AI response counts, web search counts, storage bytes, and cost ledger records against provider dashboards and payment reports.
- Alert when gross margin drops below 45% for any plan cohort.
- Alert when a guild consumes more than 50% of its included quota in the first 20% of a billing period.

## Sharding And Scale Path

V1 intentionally runs one Discord gateway worker with one SQLite writer. Keep Fly Machines at a single primary app instance while SQLite is stored on one attached volume.

Before enabling multiple gateway shards or multiple Machines:

1. Move durable state to a multi-writer-safe plan: LiteFS with a single writable primary, Fly Postgres, or another explicit storage architecture.
2. Split background workers from gateway workers so only the intended process claims long-running jobs.
3. Add shard IDs and counts to structured logs and metrics.
4. Add queue backpressure, dead-letter queues, and per-guild concurrency caps.
5. Confirm `/ops health` reports shard count, connected shard IDs, worker drain state, and queue state before scaling.

## SQLite Backup

Use the store backup path from a maintenance command or one-off shell. The implementation uses SQLite `VACUUM INTO`, which creates a consistent backup file without requiring the app to stop.

Recommended destination pattern:

```text
/data/backups/panda-YYYYMMDD-HHMMSS.db
```

Copy backups off the Fly volume after creation. Keep the main database, WAL, and backup retention policy aligned with plan-based server data retention.

## SQLite Restore

1. Drain work with `/ops drain`.
2. Stop or scale down the app so SQLite has no active writer.
3. Copy the selected backup back to the Fly volume.
4. Move the existing database, WAL, and SHM files aside with timestamped names.
5. Place the backup at the configured SQLite path.
6. Start the app and check `/readyz`, `/ops health`, `/metrics`, and `fly logs`.
7. Reconcile subscription snapshots, quota reservations, and cost ledger rows created near the restore point.
8. Resume workers with `/ops resume` after the restored database passes health checks.

## Rollback

1. Inspect `fly releases`.
2. Roll back with `fly releases rollback <version>`.
3. Watch `fly logs`.
4. Confirm `/readyz`, `/metrics`, `/ops health`, and billing webhook delivery.

Schema migrations are forward-only. Restore from a SQLite backup if a bad migration corrupts data.

## Incident Mode

For managed AI trouble, enable incident mode, drain background work if needed, and disable assistant responses in affected guilds with `/admin disable`. Removing the AI service key and redeploying is a last-resort global stop; health checks will report the AI service as missing and assistant responses will stop before provider calls.

For billing webhook trouble, pause upgrades that depend on the affected channel, keep existing entitlement snapshots intact, replay webhooks after the fix, and verify idempotency before resuming self-serve upgrades.

For quota spikes or abuse, suspend the guild, revoke trial credits when warranted, preserve audit logs, and keep export/delete/support paths available.

## Customer Support Boundaries

Customer-facing support may include guild ID, plan, subscription status, quota usage, command failure counts, recent error codes, queue depth, and Discord permission gaps.

Do not include raw prompts, raw Discord messages, provider model names, API keys, billing secrets, hidden tools, internal cost math, or vendor fallback details in normal support responses or screenshots.
