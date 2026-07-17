# Anthropic Messages compatibility

The proxy exposes an Anthropic-compatible API over the same locally authenticated Codex accounts used by the OpenAI routes.

## Endpoints

- `POST /v1/messages`
- `POST /v1/messages/count_tokens`
- `GET /v1/models`
- `GET /v1/models/:model_id`

Requests require the proxy key in `X-API-Key` or as a Bearer token, plus:

```http
Anthropic-Version: 2023-06-01
```

Model endpoints return the Anthropic envelope when that header is present. They otherwise retain their OpenAI-compatible response.

## Request mapping

| Anthropic input | Codex input |
| --- | --- |
| Top-level `system` text | `instructions` |
| Message-level `system` text | User `<system-reminder>` input |
| User text | `input_text` |
| Assistant text or prefill | `output_text` |
| Base64 or URL image | `input_image` |
| Assistant `tool_use` | `function_call` |
| User `tool_result` | `function_call_output` |
| Tool `input_schema` | Normalized function parameters |
| `tool_choice: auto` | `auto` |
| `tool_choice: any` | `required` |
| Named tool choice | Named function choice |
| `disable_parallel_tool_use` | `parallel_tool_calls` |
| Enabled/adaptive thinking | Codex reasoning summaries and encrypted reasoning state |
| JSON-schema output format | Codex structured output |
| Fast service tier | Codex priority tier |

Claude Code's attribution-only system block is filtered from model instructions. Its adaptive-thinking `display` and `context_management` fields are accepted as compatibility metadata; `output_config.effort` remains the field that selects Codex reasoning effort.

Tool names and tool-call IDs longer than the backend limit are shortened deterministically. Original tool names are restored in public responses.

Anthropic requests contain their full transcript. They deliberately bypass the proxy's implicit OpenAI continuation mechanism so transcript history is never combined with hidden server-side history.

## Streaming

Streaming responses use named Anthropic SSE events:

1. `message_start`
2. `content_block_start`
3. One or more `content_block_delta` events
4. `content_block_stop`
5. `message_delta`
6. `message_stop`

Text uses `text_delta`, thinking uses `thinking_delta` and `signature_delta`, and tool input uses `input_json_delta`. The proxy does not emit an OpenAI `[DONE]` sentinel.

If a request fails before streaming begins, the endpoint returns a normal JSON error and HTTP status. A failure after SSE headers have been committed is emitted as an `event: error` frame and is not followed by `message_stop`.

## Token counting

`/v1/messages/count_tokens` normalizes the request through the same mapper used by `/v1/messages`, then tokenizes the effective Codex instructions, transcript, tools, and output schema locally.

The result is a Codex-oriented estimate. It is not an Anthropic billing-token guarantee. Image references use a bounded per-image allowance instead of counting URL or base64 payload bytes as text because exact image costs require an upstream counting endpoint.

## Current compatibility limits

- Use actual Codex model IDs returned by `/v1/models`. The proxy does not create synthetic Claude model aliases.
- `max_tokens` is required by Messages and accepted, but positive values do not currently enforce exact output truncation. `max_tokens: 0` maps to the Codex WebSocket `generate: false` control.
- `temperature`, `top_p`, `top_k`, and `stop_sequences` are accepted but are not forwarded because the Codex backend does not expose equivalent request fields.
- `thinking.budget_tokens` enables thinking but does not map to an exact Codex token budget. The model catalog's default reasoning effort is used unless `output_config.effort` selects a supported effort.
- Hosted web-search tools are rejected because their server-tool state and citations cannot be round-tripped faithfully through the Anthropic Messages protocol.
- Prompt caching controls are accepted as content metadata. Reported cache-read usage comes from Codex cached-token counters when available.
- Anthropic client-side tools are supported. Anthropic server-tool families are rejected until their state can be round-tripped faithfully through Codex.
