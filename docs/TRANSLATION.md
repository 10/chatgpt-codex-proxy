# Translation Behavior

This document describes how `chatgpt-codex-proxy` translates between its OpenAI-compatible public API and the private ChatGPT Codex backend it actually talks to.

It is based on the current implementation, not an external specification. The code paths that define this behavior live primarily in:

- `internal/server/public.go`
- `internal/server/compact.go`
- `internal/server/images.go`
- `internal/server/session.go`
- `internal/translate/request.go`
- `internal/translate/response.go`
- `internal/translate/schema.go`
- `internal/translate/tuple.go`
- `internal/codex/http_client.go`
- `internal/codex/request.go`
- `internal/codex/ws_client.go`
- `internal/openai/types.go`

## Overview

The proxy keeps one shared streaming translation stack for Chat Completions and Responses, a dedicated compact path for `/v1/responses/compact`, and an Images API adapter over the Codex `image_generation` tool.

The high-level streaming pipeline is:

1. Accept OpenAI-compatible JSON on `/v1/chat/completions` or `/v1/responses`.
2. Normalize that payload into one internal Codex-style request model.
3. Resolve continuation state and account affinity.
4. Send the normalized request to the Codex backend over HTTP SSE or WebSocket.
5. Rebuild either OpenAI Chat Completions output or OpenAI Responses output from the upstream event stream.

The compact pipeline is:

1. Accept OpenAI-compatible JSON on `/v1/responses/compact`.
2. Normalize that payload into `translate.NormalizedCompactRequest`, which embeds `codex.CompactRequest`.
3. If `previous_response_id` is present, expand saved continuation history locally.
4. Call the private compact backend over JSON HTTP.
5. Rebuild an OpenAI-style `response.compaction` object.

The image pipeline is:

1. Accept JSON on `/v1/images/generations`, or JSON/multipart input on `/v1/images/edits`.
2. Build a forced `image_generation` tool call with `action: "generate"` or `action: "edit"`.
3. Put edit source images in the user message and an optional mask in `input_image_mask`.
4. Send the enclosing request through the normal Codex account and transport stack.
5. Translate the returned `image_generation_call` into an Images API JSON response or Images API SSE events.

## Canonical Internal Request

The canonical streaming request is `codex.Request` plus translation metadata in `translate.NormalizedRequest`.

The Codex request fields used by the proxy are:

- `model`
- `instructions`
- `input`
- `stream`
- `store`
- `tools`
- `tool_choice`
- `text`
- `reasoning`
- `service_tier`
- `previous_response_id`
- `prompt_cache_key`
- `include`

The translation-only fields are:

- `ModelExplicit`
- `TupleSchema`

The canonical compact request is `codex.CompactRequest` plus translation metadata in `translate.NormalizedCompactRequest`.

The Codex compact fields used by the proxy are:

- `model`
- `instructions`
- `input`
- `text`
- `reasoning`

The compact translation-only fields are:

- `ModelExplicit`
- `PreviousResponseID`
- `TupleSchema`

## Endpoint Entry Behavior

### `/v1/chat/completions`

`POST /v1/chat/completions` first reads the raw body and then chooses one of two parsers:

- If `messages` is present and non-empty, it is treated as a Chat Completions request.
- Otherwise, if the body has Responses-style fields like `input`, `instructions`, `previous_response_id`, `text`, or `reasoning`, it is accepted as a compatibility path and parsed as a Responses request.
- If neither shape has meaningful content, the proxy returns `400` with `request body must include chat messages or responses input`.

When the compatibility path is used, the request is normalized with `translate.Responses(...)`; the `/v1/chat/completions` handler still selects Chat Completions response shaping.

If both `messages` and Responses-style fields are present, `messages` wins.

### `/v1/responses`

`POST /v1/responses` binds directly to `openai.ResponsesRequest` and normalizes with `translate.Responses(...)`.

`GET /v1/responses` upgrades to a persistent WebSocket connection. The client sends one JSON `response.create` event per turn. WebSocket streaming is implicit, so a supplied `stream` field is ignored. `background: true` is rejected, while an optional `generate` boolean is forwarded only on the upstream WebSocket payload.

