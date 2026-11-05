# Untangling decisions

Scope: preserve public API behavior, account/cache file formats, and upstream compatibility. Both optional comment and test cleanup were selected. Baseline `go test ./...` passes at `1da5476`.

## Design and problem inventory

- Make `turn` the shared request/event vocabulary and `models` the catalog domain; keep transport and background fetching outside these stable packages.
  - OpenAI and Anthropic import Codex solely for aliases of `turn` types.
  - Anthropic imports OpenAI for `ModelNotFoundError` and schema normalization.
  - `models.Fetcher` brings account-manager, configuration, and transport dependencies into every catalog consumer. Its fields repeat services already owned by `server.App`.
  - `models/normalize.go` depends on private upstream wire types.
  - `Fetcher.routeAccounts` uses a map and duplicate-key branch although routing keys uniquely identify accounts.
- Keep schema normalization together, and colocate each protocol's paired schema transformations.
  - Shared schema traversal/normalization lives inside the OpenAI adapter.
  - Anthropic's request strict-nullability conversion and response null removal are split between large request/response files despite needing to change together.
- Encapsulate accumulation and group response formatting by responsibility.
  - Terminal events hydrate output and usage twice in `turn.Accumulator`.
  - Mutable text builders, output storage, and emitted-event flags are exported although callers do not need them.
  - A test assigns output storage directly instead of applying a response event.
- Make account policy explicit and reusable across server workflows.
  - `Service.List/Get` normalize account state, then admin/model discovery re-query `EligibleNow` per record, scanning and locking the whole store again. Model discovery also repeats status/token checks.
  - Missing-account errors are separate fresh strings across service/manager/admin; no-eligible-account detection matches error-message text in text, image, WebSocket, and Anthropic workflows.
  - Text, compaction, and native images duplicate selection/readiness and model/default resolution rules.
  - Implicit continuation application is repeated in primary-bucket and fallback searches.
  - `server/public.go` mixes HTTP handlers, account routing, transport opening, event collection, continuation persistence, chat encoding, and error encoding.
- Let Codex own upstream transport policy and event decoding.
  - Two server paths construct the same WebSocket authentication headers, private endpoint, request IDs, and initial body.
  - `turn.NormalizedRequest` builds a transport-specific WebSocket payload.
  - WebSocket events use `json.Unmarshal`, while SSE uses a number-preserving decoder.
  - HTTP and stream errors separately interpret retry/reset fields.
  - URL/header helpers expose implementation details used only inside Codex.
- Define the desktop identity once for OAuth and upstream requests.
  - The full user-agent/version is duplicated in Codex and OAuth; Git commit `0c0f751` had to update both.
  - Chromium preset and major version are separate values that must agree.
- Keep pending-login interpretation at the OAuth boundary.
  - Codexauth treats 403/404 as pending, while device-login orchestration interprets error-message substrings for the same state.
- Share the existing JSON-file write contract.
  - Account and model-cache stores independently indent JSON, append a newline, create directories, write a mode-0600 temporary file, and rename it.
- Clean comments and tests after structural changes.
  - Utility/stream comments restate implementation; external protocol/source explanations remain valuable.
  - An account persistence test races a 10ms deadline and sleeps to expire a cooldown.
  - An OAuth test calls the private JWT parser rather than observing refresh results.
  - A logger test only checks absence of secrets and passes even if nothing is logged.

## Required special cases retained

- Public API differences: legacy functions/custom tools; Responses-shaped Chat requests; raw forwarding of unfamiliar upstream input items; tuple conversion; nullable schema handling; Anthropic version/header/error envelopes and `max_tokens=0`; incomplete tool-call suppression; image token estimates.
- Continuity: signed reasoning, scoped Claude replay repair, exact transcript-prefix/tool-ID validation, explicit account affinity, stateless replay/fallback, and one retry without invalid encrypted reasoning. Claude replay and Responses continuation have distinct contracts.
- Upstream transport: hosted web search and `generate` require WebSocket; native image endpoints fall back on 404/405/501; JSON image responses can become SSE; client cancellation stops failover; account failover is limited to retryable failures.
- Account policy: missing quota falls back to round robin; least-used compares windows by usage; code-review limits do not block general routing; expired windows normalize; transient refresh failures cool down while `invalid_grant` expires; omitted refresh tokens/account IDs preserve prior values; cached usage works for disabled accounts; legacy records may omit user IDs.
- Background timing: model refresh waits one second initially, retries after ten seconds until successful, then refreshes hourly; device login honors the issuer interval with a five-second minimum and a deadline; housekeeping expires memory-only state.
- External errors: Retry-After/reset metadata, generic 403 cooldown versus Cloudflare challenge, best-effort optional cache loading, and original structured text on reconversion failure.

## Verification and review

All inventory items above are resolved. The Codex alias facade and fetcher wrapper are removed. Adapters now depend on `turn`, `models`, and `schema`; `models` no longer imports account-manager, configuration, or upstream transport. Codex owns backend metadata normalization, WebSocket requests, headers, and error interpretation. Server orchestration is grouped into routing, upstream streaming, session state, and public encoders.

The accumulator review established that final response output must override conflicting root output. That precedence remains; repeated usage hydration was consolidated. The existing terminal-field test now proves this behavior. Wire serialization helpers moved from `turn` into the server.

Behavior improvements arising from the shared transport decoder: WebSocket JSON integers retain their precision, stream errors honor `retry_after` as HTTP errors do, and SSE rejects concatenated JSON values consistently with WebSocket. Public API envelopes, routing rules, and persisted file formats remain unchanged.

Three independent cross-reviews checked protocol transformations, server/continuation/account behavior, and transport/auth/model/storage boundaries. They identified redundant private model-normalization guards and stale documentation references; both were corrected. A final recheck found no worthwhile remaining issues. Required compatibility cases are listed above rather than removed.

Passed checks:

- Baseline and refactored `go test ./...`.
- Final `go test -race ./...` and `go vet ./...`.
- `go test -tags=live ./test/integration -run '^$'` compiles the optional live suite.
- Real HTTPS/SSE and WebSocket fixtures verify authentication/turn headers, request payload policy, exact large integers, retry metadata, malformed-event rejection, and model normalization.
- An external-package app test runs the real server constructor and HTTP routes against a WebSocket upstream: first-account rate limiting fails over, the 120-second cooldown persists, account storage remains mode 0600, and Responses, Anthropic, compaction, and images retain their no-account responses.
- The compiled `cmd/api` binary ran with isolated temporary state. Checked liveness, authentication, health, models, unavailable-account responses, rotation updates, mode-0600 persistence, restart reload, and graceful SIGTERM shutdown. Temporary processes and data were removed.
- `git diff --check` and a stale-symbol/reference sweep.

Live traffic against real Codex accounts was not run. Existing endpoint, streaming, account rotation, schema round-trip, and replay coverage remains the regression safety net.
