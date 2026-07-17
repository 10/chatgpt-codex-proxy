# Multi-Account Rotation Strategy

This document explains exactly how account rotation works in `chatgpt-codex-proxy` today.

It is written to match the current implementation in:

- [internal/accounts/service.go](../internal/accounts/service.go)
- [internal/accountmanager/manager.go](../internal/accountmanager/manager.go)
- [internal/server/app.go](../internal/server/app.go)
- [internal/server/public.go](../internal/server/public.go)
- [internal/server/admin.go](../internal/server/admin.go)

If the code changes, this file should be updated to stay authoritative.

## Overview

The proxy can hold multiple authenticated ChatGPT Codex accounts and choose one account for each incoming request.

The selection model is intentionally simple:

- There is no local token accounting.
- There is no local request history.
- There is no persistent "usage ledger" maintained by the proxy.
- Rotation decisions are based on cached upstream quota data plus a simple transient cooldown field.

The proxy supports three public rotation strategies:

- `least_used`
- `round_robin`
- `sticky`

Before any strategy runs, the proxy filters the account list down to accounts that are currently eligible.

## Core Model

Each account record stores a small amount of state that matters for routing:

- `status`
  Permanent status. One of `active`, `disabled`, `expired`, or `banned`.
- `cached_quota`
  The most recent upstream quota snapshot the proxy has seen for that account.
- `cooldown_until`
  A temporary "do not route to this account until this time" marker.
- `last_error`
  A human-readable explanation for the latest cooldown or permanent failure.
- `token`
  OAuth token state, including access token and expiry.

Important non-goals of the current design:

- The proxy does not estimate usage itself.
- The proxy does not track "requests sent by this proxy" for ranking.
- The proxy does not keep historical quota snapshots over time.

## Where Cached Quota Comes From

Cached quota is updated passively or manually from upstream signals:

- HTTP response headers
- `codex.rate_limits` stream events
- Explicit admin usage lookups via `GET /admin/accounts/:account_id/usage` when `cached != true`

There is no background polling loop for quota.

That means account ranking is only as fresh as the latest upstream quota information the proxy has seen.

## Eligibility Rules

An account must pass all of the following checks before it can be selected for routing.

### 1. Permanent status must be `active`

Accounts in these states are excluded:

- `disabled`
- `expired`
- `banned`

### 2. Access token must exist

If `token.access_token` is empty, the account is ineligible.

### 3. Cooldown must be absent or expired

If `cooldown_until` exists and is still in the future, the account is ineligible.

If `cooldown_until` is in the past, the proxy clears it automatically during normal refresh checks and the account becomes eligible again.

### 4. Cached quota must not show general-routing exhaustion

Cached quota blocks normal routing only in these cases:

- Primary `allowed == false`, unless its reset is already past
- Primary `limit_reached == true` with no reset timestamp or a reset still in the future
- Secondary `limit_reached == true` with no reset timestamp or a reset still in the future

`code_review_rate_limit` does not block normal routing.

This is an intentional rule. The code-review budget is treated as observability data only, not as a global account block.

## What "General Routing" Ignores

These fields do not influence normal account selection:

- `code_review_rate_limit`
- `credits`
- `last_error` by itself
- account label
- account email
- account creation time, except as a stable list ordering fallback in admin output

## How Stale Quota Windows Are Normalized

Whenever accounts are loaded or refreshed, the proxy normalizes expired quota windows.

If a quota window has a `reset_at` in the past, the proxy clears:

- `limit_reached`
- `used_percent`
- `reset_at`

This prevents stale quota snapshots from blocking routing forever.

This normalization applies to:

- primary rate limit
- secondary rate limit
- code review rate limit

## Strategy Selection Order

Routing is a two-step process:

1. Build the eligible account list
2. Apply the configured strategy to that eligible list

The account service also accepts a preferred account ID:

- If a `preferredID` is provided and that account is still eligible, it is used immediately.
- If a `preferredID` is provided but the account is no longer eligible, the proxy falls back to the configured global strategy.

The streaming Responses paths add stricter continuation handling above this generic service behavior, as described below.

