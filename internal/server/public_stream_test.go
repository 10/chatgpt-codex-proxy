package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/turn"
)

func TestChatImageStreamerMapsAndDeduplicatesImages(t *testing.T) {
	t.Parallel()

	streamer := newChatImageStreamer()
	partial := &codex.StreamEvent{Type: "response.image_generation_call.partial_image", Raw: map[string]any{
		"item_id": "ig_1", "output_format": "png", "partial_image_b64": "aGVsbG8=",
	}}
	images := streamer.imagesForEvent(partial)
	if len(images) != 1 {
		t.Fatalf("partial images = %#v, want one image", images)
	}
	imageURL, _ := images[0]["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("partial image URL = %#v", imageURL["url"])
	}

	done := &codex.StreamEvent{Type: "response.output_item.done", Raw: map[string]any{"item": map[string]any{
		"id": "ig_1", "type": "image_generation_call", "output_format": "png", "result": "aGVsbG8=",
	}}}
	if duplicate := streamer.imagesForEvent(done); len(duplicate) != 0 {
		t.Fatalf("duplicate images = %#v, want none", duplicate)
	}

	done.Raw["item"].(map[string]any)["result"] = "d29ybGQ="
	images = streamer.imagesForEvent(done)
	if len(images) != 1 || images[0]["index"] != 0 {
		t.Fatalf("updated images = %#v, want stable index 0", images)
	}
}

func TestResponsesStreamErrorUsesTopLevelResponsesShape(t *testing.T) {
	t.Parallel()

	var output strings.Builder
	writeResponsesStreamError(&output, http.StatusTooManyRequests, "slow down")
	events := parseSSEEvents(t, output.String())
	if len(events) != 1 || events[0].Event != "error" {
		t.Fatalf("events = %#v, want one error event", events)
	}
	if events[0].Data["type"] != "error" || events[0].Data["code"] != "rate_limit_exceeded" {
		t.Fatalf("error payload = %#v", events[0].Data)
	}
	if _, nested := events[0].Data["error"]; nested {
		t.Fatalf("error payload = %#v, want top-level fields", events[0].Data)
	}
}

func TestStreamResponsesPassesThroughIncompleteTerminalEvent(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	app := newFailoverTestApp(t)
	record := mustGetAccount(t, app.accounts, "acct-a")
	stream := &fakeEventStream{events: []*codex.StreamEvent{
		{
			Type: "response.output_text.delta",
			Raw:  map[string]any{"response_id": "resp_responses_limit", "delta": "partial"},
		},
		{
			Type: "response.incomplete",
			Raw: map[string]any{"response": map[string]any{
				"id":                 "resp_responses_limit",
				"model":              "gpt-5.6-terra",
				"status":             "incomplete",
				"incomplete_details": map[string]any{"reason": "max_output_tokens"},
				"output_text":        "partial",
			}},
		},
	}}

	app.streamResponses(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{Model: "gpt-5.6-terra", Stream: true},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events, "response.output_text.delta", "response.incomplete")
	response := nestedMapFromAny(events[1].Data["response"])
	if response["status"] != "incomplete" || nestedMapFromAny(response["incomplete_details"])["reason"] != "max_output_tokens" {
		t.Fatalf("response = %#v", response)
	}
}

