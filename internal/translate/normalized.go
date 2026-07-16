package translate

import (
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/turn"
)

type NormalizedRequest = turn.Request

type NormalizedCompactRequest struct {
	codex.CompactRequest
	ModelExplicit      bool
	PreviousResponseID string
	TupleSchema        map[string]any
	ToolNameAliases    map[string]string
}
