package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestLoggerLogsRecoveredPanic(t *testing.T) {
	t.Parallel()

	engine, logs := newLoggedEngine(t)
	engine.Use(Recovery(newTestLogger(logs)))
	engine.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	engine.ServeHTTP(recorder, request)

	entries := decodeLogEntries(t, logs)
	if len(entries) != 2 {
		t.Fatalf("log entries = %d, want 2", len(entries))
	}
	if entries[0]["msg"] != "panic recovered" {
		t.Fatalf("first msg = %v, want panic recovered", entries[0]["msg"])
	}
	if entries[0]["level"] != "ERROR" {
		t.Fatalf("first level = %v, want ERROR", entries[0]["level"])
	}
	if entries[1]["msg"] != "http request completed" {
		t.Fatalf("second msg = %v, want http request completed", entries[1]["msg"])
	}
	if entries[1]["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("second status = %v, want %d", entries[1]["status"], http.StatusInternalServerError)
	}
	if entries[1]["level"] != "ERROR" {
		t.Fatalf("second level = %v, want ERROR", entries[1]["level"])
	}
}

func TestRequestLoggerDoesNotLogBodyContents(t *testing.T) {
	t.Parallel()

	engine, logs := newLoggedEngine(t)
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Status(http.StatusAccepted)
	})

	recorder := httptest.NewRecorder()
	body := `{"secret":"do-not-log-me"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)

	if strings.Contains(logs.String(), "do-not-log-me") {
		t.Fatal("request body appeared in logs")
	}
}

func newLoggedEngine(t *testing.T) (*gin.Engine, *bytes.Buffer) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	logs := new(bytes.Buffer)
	engine := gin.New()
	engine.SetTrustedProxies(nil)
	engine.Use(RequestID())
	engine.Use(RequestLogger(newTestLogger(logs)))
	return engine, logs
}

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func decodeLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	raw := strings.TrimSpace(buf.String())
	if raw == "" {
		return nil
	}

	lines := strings.Split(raw, "\n")
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("unmarshal log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}