func TestStreamChatCompletionEmitsReasoningContentAndStrictUsage(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	app := newFailoverTestApp(t)
	record := mustGetAccount(t, app.accounts, "acct-a")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{
			{
				Type: "response.reasoning_summary_text.delta",
				Raw: map[string]any{
					"response_id": "resp_chat_reasoning",
					"delta":       "Reasoning summary",
				},
			},
			{
				Type: "response.output_text.delta",
				Raw: map[string]any{
					"response_id": "resp_chat_reasoning",
					"delta":       "Final answer",
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{
					"response": map[string]any{
						"id":          "resp_chat_reasoning",
						"model":       "gpt-5.6-terra",
						"status":      "completed",
						"output_text": "Final answer",
						"usage": map[string]any{
							"input_tokens":  12,
							"output_tokens": 5,
							"input_tokens_details": map[string]any{
								"cached_tokens": 4,
							},
							"output_tokens_details": map[string]any{
								"reasoning_tokens": 2,
							},
						},
					},
				},
			},
		},
	}

	app.streamChatCompletion(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:     "gpt-5.6-terra",
			Stream:    true,
			Reasoning: &codex.Reasoning{Effort: "high"},
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 5", len(events))
	}
	created := events[0].Data["created"]
	if created == nil {
		t.Fatal("initial chat chunk missing created timestamp")
	}
	for index := 1; index < 4; index++ {
		if events[index].Data["created"] != created {
			t.Fatalf("event[%d].created = %#v, want stable %#v", index, events[index].Data["created"], created)
		}
	}
	reasoningDelta := events[1].Data
	choices := sliceOfMapsFromAny(reasoningDelta["choices"])
	delta := nestedMapFromAny(choices[0]["delta"])
	if delta["reasoning_content"] != "Reasoning summary" {
		t.Fatalf("reasoning_content = %#v, want reasoning summary", delta["reasoning_content"])
	}
	finalChunk := events[3].Data
	usage := nestedMapFromAny(finalChunk["usage"])
	if usage["prompt_tokens"] != float64(12) {
		t.Fatalf("prompt_tokens = %#v, want 12", usage["prompt_tokens"])
	}
	if usage["completion_tokens"] != float64(5) {
		t.Fatalf("completion_tokens = %#v, want 5", usage["completion_tokens"])
	}
	promptDetails := nestedMapFromAny(usage["prompt_tokens_details"])
	if promptDetails["cached_tokens"] != float64(4) {
		t.Fatalf("cached_tokens = %#v, want 4", promptDetails["cached_tokens"])
	}
	completionDetails := nestedMapFromAny(usage["completion_tokens_details"])
	if completionDetails["reasoning_tokens"] != float64(2) {
		t.Fatalf("reasoning_tokens = %#v, want 2", completionDetails["reasoning_tokens"])
	}
}

func TestStreamChatCompletionUsesToolNameFromOutputItemWhenArgumentEventsOmitIt(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	app := newFailoverTestApp(t)
	record := mustGetAccount(t, app.accounts, "acct-a")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{
			{
				Type: "response.function_call_arguments.delta",
				Raw: map[string]any{
					"response_id":  "resp_chat_tool_name",
					"item_id":      "fc_tool",
					"output_index": 0,
					"delta":        `{"path":"C:\\`,
				},
			},
			{
				Type: "response.output_item.added",
				Raw: map[string]any{
					"response_id":  "resp_chat_tool_name",
					"output_index": 0,
					"item": map[string]any{
						"id":        "fc_tool",
						"call_id":   "call_glob",
						"type":      "function_call",
						"name":      "Glob",
						"arguments": `{"path":"C:\\`,
						"status":    "in_progress",
					},
				},
			},
			{
				Type: "response.function_call_arguments.done",
				Raw: map[string]any{
					"response_id":  "resp_chat_tool_name",
					"item_id":      "fc_tool",
					"output_index": 0,
					"arguments":    `{"path":"C:\\Users\\Anson"}`,
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{
					"response": map[string]any{
						"id":     "resp_chat_tool_name",
						"model":  "gpt-5.6-terra",
						"status": "completed",
					},
				},
			},
		},
	}

	app.streamChatCompletion(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.6-terra",
			Stream: true,
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	if len(events) != 6 {
		t.Fatalf("event count = %d, want 6", len(events))
	}

	toolNameChunk := events[1].Data
	choices := sliceOfMapsFromAny(toolNameChunk["choices"])
	delta := nestedMapFromAny(choices[0]["delta"])
	toolCalls := sliceOfMapsFromAny(delta["tool_calls"])
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(toolCalls))
	}
	if toolCalls[0]["id"] != "call_glob" {
		t.Fatalf("tool_calls[0].id = %#v, want call_glob", toolCalls[0]["id"])
	}
	function := nestedMapFromAny(toolCalls[0]["function"])
	if function["name"] != "Glob" {
		t.Fatalf("function.name = %#v, want Glob", function["name"])
	}

	firstArgumentsChunk := events[2].Data
	choices = sliceOfMapsFromAny(firstArgumentsChunk["choices"])
	delta = nestedMapFromAny(choices[0]["delta"])
	toolCalls = sliceOfMapsFromAny(delta["tool_calls"])
	function = nestedMapFromAny(toolCalls[0]["function"])
	firstArguments, _ := function["arguments"].(string)

	secondArgumentsChunk := events[3].Data
	choices = sliceOfMapsFromAny(secondArgumentsChunk["choices"])
	delta = nestedMapFromAny(choices[0]["delta"])
	toolCalls = sliceOfMapsFromAny(delta["tool_calls"])
	function = nestedMapFromAny(toolCalls[0]["function"])
	secondArguments, _ := function["arguments"].(string)

	if firstArguments+secondArguments != `{"path":"C:\\Users\\Anson"}` {
		t.Fatalf("combined function.arguments = %q, want complete arguments", firstArguments+secondArguments)
	}
}

