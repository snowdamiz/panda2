# Panda Discord Assistant

Panda is a Go Discord assistant bot implemented from `PLAN.md`.

## Local Development

```bash
make start
make logs
make stop
```

`make start` builds and starts the full local dev stack in the background. The stack is the Panda app process: it starts the HTTP server, opens and migrates SQLite at `data/panda.db` by default, and runs the queue worker. There is no separate database daemon to start for local development.

Use `make stop` to stop the background stack, `make status` to check it, and `make restart` after config changes. For a foreground process you can interrupt with `Ctrl+C`, run:

```bash
make dev
```

```bash
go test ./...
```

The service can start without Discord or OpenRouter credentials in development. Local tests use fake LLM clients and SQLite fixtures, so no real credentials are needed for development verification.

Non-secret settings live in `panda.config.json`. Secret settings can live in `.env`; Panda reads that file automatically for `make dev`, `make start`, and `go run ./cmd/bot`, so you do not need to export or source it first. Set `PANDA_CONFIG=/path/to/config.json` to use another config file, or `PANDA_ENV_FILE=/path/to/.env` to use another env file. Real shell/deployment environment variables are still supported and override `.env` and config file values when present.

For live Discord/OpenRouter integrations, set `DISCORD_BOT_TOKEN` and `OPENROUTER_API_KEY` in `.env`, your shell, or deployment secrets, then set `discord.application_id` in `panda.config.json` or provide `DISCORD_APPLICATION_ID`.

When `DISCORD_GUILD_ID` is set for fast local command registration, that guild must have the app installed. Guild-scoped command sync clears global command registrations first so Discord does not show both global and guild copies in the same server. In development, Panda logs Discord command registration access errors and keeps the gateway running so message/webhook testing can continue.

Set `DISCORD_PUBLIC_KEY` from the Discord Developer Portal to enable signed Discord webhook events at `POST /discord/webhook-events`. Subscribe that endpoint to `APPLICATION_AUTHORIZED` events to enforce owner-only guild installs: Panda records the authorizing user as the guild's Panda owner when they match Discord's `guild.owner_id`; if a non-owner authorizes the app, Panda records the denial, audits it, and leaves the guild.

To enable public web search, set `BRAVE_SEARCH_API_KEY`. Panda exposes Brave Search to the model as the read-only `web.search` tool when the key is configured, the guild tool policy allows read tools, and the caller has `assistant.web_search` permission. The optional `brave_search.base_url` setting defaults to `https://api.search.brave.com/res/v1`.

For queue-only processing without Discord or HTTP, run `go run ./cmd/worker`.

Health endpoints report configuration, Fiber, Discord gateway credentials, Discord webhook public key, OpenRouter, Brave Search, SQLite, and local storage status:

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
- `/admin badge` to delegate Panda admin access to a role
- `/admin tool` to allow a role to use a specific native or composed tool
- `/admin model`
- `/admin prompt`
- `/admin soul`
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

Guild config is created automatically the first time an admin changes Panda settings. The installing owner can use `/admin badge` to choose any Discord role as Panda's admin badge; members with that badge are treated as Panda admins, so servers can use custom labels like `MOD` instead of Discord's built-in Administrator permission. Role mappings are enforced when at least one `assistant.use` role is configured. Channel rules support explicit allow lists and deny rules; owners, guild administrators, and the configured admin badge bypass assistant-use policy checks.

Tool access has two layers: `tool_policy` sets the server-wide ceiling for tool classes, and `/admin tool` can restrict individual native or composed tools to specific roles. Native tools keep their underlying permissions, so allowing a role to use an admin tool does not grant admin access. Composed tools are admin-only for regular members until a role is explicitly allowed for that composed tool; composed tools that wrap native admin tools remain admin-only.

Usage reports, request budgets, server knowledge, role permissions, channel rules, memory consent, moderation guidance, and composed-tool management are available through Panda chat/tools instead of direct slash commands.

When a chat-triggered tool prepares a destructive admin removal or a composed-tool approval/rollback, Panda renders a Discord confirmation button tied to the requesting user. Clicking it executes the reviewed server-side action only after fresh permission checks.

Server knowledge is opt-in. User-specific memory consent is separate and defaults off.

Large summarize requests from Discord are queued as durable background jobs after permission, context, rate-limit, and budget checks. Panda updates the deferred Discord response when the job finishes; `/metrics` and `/ops health` expose queue depth for operators.

## Deployment Notes

Fly.io should mount persistent storage at `/data`; the included `fly.toml` and `Dockerfile` keep SQLite and temp data off the ephemeral root filesystem. The Docker build runs tests and builds with `sqlite_fts5` enabled for production search.

See `OPERATIONS.md` for deploy, rollback, backup, and incident notes.

## Planning Docs

- `PLAN.md` covers the original production bot plan.
- `DISCORD_TOOLS_PLAN.md` covers Discord tool coverage before self-extension.
- `MOD_EXTENSION_AGENT_PLAN.md` covers moderator-created composed tools and agents.
