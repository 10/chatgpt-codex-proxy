<div align="center">

# chatgpt-codex-proxy

*Talk to ChatGPT Codex accounts using any OpenAI or Anthropic client.*

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![Docker](https://img.shields.io/badge/Deploy-Compose-2496ED?style=flat&logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![API](https://img.shields.io/badge/API-OpenAI%20%2B%20Anthropic-412991?style=flat&logo=openai&logoColor=white)]()
[![License](https://img.shields.io/badge/License-MIT-yellow?style=flat)](LICENSE)

</div>

---

Codex lives behind a private API that only the official client speaks. Everything
else — the SDKs, Claude Code, the tool you already use — speaks OpenAI or
Anthropic.

`chatgpt-codex-proxy` sits between them. It exposes OpenAI-compatible and
Anthropic Messages endpoints, translates each request into the private
`chatgpt.com/backend-api/codex/*` format, and routes it across one or more
locally authenticated Codex accounts.

The upstream surface is private and undocumented, so it may change without
notice. This is built for local and small-scale deployments.

## Features

- **Two API surfaces, one backend** — OpenAI Chat Completions, Responses, and Images alongside Anthropic Messages, all served from the same account pool.
- **Streaming everywhere** — SSE for HTTP, a persistent WebSocket for Responses sessions, and plain JSON when a client prefers it.
- **Multi-account rotation** — least-used, round-robin, or sticky selection, with cooldowns, quota awareness, and per-account status.
- **Device login onboarding** — add an account by opening a URL and polling. No cookie scraping, no hand-pasted tokens.
- **Tool calling and structured output** — custom tools, legacy `functions` and `function_call`, `json_schema`, and `json_object`.
- **Images** — generation and edits through Codex's native endpoints, with a Responses-tool fallback when they are unavailable.
- **Conversation continuation** — explicit `previous_response_id`, plus guarded implicit continuation when prior history is replayed.
- **Local state only** — accounts, OAuth tokens, and the model catalog live in JSON files you own.

## Quick Start

You need Docker, or Go `1.26.x`, plus a long random string for `PROXY_API_KEY`.

```bash
cp .env.example .env          # set PROXY_API_KEY
docker compose up -d --build  # or: go run ./cmd/api
```

The examples below assume:

```bash
export PROXY_URL=http://localhost:8080
export PROXY_API_KEY=change-me-to-a-long-random-string
```

Add a Codex account. Start a device login, open the returned `auth_url`,
complete it, then poll until `status` is `ready`:

```bash
curl -sS -X POST "${PROXY_URL}/admin/accounts/device-login/start" \
  -H "Authorization: Bearer ${PROXY_API_KEY}"

curl -sS "${PROXY_URL}/admin/accounts/device-login/<login_id>" \
  -H "Authorization: Bearer ${PROXY_API_KEY}"
```

Then make a request:

```bash
curl -sS "${PROXY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${PROXY_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.6-sol",
    "messages": [{ "role": "user", "content": "Explain what this repository does." }]
  }'
```

## Clients

Point any OpenAI client at `http://localhost:8080/v1` with your `PROXY_API_KEY`
as the API key. For Claude Code or another Anthropic client:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_API_KEY="${PROXY_API_KEY}"
export ANTHROPIC_MODEL=gpt-5.6-sol
```

Use a model ID from `GET /v1/models`. The proxy does not invent `claude-*`
aliases for Codex models.

Every route except `GET /health/live` requires the key, accepted as either
`Authorization: Bearer <key>` or `X-API-Key: <key>`. The same key protects
public and admin routes alike.

## API

```
POST /v1/completions              POST /v1/messages
POST /v1/chat/completions         POST /v1/messages/count_tokens
POST /v1/responses                GET  /v1/models
GET  /v1/responses   (WebSocket)  GET  /v1/models/:model_id
POST /v1/responses/compact        GET  /health
POST /v1/images/generations       GET  /health/live
POST /v1/images/edits
```

Text, image, and file inputs are supported, along with reasoning, hosted web
search passthrough, and both streaming and non-streaming responses. The runtime
model catalog is backed by the upstream Codex list.

Behavior worth knowing before you hit it:

- `GET /v1/responses` is a WebSocket, not SSE. The first message must be `response.create`; later turns use `response.create` or `response.append` with array `input`. There is no `[DONE]` marker.
- `/v1/chat/completions` also accepts a Responses-shaped body when `messages` is omitted.
- `/v1/responses/compact` returns `object: "response.compaction"` per the public OpenAI contract, and expands locally stored history for an explicit `previous_response_id`.
- JSON endpoints accept identity or zstd request bodies. Other content encodings are rejected.
- Images default to `gpt-image-2` on Codex's native endpoints. If those return `404`, `405`, or `501`, the proxy falls back to the Responses `image_generation` tool, which produces one image and returns a data URL for `response_format: "url"`.
- WebSocket continuations reuse the upstream connection; HTTP and compact continuations replay saved history and prefer the original account.
- Anthropic requests require `Anthropic-Version: 2023-06-01`. Sampling controls, stop sequences, exact `max_tokens` truncation, and thinking budgets are accepted but advisory, because the private Codex request exposes no equivalent.
- Audio input is rejected locally — the Codex upstream does not accept `input_audio` or audio MIME types.

Exact translation rules live in [docs/TRANSLATION.md](docs/TRANSLATION.md) and
[docs/ANTHROPIC.md](docs/ANTHROPIC.md).

## Accounts

```
GET    /admin/accounts
POST   /admin/accounts/device-login/start
GET    /admin/accounts/device-login/:login_id
DELETE /admin/accounts/:account_id
PATCH  /admin/accounts/:account_id
GET    /admin/accounts/:account_id/usage
POST   /admin/accounts/:account_id/refresh
GET    /admin/rotation
PUT    /admin/rotation
```

The admin API lists known accounts with their status, eligibility, cooldown, and
cached quota; drives device login; updates an account's `label` or `status`;
refreshes OAuth tokens; and reads or changes the rotation strategy.

Rotation is `least_used`, `round_robin`, or `sticky`. An account is skipped when
its status is permanent (`disabled`, `expired`, `banned`), a cooldown is active,
its token is missing, or its primary and secondary quota are exhausted.
`code_review_rate_limit` is tracked for observability and does not affect
routing.

OAuth refresh failures only expire an account when the provider returns
`invalid_grant`. Transient failures keep it active behind a 60-second cooldown.

Selection details are in
[docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md](docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md).

## Deployment

`compose.yaml` is the recommended path. It persists state in the
`chatgpt-codex-proxy-data` volume and runs the service with basic hardening;
`Dockerfile` is a multi-stage build of the API server on its own.

```bash
docker compose up -d --build
docker compose logs -f
docker compose down
```

Configuration is environment-only:

- `PROXY_API_KEY` — required; the key for every route.
- `PORT` — default `8080`.
- `DATA_DIR` — default `data` locally, `/app/data` in Docker.
- `DEBUG_LOG_PAYLOADS` — default `false`. Logs raw public request JSON and translated upstream payloads.

Persisted to `${DATA_DIR}` are `accounts.json` (accounts, OAuth tokens, labels,
status, cached quota, cooldowns) and `models-cache.json` (the last good catalog
snapshot). Continuation mappings, conversation affinity, and in-flight device
logins are memory-only and do not survive a restart.

## How It Works

1. The Gin server accepts an OpenAI- or Anthropic-shaped request.
2. Protocol adapters normalize it into one internal turn model.
3. The proxy picks a ready account, translates the turn, and calls the private Codex backend over HTTP SSE or WebSocket.
4. Upstream events are converted back into whichever public protocol the client asked for.

<img width="4599" height="2073" alt="chatgpt-codex-proxy architecture flowchart" src="https://github.com/user-attachments/assets/05cd8446-dd4b-43bc-a3fc-eb370ad917e6" />

Upstream endpoints the proxy talks to:

```
POST https://chatgpt.com/backend-api/codex/responses
POST https://chatgpt.com/backend-api/codex/responses/compact
GET  https://chatgpt.com/backend-api/codex/usage
GET  https://chatgpt.com/backend-api/codex/models
WSS  https://chatgpt.com/backend-api/codex/responses
     https://auth.openai.com/api/accounts/deviceauth/*
     https://auth.openai.com/oauth/token
```

## Layout

```
chatgpt-codex-proxy/
├── cmd/api/                  # server entrypoint
├── internal/
│   ├── server/               # Gin routing and handlers
│   ├── openai/ anthropic/    # public protocol adapters
│   ├── turn/ translate/      # internal turn model and translation
│   ├── codex/ codexauth/     # private upstream client and OAuth
│   ├── accounts/             # account store
│   ├── accountmanager/       # rotation, cooldowns, quota routing
│   ├── devicelogin/          # device-auth onboarding
│   ├── conversation/         # continuation state and affinity
│   ├── models/               # runtime model catalog
│   ├── admin/ middleware/    # admin API and auth
│   └── store/ config/        # persistence and configuration
├── test/integration/         # live compatibility suite
└── docs/                     # upstream and translation references
```

## Development

```bash
go test ./...
```

Live compatibility tests run against a proxy you already have up:

```bash
OPENAI_API_KEY=change-me-to-a-long-random-string \
OPENAI_MODEL=gpt-5.6-sol \
OPENAI_BASE_URL="${PROXY_URL}/v1" \
go test -tags=live ./test/integration -v -count=1
```

The live suite exercises both the OpenAI and Anthropic routes.

## Docs

- [docs/TRANSLATION.md](docs/TRANSLATION.md) — exact OpenAI-to-Codex translation behavior and compatibility rules
- [docs/ANTHROPIC.md](docs/ANTHROPIC.md) — Anthropic request mapping, streaming, token counting, and limits
- [docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md](docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md) — account selection and quota routing
- [docs/CODEX_API_DOCS.md](docs/CODEX_API_DOCS.md) — private upstream Codex behavior, as inferred from this codebase

## Limitations

- The upstream Codex backend is private and may change without notice.
- Device auth is the only onboarding flow.
- Continuation state is in memory and expires with the configured TTL.
- The implementation is deliberately small and does not chase every edge of the public OpenAI platform.
