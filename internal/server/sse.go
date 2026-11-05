package server

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"

	"github.com/gin-gonic/gin"
)

func responseEventJSON(eventType, responseID string, payload map[string]any) []byte {
	eventPayload := maps.Clone(payload)
	if eventPayload == nil {
		eventPayload = map[string]any{}
	}
	if responseID != "" {
		eventPayload["response_id"] = responseID
	}
	eventPayload["type"] = eventType
	data, _ := json.Marshal(eventPayload)
	return data
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":"%v"}`, err))
	}
	return data
}

func writeSSE(w io.Writer, eventName string, payload []byte) {
	if eventName != "" {
		_, _ = io.WriteString(w, "event: "+eventName+"\n")
	}
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(payload)
	_, _ = io.WriteString(w, "\n\n")
}

func prepareStreamResponse(c *gin.Context) {
	headers := c.Writer.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("Connection", "keep-alive")
	headers.Set("Content-Encoding", "identity")
	headers.Set("X-Accel-Buffering", "no")
	headers.Del("Content-Length")
	c.Status(http.StatusOK)
}
