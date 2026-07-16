package turn

import (
	"testing"

	"chatgpt-codex-proxy/internal/codex"
)

func TestToCodexWSCreatePayloadIncludesTurnControls(t *testing.T) {
	t.Parallel()

	generate := false
	parallel := false
	request := Request{
		Request: codex.Request{
			Model:             "gpt-5.4",
			Input:             []codex.InputItem{{Role: "user"}},
			ParallelToolCalls: &parallel,
		},
		Generate: &generate,
	}
	payload := request.ToCodexWSCreatePayload()
	if payload["generate"] != false || payload["parallel_tool_calls"] != false {
		t.Fatalf("payload = %#v", payload)
	}
}
