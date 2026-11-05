package server

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/conversation"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

var (
	errContinuationAccountUnavailable = errors.New("continuation account unavailable")
	errInvalidPreviousResponseID      = errors.New("unknown or expired previous_response_id")
)

func (a *App) resolveSession(normalized turn.NormalizedRequest) (sessionResolution, error) {
	resolution := sessionResolution{
		Request:  normalized,
		Original: normalized,
	}
	if normalized.PreviousResponseID != "" {
		record, ok := a.continuations.Get(normalized.PreviousResponseID)
		if !ok {
			return sessionResolution{}, errInvalidPreviousResponseID
		}
		if !resolution.Request.ModelExplicit || strings.TrimSpace(resolution.Request.Model) == "" {
			resolution.Request.Model = record.Model
			resolution.Original.Model = record.Model
		}
		if key := strings.TrimSpace(record.ConversationKey); key != "" {
			resolution.ConversationKey = key
			resolution.Request.PromptCacheKey = key
			resolution.Original.PromptCacheKey = key
		} else if key := conversation.Derive(resolution.Request.Request); key != "" {
			resolution.ConversationKey = key
			resolution.Request.PromptCacheKey = key
			resolution.Original.PromptCacheKey = key
		}
		resolution.PreferredAccountID = record.AccountID
		resolution.TurnState = strings.TrimSpace(record.TurnState)
		resolution.Request.ToolNameAliases = turn.MergeToolNameAliases(resolution.Request.ToolNameAliases, record.ToolNameAliases)
		resolution.Original.ToolNameAliases = turn.MergeToolNameAliases(resolution.Original.ToolNameAliases, record.ToolNameAliases)
		if history := cloneContinuationInputItems(record.InputHistory); len(history) > 0 {
			resolution.Original.Input = append(history, normalized.Input...)
			resolution.Original.PreviousResponseID = ""
			resolution.ReplayAvailable = true
		}
		resolution.ExplicitPrevious = true
		return resolution, nil
	}

	if strings.TrimSpace(resolution.Request.Model) == "" {
		return resolution, nil
	}

	if key := strings.TrimSpace(normalized.PromptCacheKey); key != "" {
		resolution.ConversationKey = key
	} else if key := conversation.Derive(normalized.Request); key != "" {
		resolution.ConversationKey = key
		resolution.Request.PromptCacheKey = key
		resolution.Original.PromptCacheKey = key
	}

	if a.continuations == nil || !hasPriorAssistantOrToolHistory(normalized.Input) {
		return resolution, nil
	}
	records := a.continuations.ListByConversation(resolution.ConversationKey)
	records = append(records, a.continuations.ListAll()...)
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, ok := seen[record.ResponseID]; ok {
			continue
		}
		seen[record.ResponseID] = struct{}{}
		if !canImplicitlyResume(record, normalized) {
			continue
		}
		trimmed, ok := trimmedContinuationInput(normalized.Input, record)
		if !ok {
			continue
		}

		resolution.Request.Input = trimmed
		resolution.Request.PreviousResponseID = record.ResponseID
		resolution.Request.Model = record.Model
		resolution.Original.Model = record.Model
		resolution.PreferredAccountID = record.AccountID
		resolution.TurnState = strings.TrimSpace(record.TurnState)
		resolution.Request.ToolNameAliases = turn.MergeToolNameAliases(resolution.Request.ToolNameAliases, record.ToolNameAliases)
		resolution.Original.ToolNameAliases = turn.MergeToolNameAliases(resolution.Original.ToolNameAliases, record.ToolNameAliases)
		resolution.ImplicitResume = true
		if key := strings.TrimSpace(record.ConversationKey); key != "" {
			resolution.ConversationKey = key
			resolution.Request.PromptCacheKey = key
			resolution.Original.PromptCacheKey = key
		}
		return resolution, nil
	}
	return resolution, nil
}