## `round_robin`

`round_robin` is the simplest strategy.

Behavior:

- Start with eligible accounts only
- Sort them by internal account ID
- Pick `eligible[index % len(eligible)]`
- Increment the shared round-robin index

Implications:

- Ineligible accounts are skipped completely
- Order is deterministic for a fixed account set
- The round-robin pointer is process-local memory
- Restarting the process resets the index

### Example

Eligible accounts:

- `acct_a`
- `acct_b`
- `acct_c`

Requests will route like this:

1. `acct_a`
2. `acct_b`
3. `acct_c`
4. `acct_a`
5. `acct_b`

If `acct_b` becomes ineligible because of cooldown, the sequence becomes:

1. `acct_a`
2. `acct_c`
3. `acct_a`
4. `acct_c`

## `least_used`

`least_used` is quota-centric, not proxy-history-centric.

It does not ask "which account has served the fewest requests through this proxy?"

It asks:

"Based on the cached upstream quota data we currently have, which eligible account looks least pressured right now?"

### Step 1: Split candidates into two groups

Eligible accounts are separated into:

- accounts with usable cached quota
- accounts without usable cached quota

An account is considered to have usable cached quota only if:

- `cached_quota` exists
- primary `used_percent` exists

If primary `used_percent` is missing, the account is treated as "no usable cached quota" even if other quota fields exist.

### Step 2: If no eligible account has usable cached quota

The strategy falls back to `round_robin` across eligible accounts.

### Step 3: Rank eligible accounts that have usable cached quota

The comparator is applied in this exact order:

1. Lower primary `used_percent`
2. Lower secondary `used_percent`, but only when both accounts have secondary values
3. Earlier primary `reset_at`, but only when both accounts have primary reset values
4. If still tied, they are treated as equal-best candidates and the proxy round-robins among the tied subset

Accounts without usable cached quota are not compared against the ranked group at all. They are fallback candidates only, behind all eligible accounts that have primary `used_percent`.

### Important details

- `code_review_rate_limit` is ignored completely for `least_used`
- Credits are ignored
- Proxy-local request counts are ignored
- Token counts are ignored
- `last_error` is ignored unless it corresponds to current ineligibility through cooldown or permanent status

### Example 1: Lower primary usage wins

Eligible accounts:

- `acct_a`: primary `used_percent = 78`
- `acct_b`: primary `used_percent = 32`

Selection:

- `acct_b` wins because `32 < 78`

### Example 2: Primary tie, secondary breaks the tie

Eligible accounts:

- `acct_a`: primary `50`, secondary `80`
- `acct_b`: primary `50`, secondary `20`

Selection:

- `acct_b` wins because the primary usage is tied and secondary usage is lower

### Example 3: Primary and secondary tie, earlier reset wins

Eligible accounts:

- `acct_a`: primary `70`, reset at `12:30`
- `acct_b`: primary `70`, reset at `12:10`

Selection:

- `acct_b` wins because the primary reset is earlier

### Example 4: Exact tie

Eligible accounts:

- `acct_a`: primary `40`
- `acct_b`: primary `40`

No secondary values, no reset values.

Selection:

- The proxy round-robins between `acct_a` and `acct_b`
- It does not permanently prefer one over the other

### Example 5: Unknown quota is a fallback

Eligible accounts:

- `acct_a`: no cached quota
- `acct_b`: primary `used_percent = 60`

Selection:

- `acct_b` wins because known quota beats unknown quota

If all eligible accounts have no usable cached quota, only then does `least_used` degrade to round-robin.

## `sticky`

`sticky` prefers reuse of the last successfully used eligible account.

This state is memory-only and is not persisted.

### How sticky affinity is set

The proxy calls `NoteSuccess(accountID)` only after a request completes successfully.

That happens after:

- a non-streaming response is fully collected and completed
- a streaming response reaches `response.completed`

If a request fails before completion, the proxy does not mark that account as the sticky winner for that request.

### Sticky selection logic

When the configured strategy is `sticky`:

1. Build the eligible account list
2. If `stickyAccountID` is set and that account is still eligible, use it
3. Otherwise fall back to `least_used`