The connection processes turns sequentially. It reuses the same upstream WebSocket while the selected account remains the same; if account selection changes, it closes that upstream connection and opens another one. Each turn ends with `response.completed` or an `error` JSON message. There is no SSE `done` event or `[DONE]` sentinel.

Malformed JSON, unsupported event types, and other request-level protocol errors are returned as `type: "error"` messages without intentionally closing the client connection. An unknown `previous_response_id` uses the WebSocket-specific code `previous_response_not_found`.

### `/v1/responses/compact`

`POST /v1/responses/compact` binds directly to `openai.ResponsesCompactRequest` and normalizes with `translate.Compact(...)`.

Unlike `/v1/responses`, this endpoint does not use the SSE streaming path. It sends a dedicated JSON request to `/codex/responses/compact` and always returns non-streaming JSON.

### `/v1/images/generations` and `/v1/images/edits`

Both endpoints use Codex image generation inside an enclosing Responses request. The public image model defaults to `gpt-image-2`; `gpt-image-1.5` is also accepted. The image model is assigned to the tool, while normal account resolution selects a route-valid text model for the enclosing request.

The adapter forwards these tool options when supplied:

- `size`
- `quality`
- `background`
- `output_format`
- `moderation`
- `output_compression`
- `partial_images`
- `input_fidelity` on edits

JSON edits accept `images` entries containing `image_url` or `file_id`, plus an optional `mask`. Multipart edits accept one or more `image` or `image[]` files and an optional `mask` file; uploads are encoded as data URLs before being sent upstream.

Non-streaming responses contain `created`, `data`, upstream image metadata when present, and image-generation usage when Codex reports it. The default data field is `b64_json`. `response_format: "url"` wraps the returned bytes in a data URL rather than creating a hosted URL.

With `stream: true`, generation emits `image_generation.partial_image` and `image_generation.completed`; edits use the equivalent `image_edit.*` event names. The stream does not append a `[DONE]` sentinel.

Codex currently returns one image for each image tool call. The adapter does not fan out `n` into multiple upstream requests.

Image options are passed through, not simulated locally. In live testing, the current `gpt-image-2-codex` backend rejected `input_fidelity` and did not always honor the requested size or partial-image count; the proxy returns the upstream error or actual output metadata/events.

## Model Resolution

Model normalization does not rewrite user-supplied model IDs.

- If a model is explicitly supplied, it must exist in the current model catalog.
- If no model is supplied, normalization leaves it empty.
- Later, during account acquisition, the proxy chooses a route-valid default model for the selected account.

Reasoning effort on Chat Completions is converted to:

- `reasoning.effort = <value>`
- `reasoning.summary = "auto"`

Reasoning on Responses uses the request's explicit reasoning object. If `effort` is set and `summary` is empty, `summary` is forced to `"auto"`.

Whenever reasoning is enabled, the proxy also sends:

- `include = ["reasoning.encrypted_content"]`

## Request Translation Rules

### Instructions

The proxy collapses instruction-like content into one Codex `instructions` string.

- Chat Completions `system` and `developer` messages are flattened as text and joined with `\n\n`.
- Responses `instructions` is included first if present.
- Responses input items with `role = system` or `role = developer` and no explicit `type` are also lifted into `instructions`.
- If no instruction text is present, the proxy sends `You are a helpful assistant.`

Only text-like content is allowed in lifted instruction roles. Image content is rejected with `400`.

### Message and Input Content

OpenAI content parts are normalized into Codex content parts as follows:

- `text`, `input_text`, empty type -> `input_text`
- `output_text` -> `output_text`
- `reasoning_text` -> `reasoning_text`
- `image_url`, `input_image` -> `input_image`
- `file`, `input_file` -> `input_file`

`input_file` must include at least one of:

- `file_data`
- `file_url`
- `file_id`

Chat-style `file` parts may put those fields under a nested `file` object. Audio input is rejected locally because the current Codex upstream rejects both `input_audio` and audio MIME types supplied as files.

