# Panda Discord Assistant

Panda is a Go Discord assistant bot implemented from `PLAN.md`.

## Local Development

```bash
go test ./...
go run ./cmd/bot
```

The service can start without Discord or OpenRouter credentials in development. Local tests use fake LLM clients and SQLite fixtures, so no real credentials are needed for development verification.

Non-secret settings live in `panda.config.json`. Set `PANDA_CONFIG=/path/to/config.json` to use another file. Environment variables are still supported for deployments and override file values when present.

For live Discord/OpenRouter integrations, set `DISCORD_BOT_TOKEN` and `OPENROUTER_API_KEY` in your shell or deployment secrets, then set `discord.application_id` in `panda.config.json` or provide `DISCORD_APPLICATION_ID`.

For queue-only processing without Discord or HTTP, run `go run ./cmd/worker`.

Health endpoints report configuration, Fiber, Discord, OpenRouter, SQLite, and local storage status:

- `GET /healthz`
- `GET /readyz`
- `GET /livez`
- `GET /metrics`

`/metrics` emits Prometheus-style local metrics for SQLite readiness, integration configuration, schema migration version, queue depth, and usage counters.

Discord gateway startup and command registration activate when `DISCORD_BOT_TOKEN` and a Discord application ID are configured. OpenRouter calls activate when `OPENROUTER_API_KEY` is set.

OpenRouter routing uses `openrouter.default_model` plus optional `openrouter.fallback_models` in `panda.config.json`. Guild admins can override the primary model, fallbacks, temperature, max response tokens, and tool policy with `/admin model`; transient OpenRouter/provider failures try the ordered fallback list before returning an error. The OpenRouter client also includes retries and a circuit breaker configured with `openrouter.circuit_breaker`.

SQLite knowledge search uses FTS5 when the binary is built with the `sqlite_fts5` tag. Default local builds fall back to an indexed table search so `go test ./...` works without custom flags. When `openrouter.embedding_model` is configured, admin-managed knowledge documents also store OpenRouter embeddings in SQLite; without it, memory remains keyword-search only.

## Commands

Panda listens to normal Discord messages that contain the word `Panda`, then uses the model to decide whether the message is meant for it. Natural requests like `Panda is this true?` and task-style asks are routed into chat without a slash command.

- `/ping`
- `/help`
- `/admin setup`
- `/admin model`
- `/admin prompt`
- `/admin audit`
- `/admin enable`
- `/admin disable`
- `/ops health`
- `/ops guilds`
- `/ops reload`
- `/ops drain`
- `/ops resume`
- `/ops incident`
- Message context menu: `Explain with Panda`
- Message context menu: `Summarize with Panda`

Role mappings are enforced when at least one `assistant.use` role is configured. Channel rules support explicit allow lists and deny rules; owners and guild administrators bypass assistant-use policy checks.

Usage reports, request budgets, server knowledge, role permissions, channel rules, memory consent, and moderation guidance are available through Panda chat/tools instead of direct slash commands.

When a chat-triggered tool prepares a destructive admin removal, Panda renders a Discord confirmation button tied to the requesting user. Clicking it executes the reviewed server-side action only after fresh permission checks.

Server knowledge is opt-in. User-specific memory consent is separate and defaults off.

Large summarize requests from Discord are queued as durable background jobs after permission, context, rate-limit, and budget checks. Panda updates the deferred Discord response when the job finishes; `/metrics` and `/ops health` expose queue depth for operators.

## Deployment Notes

Fly.io should mount persistent storage at `/data`; the included `fly.toml` and `Dockerfile` keep SQLite and temp data off the ephemeral root filesystem. The Docker build runs tests and builds with `sqlite_fts5` enabled for production search.

See `OPERATIONS.md` for deploy, rollback, backup, and incident notes.

## Planning Docs

- `PLAN.md` covers the original production bot plan.
- `DISCORD_TOOLS_PLAN.md` covers Discord tool coverage before self-extension.
- `MOD_EXTENSION_AGENT_PLAN.md` covers moderator-created composed tools and agents.
