# Panda Discord Assistant

Panda is a Go Discord assistant bot implemented from `PLAN.md`.

## Local Development

```bash
cp .env.example .env
go test ./...
go run ./cmd/bot
```

The service can start without Discord or OpenRouter credentials in development. Local tests use fake LLM clients and SQLite fixtures, so no real credentials are needed for development verification.

For queue-only processing without Discord or HTTP, run `go run ./cmd/worker`.

Health endpoints report configuration, Fiber, Discord, OpenRouter, SQLite, and local storage status:

- `GET /healthz`
- `GET /readyz`
- `GET /livez`
- `GET /metrics`

`/metrics` emits Prometheus-style local metrics for SQLite readiness, integration configuration, schema migration version, queue depth, and usage counters.

Discord gateway startup and command registration activate when `DISCORD_BOT_TOKEN` and `DISCORD_APPLICATION_ID` are set. OpenRouter calls activate when `OPENROUTER_API_KEY` is set.

OpenRouter routing uses `OPENROUTER_DEFAULT_MODEL` plus optional comma-separated `OPENROUTER_FALLBACK_MODELS`. Guild admins can override the primary model, fallbacks, temperature, max response tokens, and tool policy with `/admin model`; transient OpenRouter/provider failures try the ordered fallback list before returning an error. The OpenRouter client also includes retries and a circuit breaker configured with `OPENROUTER_CIRCUIT_FAILURE_THRESHOLD` and `OPENROUTER_CIRCUIT_COOLDOWN`.

SQLite knowledge search uses FTS5 when the binary is built with the `sqlite_fts5` tag. Default local builds fall back to an indexed table search so `go test ./...` works without custom flags. When `OPENROUTER_EMBEDDING_MODEL` is configured, admin-managed knowledge documents also store OpenRouter embeddings in SQLite; without it, memory remains keyword-search only.

## Commands

- `/ping`
- `/help`
- `/admin setup`
- `/admin model`
- `/admin usage`
- `/admin limits`
- `/admin prompt`
- `/admin memory`
- `/admin roles`
- `/admin channels`
- `/admin audit`
- `/admin enable`
- `/admin disable`
- `/ops health`
- `/ops guilds`
- `/ops reload`
- `/ops drain`
- `/ops resume`
- `/ops incident`
- `/mod triage`
- `/mod note`
- `/mod slowmode`
- `/mod cleanup`
- `/mod history subject_id:<user_id>` with optional `recent_limit:<n>` and moderator note text
- `/ask question:<text>`
- `/chat question:<text>`
- `/summarize text:<text>`, `attachment_id:<id>`, or `message_id:<id>` / `recent_limit:<n>` when Discord context fetching is configured
- `/explain text:<text>`, `attachment_id:<id>`, or `message_id:<id>` when Discord context fetching is configured
- `/rewrite text:<text> tone:<tone>`
- `/translate text:<text> language:<language>`
- `/search-memory query:<text>`
- `/memory-consent action:<status|enable|disable>`
- Message context menu: `Explain with Panda`
- Message context menu: `Summarize with Panda`

Role mappings are enforced when at least one `assistant.use` role is configured. Channel rules support explicit allow lists and deny rules; owners and guild administrators bypass assistant-use policy checks.

Moderator helpers are suggestion-only and require guild administrator, owner, or `moderation.use` role permission. Durable request budgets can be configured with `/admin limits`.

Server knowledge is opt-in through `/admin memory enable`. User-specific memory consent is separate and defaults off; users can inspect or change it with `/memory-consent`.

Large `/summarize` requests from Discord are queued as durable background jobs after permission, context, rate-limit, and budget checks. Panda updates the deferred Discord response when the job finishes; `/metrics` and `/ops health` expose queue depth for operators.

## Deployment Notes

Fly.io should mount persistent storage at `/data`; the included `fly.toml` and `Dockerfile` keep SQLite and temp data off the ephemeral root filesystem. The Docker build runs tests and builds with `sqlite_fts5` enabled for production search.

See `OPERATIONS.md` for deploy, rollback, backup, and incident notes.