Unsupported content parts return `400` with an `unsupported_content_part` error message.

### Chat Completions Message Mapping

Chat Completions messages are converted like this:

- `user` and `assistant` messages without tool calls become Codex `InputItem{Role, Content}`.
- `assistant` or `user` messages with `tool_calls` first emit the role/content item if content exists, then emit one input item per tool call.
- Legacy assistant `function_call` becomes `InputItem{Type: "function_call", Name, Arguments}`.
- `tool` messages become tool output items. Text-only output remains a string; output containing images or files is preserved as structured content.

Tool outputs are typed using the earlier tool-call type seen in the same request:

- function tool -> `function_call_output`
- custom tool -> `custom_tool_call_output`

If the original call type is not known, tool output defaults to `function_call_output`.

If a Chat Completions request has no input messages after normalization, the proxy inserts one empty user text item.

### Responses Input Mapping

Responses input is converted like this:

- String input becomes a single user `input_text` item.
- Generic role/content items become `InputItem{Role, Content}`.
- Missing role on a generic item defaults to `user`.
- `function_call` maps to Codex `function_call`.
- `custom_tool_call` maps to Codex `custom_tool_call`.
- `function_call_output` and `custom_tool_call_output` preserve either `output` text or structured `output` content.
- `reasoning` items preserve:
  - `id`
  - `status`
  - `content`
  - `summary`
  - `encrypted_content`
- `compaction` items preserve:
  - `id`
  - `encrypted_content`
- Generic role/content items preserve `phase` when present so assistant output messages can be replayed through compaction.

### Tool Definitions

Tool definitions are normalized into Codex tools like this:

- `function` tools are rewritten into top-level `{type, name, description, parameters, strict}` objects even if they arrived in nested Chat Completions form.
- Function tool schemas are run through `NormalizeSchema`, which ensures `type = object` schemas have a `properties` map.
- `web_search_preview` is rewritten to `web_search`.
- `web_search` stays `web_search`.
- Custom tools and unknown tool types are passed through as-is.
- Tool names longer than 64 characters are shortened deterministically before the upstream request and restored in Chat Completions and Responses output. Colliding shortened names receive numeric suffixes.

Legacy Chat Completions `functions` are converted into modern `tools` only when `tools` is empty.

### Tool Choice

Tool choice is normalized like this:

- JSON string `"auto"` or `"none"` passes through as a JSON string.
- `{type: "function", name: ...}` becomes `{"type":"function","name":"..."}`.
- `{type: "function", function: {name: ...}}` is collapsed to the same form.
- `{type: "web_search"}` and `{type: "web_search_preview"}` become `{"type":"web_search"}`.
- Unrecognized objects pass through unchanged.

Legacy Chat Completions `function_call` is converted to:

- `"auto"` or `"none"` as a JSON string
- `{"type":"function","name":"..."}` when a specific name is provided

Modern `tools` and `tool_choice` take precedence over legacy `functions` and `function_call`.

### Structured Output Translation

Chat Completions `response_format` and Responses `text.format` are normalized into Codex `text.format`.

Supported modes:

- text: omitted upstream
- `json_object`
- `json_schema`

For `json_schema`, the proxy performs two schema adjustments before sending it upstream:

1. It injects `additionalProperties: false` on object schemas when missing.
2. If the schema uses tuple validation via `prefixItems`, it converts those tuple nodes into object-shaped schemas keyed by `"0"`, `"1"`, and so on.

When tuple conversion happens, the original schema is retained in `NormalizedRequest.TupleSchema` so response payloads can be converted back to array form before returning to the client.

## Fields Accepted but Ignored

The proxy accepts several OpenAI fields for compatibility but does not apply them upstream.

Chat Completions ignored fields:

- `n`
- `temperature`
- `top_p`
- `max_tokens`
- `presence_penalty`
- `frequency_penalty`
- `stop`
- `user`
- `parallel_tool_calls`
- `stream_options`

Responses ignored fields:

