package server

import (
	"slices"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/translate"
)

func continuationInputHistory(accumulator *translate.Accumulator) []accounts.ContinuationInputItem {
	history := make([]accounts.ContinuationInputItem, 0, len(accumulator.Normalized.Input))
	for _, item := range accumulator.Normalized.Input {
		history = append(history, continuationInputItemFromCodex(item))
	}
	history = append(history, continuationOutputHistory(accumulator)...)
	return history
}

func continuationOutputHistory(accumulator *translate.Accumulator) []accounts.ContinuationInputItem {
	if accumulator == nil {
		return nil
	}
	response := accumulator.ResponsesObject()
	output, ok := response["output"].([]map[string]any)
	if !ok || len(output) == 0 {
		return nil
	}
	history := make([]accounts.ContinuationInputItem, 0, len(output))
	for _, item := range output {
		converted, ok := continuationInputItemFromResponseOutput(item, accumulator.Normalized.ToolNameAliases)
		if ok {
			history = append(history, converted)
		}
	}
	return history
}

func continuationInputItemFromResponseOutput(item map[string]any, toolNameAliases map[string]string) (accounts.ContinuationInputItem, bool) {
	if len(item) == 0 {
		return accounts.ContinuationInputItem{}, false
	}
	out := accounts.ContinuationInputItem{
		Role:             jsonutil.StringValue(item["role"]),
		Type:             jsonutil.StringValue(item["type"]),
		Phase:            jsonutil.StringValue(item["phase"]),
		CallID:           jsonutil.StringValue(item["call_id"]),
		Name:             jsonutil.StringValue(item["name"]),
		Input:            jsonutil.StringValue(item["input"]),
		Arguments:        jsonutil.StringValue(item["arguments"]),
		OutputText:       jsonutil.StringValue(item["output"]),
		ID:               jsonutil.StringValue(item["id"]),
		Status:           jsonutil.StringValue(item["status"]),
		EncryptedContent: jsonutil.StringValue(item["encrypted_content"]),
	}
	out.Summary = continuationSummaryPartsFromMaps(jsonutil.SliceOfMaps(item["summary"]))
	out.Content = continuationContentPartsFromMaps(jsonutil.SliceOfMaps(item["content"]))
	out.OutputContent = continuationContentPartsFromMaps(jsonutil.SliceOfMaps(item["output"]))
	out.Name = translate.UpstreamToolName(out.Name, toolNameAliases)
	if out.Type == "message" {
		if out.Role == "" {
			out.Role = "assistant"
		}
		out.Type = ""
	}
	if out.Role == "" && out.Type == "" && len(out.Content) == 0 && len(out.OutputContent) == 0 && out.CallID == "" && out.ID == "" {
		return accounts.ContinuationInputItem{}, false
	}
	return out, true
}

func continuationInputItemsToCodex(items []accounts.ContinuationInputItem) []codex.InputItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]codex.InputItem, 0, len(items))
	for _, item := range items {
		out = append(out, continuationInputItemToCodex(item))
	}
	return out
}

func continuationInputItemFromCodex(item codex.InputItem) accounts.ContinuationInputItem {
	out := accounts.ContinuationInputItem{
		Role:             item.Role,
		Type:             item.Type,
		Phase:            item.Phase,
		CallID:           item.CallID,
		Name:             item.Name,
		Input:            item.Input,
		Arguments:        item.Arguments,
		OutputText:       item.OutputText,
		ID:               item.ID,
		Status:           item.Status,
		EncryptedContent: item.EncryptedContent,
	}
	out.Summary = slices.Clone(item.Summary)
	out.Content = slices.Clone(item.Content)
	out.OutputContent = slices.Clone(item.OutputContent)
	return out
}

func continuationInputItemToCodex(item accounts.ContinuationInputItem) codex.InputItem {
	out := codex.InputItem{
		Role:             item.Role,
		Type:             item.Type,
		Phase:            item.Phase,
		CallID:           item.CallID,
		Name:             item.Name,
		Input:            item.Input,
		Arguments:        item.Arguments,
		OutputText:       item.OutputText,
		ID:               item.ID,
		Status:           item.Status,
		EncryptedContent: item.EncryptedContent,
	}
	out.Summary = slices.Clone(item.Summary)
	out.Content = slices.Clone(item.Content)
	out.OutputContent = slices.Clone(item.OutputContent)
	return out
}

func continuationSummaryPartsFromMaps(parts []map[string]any) []accounts.ContinuationSummaryPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]accounts.ContinuationSummaryPart, 0, len(parts))
	for _, part := range parts {
		out = append(out, accounts.ContinuationSummaryPart{
			Type: jsonutil.StringValue(part["type"]),
			Text: jsonutil.StringValue(part["text"]),
		})
	}
	return out
}

func continuationContentPartsFromMaps(parts []map[string]any) []accounts.ContinuationContentPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]accounts.ContinuationContentPart, 0, len(parts))
	for _, part := range parts {
		out = append(out, accounts.ContinuationContentPart{
			Type:     jsonutil.StringValue(part["type"]),
			Text:     jsonutil.StringValue(part["text"]),
			ImageURL: jsonutil.StringValue(part["image_url"]),
			Detail:   jsonutil.StringValue(part["detail"]),
			FileURL:  jsonutil.StringValue(part["file_url"]),
			FileData: jsonutil.StringValue(part["file_data"]),
			FileID:   jsonutil.StringValue(part["file_id"]),
			Filename: jsonutil.StringValue(part["filename"]),
		})
	}
	return out
}