func canImplicitlyResume(record conversation.ContinuationRecord, normalized turn.NormalizedRequest) bool {
	if strings.TrimSpace(record.ResponseID) == "" {
		return false
	}
	if strings.TrimSpace(record.Model) != strings.TrimSpace(normalized.Model) {
		return false
	}
	if strings.TrimSpace(record.Instructions) != strings.TrimSpace(normalized.Instructions) {
		return false
	}
	return hasPriorAssistantOrToolHistory(normalized.Input)
}

func hasPriorAssistantOrToolHistory(input []turn.InputItem) bool {
	for _, item := range input {
		if item.Role == "assistant" {
			return true
		}
		switch item.Type {
		case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
			return true
		}
	}
	return false
}

func trimmedContinuationInput(input []turn.InputItem, record conversation.ContinuationRecord) ([]turn.InputItem, bool) {
	if len(input) == 0 {
		return nil, false
	}

	history := cloneContinuationInputItems(record.InputHistory)
	if len(history) == 0 || len(input) <= len(history) {
		return nil, false
	}
	for idx := range history {
		if !reflect.DeepEqual(input[idx], history[idx]) {
			return nil, false
		}
	}

	allowedCallIDs := make(map[string]struct{}, len(record.FunctionCallIDs))
	for _, callID := range record.FunctionCallIDs {
		callID = strings.TrimSpace(callID)
		if callID != "" {
			allowedCallIDs[callID] = struct{}{}
		}
	}

	trimmed := append([]turn.InputItem(nil), input[len(history):]...)
	for _, item := range trimmed {
		switch item.Type {
		case "function_call_output", "custom_tool_call_output":
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				return nil, false
			}
			if _, ok := allowedCallIDs[callID]; !ok {
				return nil, false
			}
		}
	}
	return trimmed, true
}

func (a *App) writeRequestError(c *gin.Context, err error) bool {
	if !errors.Is(err, errInvalidPreviousResponseID) {
		return false
	}
	a.writeOpenAIError(c, http.StatusBadRequest, "invalid_previous_response_id", errInvalidPreviousResponseID.Error(), "invalid_request_error")
	return true
}

func functionCallIDs(accumulator *turn.Accumulator) []string {
	if accumulator == nil || len(accumulator.ToolCalls) == 0 {
		return nil
	}
	ids := make([]string, 0, len(accumulator.ToolCalls))
	seen := make(map[string]struct{}, len(accumulator.ToolCalls))
	for _, call := range accumulator.ToolCalls {
		callID := strings.TrimSpace(call.CallID)
		if callID == "" {
			continue
		}
		if _, ok := seen[callID]; ok {
			continue
		}
		seen[callID] = struct{}{}
		ids = append(ids, callID)
	}
	return ids
}

func resolutionConversationKey(normalized turn.NormalizedRequest) string {
	if key := strings.TrimSpace(normalized.PromptCacheKey); key != "" {
		return key
	}
	return conversation.Derive(normalized.Request)
}

type sessionResolution struct {
	Request            turn.NormalizedRequest
	Original           turn.NormalizedRequest
	PreferredAccountID string
	TurnState          string
	ConversationKey    string
	ExplicitPrevious   bool
	ImplicitResume     bool
	ReplayAvailable    bool
}

func (a *App) rememberContinuation(accountID string, accumulator *turn.Accumulator, turnState string) {
	if accumulator == nil || accumulator.ResponseID == "" {
		return
	}
	conversationKey := accumulator.Normalized.PromptCacheKey
	if strings.TrimSpace(conversationKey) == "" {
		conversationKey = resolutionConversationKey(accumulator.Normalized)
	}
	a.continuations.Put(conversation.ContinuationRecord{
		ResponseID:      accumulator.ResponseID,
		AccountID:       accountID,
		ConversationKey: conversationKey,
		TurnState:       strings.TrimSpace(turnState),
		Instructions:    strings.TrimSpace(accumulator.Normalized.Instructions),
		Model:           jsonutil.FirstNonEmpty(accumulator.Model, accumulator.Normalized.Model),
		InputHistory:    continuationInputHistory(accumulator),
		FunctionCallIDs: functionCallIDs(accumulator),
		ToolNameAliases: accumulator.Normalized.ToolNameAliases,
	})
}