func TestStreamChatCompletionSupportsCustomToolCalls(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	app := newFailoverTestApp(t)
	record := mustGetAccount(t, app.accounts, "acct-a")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{
			{
				Type: "response.custom_tool_call_input.delta",
				Raw: map[string]any{
					"response_id":  "resp_chat_custom_tool",
					"item_id":      "ctc_tool",
					"output_index": 0,
					"delta":        "*** Begin Patch\\n",
				},
			},
			{
				Type: "response.output_item.added",
				Raw: map[string]any{
					"response_id":  "resp_chat_custom_tool",
					"output_index": 0,
					"item": map[string]any{
						"id":      "ctc_tool",
						"call_id": "call_patch",
						"type":    "custom_tool_call",
						"name":    "ApplyPatch",
						"input":   "*** Begin Patch\\n",
						"status":  "in_progress",
					},
				},
			},
			{
				Type: "response.custom_tool_call_input.done",
				Raw: map[string]any{
					"response_id":  "resp_chat_custom_tool",
					"item_id":      "ctc_tool",
					"output_index": 0,
					"input":        "*** Begin Patch\\n*** End Patch\\n",
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{
					"response": map[string]any{
						"id":     "resp_chat_custom_tool",
						"model":  "gpt-5.6-terra",
						"status": "completed",
					},
				},
			},
		},
	}

	app.streamChatCompletion(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.6-terra",
			Stream: true,
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	if len(events) != 6 {
		t.Fatalf("event count = %d, want 6", len(events))
	}

	toolChunk := events[1].Data
	choices := sliceOfMapsFromAny(toolChunk["choices"])
	delta := nestedMapFromAny(choices[0]["delta"])
	toolCalls := sliceOfMapsFromAny(delta["tool_calls"])
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(toolCalls))
	}
	if toolCalls[0]["id"] != "call_patch" {
		t.Fatalf("tool_calls[0].id = %#v, want call_patch", toolCalls[0]["id"])
	}
	if toolCalls[0]["type"] != "function" {
		t.Fatalf("tool_calls[0].type = %#v, want function compatibility shim", toolCalls[0]["type"])
	}
	function := nestedMapFromAny(toolCalls[0]["function"])
	if function["name"] != "ApplyPatch" {
		t.Fatalf("function.name = %#v, want ApplyPatch", function["name"])
	}
	if function["arguments"] != "" {
		t.Fatalf("function.arguments = %q, want empty initializer", function["arguments"])
	}

	inputChunk := events[2].Data
	choices = sliceOfMapsFromAny(inputChunk["choices"])
	delta = nestedMapFromAny(choices[0]["delta"])
	toolCalls = sliceOfMapsFromAny(delta["tool_calls"])
	function = nestedMapFromAny(toolCalls[0]["function"])
	firstDelta, _ := function["arguments"].(string)

	inputDoneChunk := events[3].Data
	choices = sliceOfMapsFromAny(inputDoneChunk["choices"])
	delta = nestedMapFromAny(choices[0]["delta"])
	toolCalls = sliceOfMapsFromAny(delta["tool_calls"])
	function = nestedMapFromAny(toolCalls[0]["function"])
	secondDelta, _ := function["arguments"].(string)

	if firstDelta+secondDelta != "*** Begin Patch\\n*** End Patch\\n" {
		t.Fatalf("combined function.arguments deltas = %q, want complete streamed input", firstDelta+secondDelta)
	}

	finalChunk := events[4].Data
	choices = sliceOfMapsFromAny(finalChunk["choices"])
	if choices[0]["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %#v, want tool_calls", choices[0]["finish_reason"])
	}
}