- `temperature`
- `top_p`
- `max_output_tokens`
- `parallel_tool_calls`
- `store`
- `background` on `POST /v1/responses`; the WebSocket endpoint rejects `background: true`
- `user`
- `metadata`
- `stream_options`

`service_tier` is retained on the canonical request and sent on the HTTP Codex path. OpenAI's `auto` value is normalized to Codex's `default` wire value, and the compatibility alias `fast` is normalized to `priority`. The current WebSocket payload builder does not include `service_tier`.

The Chat Completions, Responses, and Responses compact JSON handlers decode `Content-Encoding: zstd` request bodies. Identity or an omitted encoding is accepted; other request content encodings are rejected locally.

## Continuation Translation

Continuation resolution happens after request normalization.

### Explicit continuation

If `previous_response_id` is supplied:

- The proxy looks it up in its in-memory continuation store.
- If the ID is unknown or expired, it returns `400` with code `invalid_previous_response_id`.
- If the request did not specify a model, the model is filled from the saved continuation record.
- The saved conversation key becomes `prompt_cache_key`.
- The saved account ID becomes the preferred account.
- The saved upstream turn state becomes `x-codex-turn-state`.

### Explicit continuation for compaction

`/v1/responses/compact` handles `previous_response_id` differently because the private compact backend does not accept continuation state directly.

If `previous_response_id` is supplied on the compact endpoint:

- The proxy looks it up in the same in-memory continuation store.
- If the ID is unknown or expired, it returns `400` with code `invalid_previous_response_id`.
- If the compact request did not specify a model, the model is filled from the saved continuation record.
- Saved continuation history is converted back into Codex input items and prepended to the current compact request input.
- `previous_response_id` is not sent upstream to `/codex/responses/compact`.

The compact endpoint does not perform implicit continuation detection.

### Implicit continuation

If there is no explicit `previous_response_id`, the proxy may opportunistically convert a replay-style request into a continuation request.

That only happens when:

- the normalized request has a model
- a stable conversation key can be derived
- a recent continuation exists for that conversation key
- the saved model matches
- the saved instructions match
- the new input contains prior assistant or tool history
- the prefix of the new input exactly matches the saved prior input history
- any replayed tool outputs in the trimmed suffix reference known prior `call_id` values

If all checks pass:

- the already-known prefix is removed from `input`
- `previous_response_id` is filled with the saved response ID
- the preferred account and turn state are restored
- the model is pinned to the saved model

If the later WebSocket continuation attempt fails for an implicit resume, the proxy falls back to the original full request over HTTP.

### Prompt cache key derivation

When possible, the proxy derives a stable `prompt_cache_key` from:

- model
- normalized instructions
- the conversation prefix of the normalized input

The conversation prefix stops at the last assistant or tool-call item. This is used for both explicit and implicit continuation flows.

## Upstream Transport Translation

Chat Completions, Responses, and Images requests consume an upstream stream even when the public request is non-streaming. Responses compact is the exception: it uses a dedicated JSON request and response.

### HTTP path

For requests without continuation state or hosted web search, the proxy sends `codex.StreamRequestPayload(req)` to `POST /codex/responses`.

That forced HTTP payload does all of the following:

- `stream = true`
- `store = false`
- `previous_response_id = ""`

So even if the public request was non-streaming, the proxy still uses the streaming Codex endpoint and reconstructs the final JSON object locally.

### WebSocket path

The proxy uses `WSS /codex/responses` for explicit or implicit continuations, requests containing the hosted `web_search` tool, and turns received through the public Responses WebSocket. It sends:

- `type = "response.create"`
- `model`
- `input`
- `instructions`
- optional `tools`
- optional `tool_choice`
- optional `text`
- optional `reasoning`
- optional `previous_response_id`
- optional `prompt_cache_key`
- optional `include`
- optional `generate` for public WebSocket turns

The WebSocket payload omits `stream`, `store`, and `service_tier`. It preserves `previous_response_id` because Codex continuation happens there. A persistent public WebSocket session can send later `response.create` payloads over the same upstream connection when the account does not change.