### Important details

- Sticky is not persisted to disk
- Restarting the process clears sticky memory
- Sticky does not override ineligibility
- Sticky does not bypass cooldown
- Sticky does not bypass permanent status

### Example

Suppose:

- `acct_a` completed the last successful request
- `acct_b` is also healthy
- strategy is `sticky`

The next request will use `acct_a` again, as long as `acct_a` is still eligible.

If `acct_a` later hits cooldown:

- `sticky` cannot use it
- the proxy falls back to `least_used`

## Continuations and Preferred Account Routing

The proxy keeps short-lived continuation state in memory, including the account that created each response.

For explicit `previous_response_id` requests on `POST /v1/responses`, `POST /v1/chat/completions`, and the public Responses WebSocket:

- the original account is required; the global rotation strategy is not used as a fallback
- the account must still have a usable token and support the resolved model
- the continuation is not failed over to another account if the upstream WebSocket request fails
- failure to prepare the original account returns `continuation_account_unavailable`

These continuation paths call the account readiness check directly. They do not re-run the normal rotation eligibility filter, so a stored cooldown alone does not move the continuation to another account.

Implicit replay detection initially pins the matching continuation in the same way. If that implicit WebSocket resume fails, however, the proxy can retry the original full request through normal account selection.

`POST /v1/responses/compact` is different: saved history is expanded locally, and the original account is only preferred. If it cannot be selected, compact acquisition may use another eligible account that supports the model.

## Readiness Checks After Selection

Selecting an account is not the same as proving it is ready to send a request.

After the service chooses an account, `AccountManager.AcquireReady` runs readiness checks:

1. Acquire an eligible account from the rotation service
2. Check the current token state
3. If the token is close to expiry, refresh it
4. If refresh succeeds, update the stored auth state while preserving the existing refresh token when the provider does not return a replacement
5. If refresh fails with OAuth `invalid_grant`, mark the account `expired`
6. For other refresh failures, keep the account active and apply the built-in 60-second cooldown

This means the routing service decides eligibility based on current record state, and the account manager performs last-mile readiness before the upstream request is sent.

## Cooldown Behavior

Cooldown is the proxy's single transient unavailability mechanism.

It is used for temporary upstream failures such as:

- `429 Too Many Requests`
- `402 Payment Required` or quota exhaustion
- transient OAuth refresh failures

### How 429 cooldown is chosen

When the proxy classifies a failure as `429`:

1. Use `Retry-After` if present
2. Otherwise use cached primary quota reset if available
3. Otherwise use the built-in 60-second rate-limit fallback

### How 402 cooldown is chosen

When the proxy classifies a failure as quota exhaustion:

1. Use cached primary reset if available
2. Otherwise use cached secondary reset if available
3. Otherwise use the built-in 5-minute quota fallback

### What cooldown does not do

Cooldown does not change permanent status to something custom like "rate_limited" or "quota_exhausted".

The account usually remains:

- `status = active`

but is temporarily ineligible because:

- `cooldown_until > now`

## When Cooldown Clears

Cooldown clears in two ways.

### 1. Time passes

If `cooldown_until` is now in the past, the proxy clears it during normal refresh checks.

### 2. A fresh quota snapshot shows recovery

When `ObserveQuota` receives a fresh quota snapshot and the snapshot no longer blocks general routing:

- `cooldown_until` is cleared immediately

This lets the proxy recover early if the upstream quota cache proves the account is healthy again.

## Failure Handling

Failure classification can change permanent account status or apply a temporary cooldown.

### 401 Unauthorized

When the proxy classifies upstream failure as `401`:

- the account is marked `expired`

### 403 Forbidden

If the failure looks like a generic upstream access denial:

- the account remains `active`
- the account receives a temporary cooldown

If the 403 looks like a Cloudflare-style interstitial or challenge:

- the proxy does not change permanent account status or set an account cooldown automatically

Neither form permanently poisons the account state.

## Manual Admin Overrides

The admin API can change account state in ways that affect routing.

### `PATCH /admin/accounts/:account_id`

