package server

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/openai"
	"chatgpt-codex-proxy/internal/translate"
)

var errUnsupportedMultiplePrompts = errors.New("multiple prompts are not supported")

type completionsRequest struct {
	Model  string          `json:"model"`
	Prompt json.RawMessage `json:"prompt"`
	Stream bool            `json:"stream"`
}

func (a *App) handleCompletions(c *gin.Context) {
	a.handlePublicRequest(
		c,
		"completions",
		func(body []byte) (translate.NormalizedRequest, error) {
			return normalizeCompletionsBody(body, a.modelCatalog())
		},
		a.streamCompletion,
		(*translate.Accumulator).CompletionObject,
		func(map[string]any, map[string]any) error { return nil },
	)
}

func normalizeCompletionsBody(body []byte, catalog *models.Catalog) (translate.NormalizedRequest, error) {
	var req completionsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return translate.NormalizedRequest{}, err
	}

	var prompt string
	if err := json.Unmarshal(req.Prompt, &prompt); err != nil {
		var prompts []string
		if arrayErr := json.Unmarshal(req.Prompt, &prompts); arrayErr != nil {
			return translate.NormalizedRequest{}, errors.New("prompt must be a string or an array containing one string")
		}
		if len(prompts) != 1 {
			return translate.NormalizedRequest{}, errUnsupportedMultiplePrompts
		}
		prompt = prompts[0]
	}

	return translate.ChatCompletions(openai.ChatCompletionsRequest{
		Model:  req.Model,
		Stream: req.Stream,
		Messages: []openai.ChatMessage{{
			Role:    "user",
			Content: openai.MessageContent{{Type: "text", Text: prompt}},
		}},
	}, catalog)
}

func (a *App) streamCompletion(c *gin.Context, account accounts.Record, normalized translate.NormalizedRequest, stream eventStream) {
	prepareStreamResponse(c)
	accumulator := translate.NewAccumulator(normalized)
	createdAt := time.Now().UTC().Unix()

	for {
		event, upstreamErr, err := a.nextStreamEvent(c.Request.Context(), account, accumulator, stream)
		if err != nil {
			if err == io.EOF && accumulator.IsCompleted() {
				break
			}
			if err == io.EOF {
				err = errIncompleteResponse
			}
			a.respondStreamError(c, "completions", account.ID, accumulator.ResponseID, "", err, upstreamErr)
			return
		}
		if event.Type == "response.output_text.delta" {
			if delta := jsonutil.StringValue(event.Raw["delta"]); delta != "" {
				writeSSE(c.Writer, "", translate.MustJSON(completionChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), delta, "", createdAt)))
				c.Writer.Flush()
			}
		}
		if event.Type == "response.completed" {
			break
		}
	}

	a.finalizeSuccessfulStream(account.ID, accumulator, stream)
	final := completionChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), "", "stop", createdAt)
	if usage := accumulator.ChatUsageObject(); usage != nil {
		final["usage"] = usage
	}
	writeSSE(c.Writer, "", translate.MustJSON(final))
	_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}

func completionChunk(responseID, model, text, finishReason string, createdAt int64) map[string]any {
	choice := map[string]any{"index": 0, "text": text}
	if strings.TrimSpace(finishReason) != "" {
		choice["finish_reason"] = finishReason
	}
	return map[string]any{
		"id":      jsonutil.FirstNonEmpty(responseID, "cmpl_proxy"),
		"object":  "text_completion",
		"created": createdAt,
		"model":   model,
		"choices": []map[string]any{choice},
	}
}
