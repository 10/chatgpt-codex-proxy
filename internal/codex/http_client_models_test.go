package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpcloak "github.com/sardanioss/httpcloak/client"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/config"
)

func TestGetCodexModelsNormalizesBackendMetadata(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/codex/models" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[
			{"slug":"gpt-6-astra","display_name":"Astra","context_window":300000,"max_context_window":900000,"default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high"},{"effort":"max"}]},
			{"id":"other-model","supported_reasoning_efforts":[{"reasoningEffort":"low"},{"reasoning_effort":"low"},{"effort":"high"}]}
		]}`)
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	client := NewHTTPClient(config.Config{CodexBaseURL: upstream.URL, RequestTimeout: 5 * time.Second})
	defer client.Close()
	client.sessions["test"] = httpcloak.NewSession(chromiumPreset, httpcloak.WithTimeout(5*time.Second), httpcloak.WithDisableHTTP3(), httpcloak.WithoutRetry(), httpcloak.WithInsecureSkipVerify())
	entries, err := client.GetCodexModels(context.Background(), accounts.Record{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("models = %#v", entries)
	}
	astra, other := entries[0], entries[1]
	if astra.ID != "gpt-6-astra" || astra.DisplayName != "Astra" || astra.ContextWindow != 300000 || astra.MaxContextWindow != 900000 || astra.DefaultReasoningEffort != "high" || len(astra.SupportedReasoningEfforts) != 2 || astra.SupportedReasoningEfforts[1].ReasoningEffort != "max" {
		t.Fatalf("lost Astra metadata: %#v", astra)
	}
	if other.ID != "other-model" || other.DisplayName != "other-model" || other.DefaultReasoningEffort != "low" || len(other.SupportedReasoningEfforts) != 2 || other.SupportedReasoningEfforts[1].ReasoningEffort != "high" {
		t.Fatalf("incorrect normalized reasoning efforts: %#v", other)
	}
}