Supported admin statuses:

- `active`
- `disabled`

Behavior:

- Setting `active` clears:
  - `cooldown_until`
  - `last_error`
  - `cached_quota`
- Setting `disabled` clears:
  - `cooldown_until`
  - `last_error`

This is intentionally simple:

- `active` acts like a clean re-enable
- `disabled` takes the account out of routing immediately

## What `/admin/accounts` and `/admin/accounts/:id/usage` Mean

The admin surface is quota-oriented now.

### `GET /admin/accounts`

This returns, for each account:

- permanent status
- `eligible_now`
- `cooldown_until`
- `last_error`
- `cached_quota`
- token expiry

`eligible_now` is derived, not stored.

### `GET /admin/accounts/:account_id/usage`

This keeps the old route name, but it is no longer a local usage report.

It returns:

- permanent status
- `eligible_now`
- `cooldown_until`
- `last_error`
- `cached_quota`
- `quota_runtime` if an uncached refresh was requested
- quota source and fetch time

There is no `local_usage` field anymore.

## No Local Usage Accounting

This is worth stating clearly because it changes how people should reason about the system.

The proxy no longer tries to answer:

- How many requests has this account handled through the proxy?
- How many input or output tokens has this account used through the proxy?
- Which account is least used based on proxy-local traffic?

Instead, it answers:

- Which accounts are eligible right now?
- Which account looks least pressured based on cached upstream quota?
- Which account should be temporarily cooled down after a transient upstream failure?

## End-to-End Examples

### Example A: Normal `least_used` routing

Accounts:

- `acct_a`
  - status: `active`
  - cooldown: none
  - primary used: `82%`
- `acct_b`
  - status: `active`
  - cooldown: none
  - primary used: `25%`

Request flow:

1. Incoming `POST /v1/responses`
2. Both accounts are eligible
3. Strategy is `least_used`
4. `acct_b` wins because `25 < 82`
5. Request completes successfully
6. Sticky memory is updated to `acct_b`

### Example B: 429 on the selected account

Accounts:

- `acct_a`
  - active
  - primary used: `20%`
- `acct_b`
  - active
  - primary used: `10%`

Request flow:

1. Strategy chooses `acct_b`
2. Upstream returns `429`
3. Proxy sets `acct_b.cooldown_until`
4. `acct_b` stays `active`
5. The same request retries on `acct_a` before any client-visible output
6. Later requests skip `acct_b` until its cooldown clears

### Example C: Code review budget exhausted

Account:

- `acct_a`
  - active
  - code review rate limit exhausted
  - primary and secondary are still healthy

Result:

- `acct_a` is still eligible for normal chat/responses routing
- The exhausted code-review window does not remove the account from general rotation

### Example D: Continuation with preferred account

Suppose response `resp_123` was created on `acct_b`.

Later the client sends:

- `POST /v1/responses`
- with `previous_response_id = "resp_123"`

Behavior:

1. The proxy resolves the continuation state
2. It requires `acct_b` so upstream turn state is not replayed on a different account
3. It verifies that `acct_b` is ready and supports the continuation model
4. A cooldown alone does not reroute the continuation
5. If `acct_b` cannot be prepared, the request fails with `continuation_account_unavailable`

## Practical Mental Model

If you need a short way to think about the system, use this:

- `status` decides whether the account is fundamentally usable
- `cooldown_until` decides whether the account is temporarily parked
- `cached_quota` decides whether the account appears exhausted and how `least_used` ranks it
- `sticky` only remembers the last successful healthy account in memory
- explicit continuations remain pinned to their original account

## Summary

The current rotation system is deliberately small and predictable:

- Eligibility first
- Then one of three strategies
- Ranking based on cached upstream quota only
- No local usage counters
- One transient cooldown mechanism
- One in-memory sticky preference
- Safe in-request failover before client-visible output
- Explicit continuations require their original account

If you want to understand why a specific request chose a specific account, the right questions are:

1. Which accounts were eligible at that moment?
2. Was there a preferred continuation account?
3. Which global strategy was configured?
4. What did the latest cached primary and secondary quota say?
