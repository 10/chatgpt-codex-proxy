package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/accounts/jsonstore"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/server"
)

func TestProxyRoutesAcrossAccountsAndPersistsCooldown(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/codex/models" {
			fmt.Fprint(w, `{"models":[{"slug":"gpt-6.1-sol"}]}`)
			return
		}
		if r.Header.Get("ChatGPT-Account-Id") == "upstream-a" {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-b" || r.Header.Get("ChatGPT-Account-Id") != "upstream-b" {
			t.Errorf("unexpected upstream credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/codex/responses" {
			http.NotFound(w, r)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var body struct {
			Model string `json:"model"`
		}
		if err := conn.ReadJSON(&body); err != nil || body.Model != "gpt-6.1-sol" {
			t.Errorf("unexpected upstream request: model=%q err=%v", body.Model, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_fixture","model":"gpt-6.1-sol","status":"completed","output_text":"hello"}}`)); err != nil {
			t.Error(err)
		}

	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	store := jsonstore.NewJSONAccountsStore(dataDir)
	now := time.Now().UTC()
	state := accounts.State{RotationStrategy: accounts.RotationRoundRobin}
	for _, id := range []string{"a", "b"} {
		state.Records = append(state.Records, &accounts.Record{
			ID: "acct-" + id, AccountID: "upstream-" + id, Status: accounts.StatusActive,
			Token:     accounts.OAuthToken{AccessToken: "token-" + id, ExpiresAt: now.Add(time.Hour)},
			CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	app, err := server.New(config.Config{
		ProxyAPIKey: "test-key", DataDir: dataDir, DefaultModel: "gpt-6.1-sol", CodexBaseURL: upstream.URL,
		RequestTimeout: 5 * time.Second, RefreshSkew: time.Minute, ContinuationTTL: time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	proxy := httptest.NewServer(app.Handler())
	defer proxy.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, proxy.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-API-Key", "test-key")
		req.Header.Set("Content-Type", "application/json")
		if path == "/v1/messages" {
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d body=%s err=%v", method, path, resp.StatusCode, payload, err)
		}
		var result map[string]any
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	response := request("POST", "/v1/responses", `{"input":"hello","tools":[{"type":"web_search"}]}`, http.StatusOK)
	if response["output_text"] != "hello" || response["model"] != "gpt-6.1-sol" {
		t.Fatalf("response lost default model or text: %#v", response)
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	foundCooledAccount := false
	for _, record := range persisted.Records {
		if record.ID == "acct-a" {
			foundCooledAccount = true
		}
		if record.ID == "acct-a" && (record.CooldownUntil == nil || record.CooldownUntil.Before(now.Add(110*time.Second))) {
			t.Fatal("failover did not persist the upstream retry delay")
		}
	}
	if !foundCooledAccount || len(persisted.Records) != 2 {
		t.Fatalf("persisted accounts missing after failover: %#v", persisted.Records)
	}
	info, err := os.Stat(filepath.Join(dataDir, "accounts.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("account storage permissions: info=%v err=%v", info, err)
	}
	request("DELETE", "/admin/accounts/acct-a", "", http.StatusNoContent)
	request("DELETE", "/admin/accounts/acct-b", "", http.StatusNoContent)
	for _, test := range []struct{ path, body string }{
		{"/v1/responses", `{"input":"hello"}`},
		{"/v1/messages", `{"model":"gpt-6.1-sol","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/responses/compact", `{"input":"hello"}`},
		{"/v1/images/generations", `{"prompt":"a tree"}`},
	} {
		response := request("POST", test.path, test.body, http.StatusServiceUnavailable)
		if response["error"].(map[string]any)["message"] != "no available accounts" {
			t.Fatalf("unavailable account error changed: %#v", response)
		}
	}
}
