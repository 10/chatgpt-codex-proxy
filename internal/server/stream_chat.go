package server

import (
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/schema"
	"chatgpt-codex-proxy/internal/turn"
)

func (a *App) streamChatCompletion(c *gin.Context, account accounts.Record, normalized turn.NormalizedRequest, stream eventStream) {
	prepareStreamResponse(c)

	accumulator := turn.NewAccumulator(normalized)
	createdAt := time.Now().UTC().Unix()
	toolCalls := &chatToolCallStreamer{
		indexByCallID: make(map[string]int),
		initialized:   make(map[string]bool),
		argumentsSent: make(map[string]int),
		createdAt:     createdAt,
	}
	images := newChatImageStreamer()
	var tupleTextBuffer strings.Builder
	textSent := false
	writeSSE(c.Writer, "", mustJSON(turn.ChatChunk("", normalized.Model, map[string]any{"role": "assistant"}, "", createdAt)))
	c.Writer.Flush()

	for {
		event, upstreamErr, err := a.nextStreamEvent(c.Request.Context(), account, accumulator, stream)
		if err != nil {
			if err == io.EOF {
				if !accumulator.IsTerminal() {
					a.respondStreamError(c, "chat_completions", account.ID, accumulator.ResponseID, "", errIncompleteResponse, false)
					return
				}
				break
			}
			a.respondStreamError(c, "chat_completions", account.ID, accumulator.ResponseID, "", err, upstreamErr)
			return
		}
		if state := accumulator.ToolCallStateForEvent(event); state != nil && (state.ToolType == "custom" || strings.HasPrefix(event.Type, "response.custom_tool_call_input.")) {
			a.logCustomToolTrace(c, "chat_completions", "upstream_event", event.Type, state)
		}
		if emitted := toolCalls.writeChunk(c.Writer, accumulator, normalized, event); emitted {
			if state := accumulator.ToolCallStateForEvent(event); state != nil && state.ToolType == "custom" {
				a.logCustomToolTrace(c, "chat_completions", "chat_chunk_emitted", event.Type, state)
			}
			c.Writer.Flush()
			continue
		}
		for _, image := range images.imagesForEvent(event) {
			writeSSE(c.Writer, "", mustJSON(turn.ChatChunk(
				accumulator.ResponseID,
				jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model),
				map[string]any{"role": "assistant", "images": []map[string]any{image}},
				"",
				createdAt,
			)))
			c.Writer.Flush()
		}
		switch event.Type {
		case "response.reasoning_summary_text.delta":
			if normalized.Reasoning != nil {
				delta := jsonutil.StringValue(event.Raw["delta"])
				if delta != "" {
					writeSSE(c.Writer, "", mustJSON(turn.ChatChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), map[string]any{"reasoning_content": delta}, "", createdAt)))
					c.Writer.Flush()
				}
			}
		case "response.output_text.delta":
			delta := jsonutil.StringValue(event.Raw["delta"])
			if delta == "" {
				continue
			}
			if normalized.TupleSchema != nil {
				tupleTextBuffer.WriteString(delta)
				continue
			}
			textSent = true
			writeSSE(c.Writer, "", mustJSON(turn.ChatChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), map[string]any{"content": delta}, "", createdAt)))
			c.Writer.Flush()
		case "response.output_text.done":
			if normalized.TupleSchema != nil {
				if text := jsonutil.StringValue(event.Raw["text"]); text != "" {
					tupleTextBuffer.Reset()
					tupleTextBuffer.WriteString(text)
				}
			}
		case "response.completed", "response.incomplete":
			if normalized.TupleSchema != nil && tupleTextBuffer.Len() == 0 {
				tupleTextBuffer.WriteString(accumulator.Text())
			}
			if normalized.TupleSchema != nil && strings.TrimSpace(tupleTextBuffer.String()) != "" {
				reconverted := tupleTextBuffer.String()
				if patched, err := schema.ReconvertJSONText(reconverted, normalized.TupleSchema); err != nil {
					a.logTupleReconversionWarning(c, "chat_completions", accumulator.ResponseID, err)
				} else {
					reconverted = patched
				}
				textSent = true
				writeSSE(c.Writer, "", mustJSON(turn.ChatChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), map[string]any{"content": reconverted}, "", createdAt)))
				c.Writer.Flush()
			}
		}
		if event.IsTerminalResponse() {
			break
		}
	}

	a.finalizeSuccessfulStream(account.ID, accumulator, stream)

	finalDelta := map[string]any{}
	if !textSent {
		if text := accumulator.Text(); text != "" {
			finalDelta["content"] = text
		}
	}
	finalUsage := accumulator.ChatUsageObject()
	finalChunk := turn.ChatChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), finalDelta, accumulator.ChatFinishReason(), createdAt)
	if nativeFinishReason := accumulator.NativeFinishReason(); nativeFinishReason != "" {
		choices := finalChunk["choices"].([]map[string]any)
		choices[0]["native_finish_reason"] = nativeFinishReason
	}
	if finalUsage != nil {
		finalChunk["usage"] = finalUsage
	}
	writeSSE(c.Writer, "", mustJSON(finalChunk))
	_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}