func TestContinuationInputHistoryIncludesReasoningReplay(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{
		Request: codex.Request{
			Input: []codex.InputItem{{
				Role: "user",
				Content: []codex.ContentPart{{
					Type: "input_text",
					Text: "hello",
				}},
			}},
		},
	})
	accumulator.Apply(&codex.StreamEvent{
		Type: "response.completed",
		Raw: map[string]any{
			"response": map[string]any{
				"id":     "resp_continuation_reasoning",
				"model":  "gpt-5.6-terra",
				"status": "completed",
				"output": []any{
					map[string]any{
						"type":              "reasoning",
						"id":                "rs_1",
						"status":            "completed",
						"encrypted_content": "encrypted-reasoning",
						"summary": []any{
							map[string]any{
								"type": "summary_text",
								"text": "Reasoning summary",
							},
						},
					},
					map[string]any{
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": "assistant replay",
							},
						},
					},
				},
				"output_text": "assistant replay",
			},
		},
	})

	history := continuationInputHistory(accumulator)
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	if history[1].Type != "reasoning" {
		t.Fatalf("history[1].Type = %q, want reasoning", history[1].Type)
	}
	if history[1].EncryptedContent != "encrypted-reasoning" {
		t.Fatalf("history[1].EncryptedContent = %q, want encrypted-reasoning", history[1].EncryptedContent)
	}
	if len(history[1].Summary) != 1 || history[1].Summary[0].Text != "Reasoning summary" {
		t.Fatalf("history[1].Summary = %#v", history[1].Summary)
	}

	replayed := cloneContinuationInputItems(history)
	if len(replayed) != 3 {
		t.Fatalf("replayed len = %d, want 3", len(replayed))
	}
	if replayed[1].Type != "reasoning" {
		t.Fatalf("replayed[1].Type = %q, want reasoning", replayed[1].Type)
	}
	if replayed[1].EncryptedContent != "encrypted-reasoning" {
		t.Fatalf("replayed[1].EncryptedContent = %q, want encrypted-reasoning", replayed[1].EncryptedContent)
	}
	replayed[1].Summary[0].Text = "mutated"
	if history[1].Summary[0].Text != "Reasoning summary" {
		t.Fatalf("history[1].Summary = %#v, want independent clone", history[1].Summary)
	}
}

func TestContinuationInputHistoryKeepsShortToolNameForUpstreamReplay(t *testing.T) {
	t.Parallel()

	shortName := "mcp__read_project_file"
	originalName := "mcp__filesystem__read_project_file_with_a_name_longer_than_sixty_four_characters"
	accumulator := turn.NewAccumulator(turn.NormalizedRequest{
		Request: codex.Request{
			Input: []codex.InputItem{userText("inspect the project")},
		},
		ToolNameAliases: map[string]string{shortName: originalName},
	})
	accumulator.Apply(&codex.StreamEvent{
		Type: "response.completed",
		Raw: map[string]any{
			"response": map[string]any{
				"id": "resp_long_tool",
				"output": []any{map[string]any{
					"type": "function_call", "id": "fc_1", "call_id": "call_1",
					"name": shortName, "arguments": `{}`, "status": "completed",
				}},
			},
		},
	})

	if accumulator.ToolCalls[0].Name != originalName {
		t.Fatalf("client response name = %q, want original name", accumulator.ToolCalls[0].Name)
	}
	history := continuationInputHistory(accumulator)
	if len(history) != 2 || history[1].Name != shortName {
		t.Fatalf("continuation history = %#v, want shortened upstream name", history)
	}
}

func TestClassifyUpstreamErrorCoolsDownGeneric403ButNotCloudflare403(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	app := newFailoverTestApp(t)

	status, code, _ := app.classifyUpstreamError("acct-a", &codex.UpstreamError{
		Op:         "codex response",
		StatusCode: http.StatusForbidden,
		Body:       `{"error":"access denied"}`,
	})
	if status != http.StatusForbidden || code != "upstream_error" {
		t.Fatalf("generic 403 = (%d, %q), want (403, upstream_error)", status, code)
	}

	status, code, _ = app.classifyUpstreamError("acct-b", &codex.UpstreamError{
		Op:         "codex response",
		StatusCode: http.StatusForbidden,
		Body:       "<!DOCTYPE html><html><body>cf_chl blocked</body></html>",
	})
	if status != http.StatusForbidden || code != "upstream_error" {
		t.Fatalf("cloudflare 403 = (%d, %q), want (403, upstream_error)", status, code)
	}

	generic := mustGetAccount(t, app.accounts, "acct-a")
	if generic.Status != accounts.StatusActive {
		t.Fatalf("generic status = %q, want active", generic.Status)
	}
	if generic.CooldownUntil == nil || !generic.CooldownUntil.After(now) {
		t.Fatalf("generic cooldown = %v, want future cooldown", generic.CooldownUntil)
	}
	cloudflare := mustGetAccount(t, app.accounts, "acct-b")
	if cloudflare.Status != accounts.StatusActive {
		t.Fatalf("cloudflare status = %q, want active", cloudflare.Status)
	}
}
