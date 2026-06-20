# Panda Operations Runbook

## Local Verification

```bash
go test ./...
go test -tags sqlite_fts5 ./...
go vet ./...
```

The default test run covers the SQLite fallback search path. The tagged run covers the production FTS5 path used by the Docker image.

## Fly.io Deployment

1. Create one Fly volume mounted at `/data`.
2. Set `discord.application_id` and `discord.owner_user_ids` in `panda.config.json`, or provide `DISCORD_APPLICATION_ID` and `OWNER_USER_IDS` as environment overrides.
3. Set secrets with `fly secrets set`:
   - `DISCORD_BOT_TOKEN`
   - `OPENROUTER_API_KEY`
4. Deploy with `fly deploy`.
5. Check rollout status with `fly status`, `fly releases`, and `fly logs`.

Keep SQLite on a single primary Machine for v1. Do not scale writers horizontally until the storage plan changes.

## Health Checks

- `GET /healthz` reports configuration, Fiber, Discord, OpenRouter, SQLite, and local storage.
- `GET /readyz` returns unavailable when SQLite is not ready.
- `GET /metrics` emits Prometheus-style gauges for SQLite, configured integrations, schema version, queue depth, and usage totals.
- `/ops health` gives bot owners a Discord-side operational summary, including shard status. V1 reports `single-gateway-v1` when Discord credentials are configured and `disabled` when the gateway is not running.
- `/ops drain` stops the background worker from claiming new jobs.
- `/ops resume` resumes background job processing.
- `/ops incident action:enable` records incident mode in runtime state.
- Long Discord summaries are queued as `discord.interaction` jobs. If queue depth grows, check `fly logs`, OpenRouter health, and worker drain/incident state before scaling.
- `/ops reload` rechecks runtime dependencies.

## Sharding Plan

V1 intentionally runs one Discord gateway worker with one SQLite writer. Keep Fly Machines at a single primary app instance while SQLite is stored on one attached volume.

Before enabling multiple gateway shards or multiple Machines:

1. Move durable state to a multi-writer-safe plan: LiteFS with a single writable primary, Fly Postgres, or another explicit storage architecture.
2. Split background workers from gateway workers so only the intended process claims long-running jobs.
3. Add shard IDs and counts to structured logs and metrics.
4. Confirm `/ops health` reports shard count, connected shard IDs, and worker drain status before scaling.

## SQLite Backup

Use the store backup path from a maintenance command or one-off shell. The implementation uses SQLite `VACUUM INTO`, which creates a consistent backup file without requiring the app to stop.

Recommended destination pattern:

```text
/data/backups/panda-YYYYMMDD-HHMMSS.db
```

Copy backups off the Fly volume after creation. Keep the main database, WAL, and backup retention policy aligned with the server data-retention policy.

## SQLite Restore

1. Drain work with `/ops drain`.
2. Stop or scale down the app so SQLite has no active writer.
3. Copy the selected backup back to the Fly volume.
4. Move the existing database, WAL, and SHM files aside with timestamped names.
5. Place the backup at the configured SQLite path.
6. Start the app and check `/readyz`, `/ops health`, and `fly logs`.
7. Resume workers with `/ops resume` after the restored database passes health checks.

## Rollback

1. Inspect `fly releases`.
2. Roll back with `fly releases rollback <version>`.
3. Watch `fly logs`.
4. Confirm `/readyz` and `/ops health`.

Schema migrations are forward-only. Restore from a SQLite backup if a bad migration corrupts data.

## Incident Mode

For model-provider trouble, disable assistant responses with `/admin disable` in affected guilds or remove `OPENROUTER_API_KEY` from the runtime environment and redeploy. Existing health checks will report OpenRouter as missing and natural-language assistant responses will stop before calling the model.

Use `/ops drain` before maintenance that should not start new queued work. Use `/ops resume` after health checks pass.
