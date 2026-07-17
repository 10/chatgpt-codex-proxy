package server

import (
	"io"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

func (a *App) streamResponses(c *gin.Context, account accounts.Record, normalized turn.NormalizedRequest, stream eventStream) {
	prepareStreamResponse(c)

	accumulator := turn.NewAccumulator(normalized)
	var tupleTextBuffer strings.Builder
	for {
		event, upstreamErr, err := a.nextStreamEvent(c.Request.Context(), account, accumulator, stream)
		if err != nil {
			if err == io.EOF {
				if !accumulator.IsTerminal() {
					a.respondStreamError(c, "responses", account.ID, accumulator.ResponseID, "error", errIncompleteResponse, false)
					return
				}
				break
			}
			a.respondStreamError(c, "responses", account.ID, accumulator.ResponseID, "error", err, upstreamErr)
			return
		}
		for _, outgoing := range a.responsesStreamEvents(c, accumulator, normalized, &tupleTextBuffer, event) {
			writeSSE(c.Writer, outgoing.Type, turn.ResponseEventJSON(outgoing.Type, accumulator.ResponseID, outgoing.Payload))
		}
		c.Writer.Flush()
		if event.IsTerminalResponse() {
			break
		}
	}

	a.finalizeSuccessfulStream(account.ID, accumulator, stream)
	c.Writer.Flush()
}

func responseStreamPayload(event *codex.StreamEvent, accumulator *turn.Accumulator) map[string]any {
	if event == nil || event.Raw == nil {
		return nil
	}
	if !event.IsTerminalResponse() {
		return event.Raw
	}

	payload := jsonutil.CloneMap(event.Raw)
	response, _ := payload["response"].(map[string]any)
	if response == nil {
		response = map[string]any{}
	}

	text := accumulator.Text()
	response["output"] = accumulator.ResponsesObject()["output"]
	if strings.TrimSpace(jsonutil.StringValue(response["output_text"])) == "" && strings.TrimSpace(text) != "" {
		response["output_text"] = text
	}
	if strings.TrimSpace(jsonutil.StringValue(response["status"])) == "" {
		response["status"] = strings.TrimPrefix(event.Type, "response.")
	}
	if accumulator.ResponseID != "" && strings.TrimSpace(jsonutil.StringValue(response["id"])) == "" {
		response["id"] = accumulator.ResponseID
	}
	if accumulator.Model != "" && strings.TrimSpace(jsonutil.StringValue(response["model"])) == "" {
		response["model"] = accumulator.Model
	}
	if rebuilt := accumulator.ResponsesUsageObject(); rebuilt != nil {
		response["usage"] = rebuilt
	}
	payload["response"] = response
	return payload
}