## Upstream Events the Proxy Consumes

The proxy uses a single accumulator to interpret upstream events.

It tracks:

- response ID
- model
- final status
- text deltas and final text
- reasoning summary deltas
- tool calls
- output item ordering
- usage
- final raw response payload

Internal quota events are consumed but not forwarded:

- `codex.rate_limits` updates cached account quota state

## Non-Streaming Response Translation

For `stream = false`, the proxy still reads the full upstream stream and waits for `response.completed`.

If the stream ends before a completed response is seen, it returns an upstream error.

### Chat Completions object

The rebuilt Chat Completions response has:

- `id`
- `object = "chat.completion"`
- `created`, copied from upstream `created_at` when available or synthesized locally
- `model`
- `choices[0].index = 0`
- `choices[0].message.role = "assistant"`
- `choices[0].message.content = accumulated text`
- optional `choices[0].message.reasoning_content`
- optional `choices[0].message.tool_calls`
- `choices[0].finish_reason = "tool_calls"` when tool calls exist, else `"stop"`
- `usage` rewritten into Chat Completions token field names

Tool calls are returned in native shape:

- function tools -> `{type:"function", function:{name, arguments}}`
- custom tools -> `{type:"custom", custom:{name, input}}`

### Responses object

The rebuilt Responses response has:

- `id`
- `object = "response"`
- `model`
- `status`
- `output`
- `output_text`
- `usage`

The proxy starts from the native final upstream `response` object, preserving other top-level and usage fields it does not explicitly rebuild. It then overwrites or fills the compatibility fields listed above.

`output` is reconstructed from both:

- explicit upstream output items
- tool call state accumulated from tool-related events

If the final output has no items at all, the proxy synthesizes one completed assistant message item containing `output_text`.

### Usage mapping

Codex usage is translated like this:

- Chat Completions:
  - `input_tokens` -> `prompt_tokens`
  - `output_tokens` -> `completion_tokens`
- Responses:
  - `input_tokens` -> `input_tokens`
  - `output_tokens` -> `output_tokens`
- Both:
  - `total_tokens = input + output`
  - cached token detail becomes `cached_tokens`
  - reasoning token detail becomes `reasoning_tokens`

### Tuple-schema reconversion

If a tuple schema was rewritten on the way upstream:

- non-streaming Chat Completions patches `message.content`
- non-streaming Responses patches both `output_text` and the structured `output`

The reconversion rewrites object-shaped `"0"`, `"1"` tuple placeholders back into arrays using the original schema captured at request time.

### Compact response shaping

The private compact backend may return only partial JSON such as:

- `output`

The proxy reshapes that into an OpenAI-style compaction object with:

- `id`
- `object = "response.compaction"`
- `created_at`
- `output`
- `usage` when present upstream

If the upstream body omits `id` or `created_at`, the proxy synthesizes them locally. `usage` is omitted when it is not present upstream.

If tuple-schema reconversion is active, the proxy applies the same output-message reconversion used by `/v1/responses` before returning the compacted object.

## Streaming Response Translation

### Chat Completions streaming

The proxy emits Chat Completions SSE, not raw Codex SSE.

It always starts by sending an initial assistant-role chunk:

- `delta = {"role":"assistant"}`

Every chunk includes one stable `created` timestamp for the stream.

Then it maps upstream events like this:

- `response.output_text.delta` -> `delta.content`
- `response.reasoning_summary_text.delta` -> `delta.reasoning_content`

Tool-call streaming has a compatibility shim:

- Upstream function tools and custom tools are both emitted as Chat Completions `tool_calls` function-shaped deltas.
- The initial delta for a tool call includes:
  - `index`
  - `id`
  - `type = "function"`
  - `function.name`
  - empty `function.arguments`
- Subsequent deltas append argument text.
- For custom tools, the streamed argument text is actually the Codex custom tool `input`.

This is intentionally asymmetric:

- streamed Chat Completions custom tools are exposed as function-shaped deltas for client compatibility
- non-streaming Chat Completions still preserve native custom tool shape
- replayed function-shaped custom tool calls are mapped back upstream as custom tools when the tool name matches a declared custom tool

At completion:

- if tuple-schema reconversion is active, the buffered JSON text is converted back and emitted as one final content delta
- a final chunk is sent with finish reason and usage
- then `data: [DONE]`

### Responses streaming

The proxy emits Responses-style SSE events and preserves most upstream event types.

For ordinary non-tool events:

- the SSE event name is the upstream `type`
- the payload is the upstream raw event body

There are three important modifications.

First, tool call events can be synthesized or normalized:

- `response.function_call_arguments.delta`
- `response.function_call_arguments.done`
- `response.custom_tool_call_input.delta`
- `response.custom_tool_call_input.done`
- `response.output_item.added`
- `response.output_item.done`

The accumulator may emit missing `output_item.added` and `output_item.done` events so the downstream Responses stream contains a coherent tool-call output sequence even when Codex provides fragmented data.

Second, on `response.completed`, the proxy rewrites the nested `response` object so it has:

- rebuilt `output`
- filled `output_text` if missing
- filled `status = completed` if missing
- filled `id` if missing
- filled `model` if missing
- rebuilt `usage`

Third, if tuple-schema reconversion is active:

- buffered `response.output_text.delta` text is reconverted at completion
- one synthetic `response.output_text.delta` SSE is emitted with the patched JSON text
- the final `response.completed` payload is also patched

At the end of the Responses stream, the proxy emits:

- `event: done`
- `data: [DONE]`

The public Responses WebSocket applies the same tool-event normalization, completed-response rebuilding, and tuple reconversion, but writes plain JSON WebSocket messages. It does not append the SSE terminal event or `[DONE]` sentinel.

## Error Translation

Request-shape and normalization failures are returned as OpenAI-style JSON errors.

Model lookup failures return:

- HTTP `404`
- code `model_not_found`

Unknown or expired continuation IDs on the JSON HTTP endpoints return:

- HTTP `400`
- code `invalid_previous_response_id`

On the public Responses WebSocket, the equivalent error message has status `400` and code `previous_response_not_found`.

Upstream failures are classified into OpenAI-style proxy errors:

- upstream `401` or auth-like error code -> `401`, `upstream_unauthorized`
- upstream `403` -> `403`, `upstream_error`; generic access denials temporarily cool down the account
- upstream `402` or quota-like error code -> `402`, `quota_exhausted`
- upstream `429` or rate-limit-like error code -> `429`, `rate_limited`
- other upstream failures -> clamped upstream status, `upstream_error`

During streaming, classified errors are sent as SSE error payloads instead of raw upstream error objects.

For requests without explicit continuation state, retryable upstream failures (`401`, `402`, `403`, `408`, `429`, and transient `5xx` statuses) fail over to another eligible account before client-visible output begins. Each account is attempted at most once. Non-streaming responses remain retryable until the full upstream response has been buffered; streaming responses stop being retryable after the first public event is ready for delivery.

## State Recorded From Responses

After a successful completed upstream response, the proxy stores a continuation record containing:

- response ID
- selected account ID
- turn state from `x-codex-turn-state`
- derived conversation key
- normalized instructions
- resolved model
- full replayable input history
- tool call IDs seen in the response

That saved state is what powers both explicit and implicit continuation translation on later requests.

## Practical Summary

The proxy is not a thin field rename layer. It does all of the following:

- normalizes two public OpenAI request formats into one private Codex request format
- collapses instructions and content into the shapes Codex accepts
- rewrites schemas for Codex compatibility and converts tuple outputs back afterward
- converts public replay-style follow-up turns into true Codex continuations when safe
- uses HTTP SSE for ordinary requests and WebSocket for continuations, hosted web search, and the public Responses WebSocket
- reconstructs OpenAI Chat Completions and Responses outputs from the Codex stream
- hides internal quota events and translates upstream failures into OpenAI-style errors

If this behavior changes in code, this document should be updated to stay authoritative.
