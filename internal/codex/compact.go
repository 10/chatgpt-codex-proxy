package codex

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

type CompactResponse struct {
	ID        string           `json:"id,omitempty"`
	Object    string           `json:"object,omitempty"`
	CreatedAt int64            `json:"created_at,omitempty"`
	Output    []map[string]any `json:"output,omitempty"`
	Usage     map[string]any   `json:"usage,omitempty"`
}

func (c *HTTPClient) CompactResponse(ctx context.Context, record accounts.Record, req turn.CompactRequest) (CompactResponse, *accounts.QuotaSnapshot, error) {
	stream, err := c.StreamResponse(ctx, record, turn.Request{
		Model:        req.Model,
		Instructions: req.Instructions,
		Input:        slices.Concat(req.Input, []turn.InputItem{{Type: "compaction_trigger"}}),
		Text:         req.Text,
		Reasoning:    req.Reasoning,
	}, "")
	if err != nil {
		return CompactResponse{}, nil, err
	}
	defer stream.Close()

	quota := ParseQuotaFromHeaders(stream.Headers())
	accumulator := turn.NewAccumulator(turn.NormalizedRequest{})
	var compacted map[string]any
	compactionCount := 0
	for {
		event, err := stream.NextEvent()
		if err == io.EOF {
			return CompactResponse{}, quota, fmt.Errorf("codex compaction stream ended before response.completed")
		}
		if err != nil {
			return CompactResponse{}, quota, err
		}
		if err := StreamEventError(event); err != nil {
			return CompactResponse{}, quota, err
		}
		if snapshot := ParseQuotaFromEvent(event, record.PlanType); snapshot != nil {
			quota = snapshot
		}
		accumulator.Apply(event)
		if event.Type == "response.output_item.done" {
			item := jsonutil.MapValue(event.Raw, "item")
			if jsonutil.StringValue(item["type"]) == "compaction" {
				compactionCount++
				compacted = item
			}
		}
		if accumulator.IsIncomplete() {
			return CompactResponse{}, quota, fmt.Errorf("codex compaction response incomplete: %s", accumulator.NativeFinishReason())
		}
		if !accumulator.IsCompleted() {
			continue
		}
		if compactionCount != 1 {
			return CompactResponse{}, quota, fmt.Errorf("codex compaction expected exactly one compaction item, got %d", compactionCount)
		}
		if strings.TrimSpace(jsonutil.StringValue(compacted["encrypted_content"])) == "" {
			return CompactResponse{}, quota, fmt.Errorf("codex compaction item is missing encrypted_content")
		}
		if accumulator.ResponseID == "" {
			return CompactResponse{}, quota, fmt.Errorf("codex compaction response is missing its id")
		}

		// The encrypted item is the next context window. Collect it from item.done;
		// response.completed can carry an empty output array.
		return CompactResponse{
			ID:        accumulator.ResponseID,
			Object:    "response.compaction",
			CreatedAt: accumulator.CreatedAt,
			Output:    []map[string]any{compacted},
			Usage:     jsonutil.MapValue(jsonutil.MapValue(event.Raw, "response"), "usage"),
		}, quota, nil
	}
}
