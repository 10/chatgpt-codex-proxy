package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accountmanager"
	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codexauth"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/devicelogin"
)

func TestEveryRouteRequiresAuthenticationExceptLiveness(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	app := &App{cfg: config.Config{ProxyAPIKey: "test-key"}, engine: gin.New()}
	app.routes()
	for _, route := range app.engine.Routes() {
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			for _, key := range []string{"", "wrong-key"} {
				req := httptest.NewRequest(route.Method, route.Path, strings.NewReader("{}"))
				if key != "" {
					req.Header.Set("Authorization", "Bearer "+key)
				}
				recorder := httptest.NewRecorder()
				app.Handler().ServeHTTP(recorder, req)
				expected := http.StatusUnauthorized
				if route.Path == "/health/live" {
					expected = http.StatusOK
				}
				if recorder.Code != expected {
					t.Fatalf("status=%d, want %d", recorder.Code, expected)
				}
			}
		})
	}
}

func TestAdminEndpointLifecycle(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"user_code":"TEST-CODE","device_auth_id":"device_fixture","interval":5}`)
		case "/api/accounts/deviceauth/token":
			fmt.Fprint(w, `{"authorization_code":"test-code","code_verifier":"test-verifier"}`)
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			accountID := "upstream_fixture"
			if r.Form.Get("grant_type") == "authorization_code" {
				accountID = "upstream_device"
			}
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"` + accountID + `"}`))
			json.NewEncoder(w).Encode(map[string]any{"access_token": "e30." + claims + ".test", "refresh_token": "fixture-refreshed-secret", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer oauthServer.Close()
	cfg := config.Config{ProxyAPIKey: "test-key", DefaultModel: "gpt-6.1-sol", AuthIssuer: oauthServer.URL, OAuthClientID: "test-client", RequestTimeout: time.Second, LoginTimeout: 10 * time.Second}
	svc := newServerAccounts(t, &accounts.Record{ID: "acct_fixture", AccountID: "upstream_fixture", Status: accounts.StatusActive,
		Token:   accounts.OAuthToken{AccessToken: "fixture-access-secret", RefreshToken: "fixture-refresh-secret", ExpiresAt: time.Now().Add(time.Hour)},
		Cookies: map[string]string{"session": "fixture-cookie-secret"},
	})
	oauth := codexauth.NewOAuthService(cfg)
	app := &App{cfg: cfg, engine: gin.New(), logger: slog.New(slog.NewTextHandler(io.Discard, nil)), accounts: svc,
		accountMgr: accountmanager.NewAccountManager(cfg, svc, oauth, nil, nil), deviceLogins: devicelogin.NewDeviceLoginService(oauth, svc, cfg.LoginTimeout),
	}
	app.routes()
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", "test-key")
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, req)
		if recorder.Code != want {
			t.Errorf("%s %s: status=%d, want %d", method, path, recorder.Code, want)
		}
		for _, secret := range []string{"fixture-access-secret", "fixture-refresh-secret", "fixture-refreshed-secret", "fixture-cookie-secret"} {
			if strings.Contains(recorder.Body.String(), secret) {
				t.Errorf("%s %s exposed account credentials", method, path)
				break
			}
		}
		var result map[string]any
		if recorder.Body.Len() > 0 {
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	if request("GET", "/health", "", 200)["default_model"] != "gpt-6.1-sol" {
		t.Fatal("health default model missing")
	}
	request("GET", "/admin/accounts", "", 200)
	patched := request("PATCH", "/admin/accounts/acct_fixture", `{"label":"QA fixture","status":"disabled"}`, 200)
	if patched["label"] != "QA fixture" || patched["status"] != "disabled" {
		t.Fatal("account patch not applied")
	}
	request("PATCH", "/admin/accounts/acct_fixture", `{"status":"invalid"}`, 400)
	request("PATCH", "/admin/accounts/acct_fixture", `{"status":"active"}`, 200)
	request("GET", "/admin/accounts/acct_fixture/usage?cached=true", "", 200)
	request("POST", "/admin/accounts/acct_fixture/refresh", "{}", 200)
	if record := mustGetAccount(t, svc, "acct_fixture"); record.Token.RefreshToken != "fixture-refreshed-secret" {
		t.Fatal("refreshed token was not saved")
	}
	for _, strategy := range []string{"round_robin", "sticky", "least_used"} {
		request("PUT", "/admin/rotation", `{"strategy":"`+strategy+`"}`, 200)
		if request("GET", "/admin/rotation", "", 200)["strategy"] != strategy {
			t.Fatal("rotation update not persisted")
		}
	}
	request("PUT", "/admin/rotation", `{"strategy":"invalid"}`, 400)
	login := request("POST", "/admin/accounts/device-login/start", "{}", 200)
	id, _ := login["login_id"].(string)
	if id == "" || login["auth_url"] != oauthServer.URL+"/codex/device" {
		t.Fatal("device login did not start")
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		status := request("GET", "/admin/accounts/device-login/"+id, "", 200)["status"]
		if status == "ready" {
			break
		}
		if status != "pending" || time.Now().After(deadline) {
			t.Fatalf("device login status=%v", status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	listed := request("GET", "/admin/accounts", "", 200)["accounts"].([]any)
	if len(listed) != 2 {
		t.Fatalf("device login saved %d accounts, want 2", len(listed))
	}
	request("DELETE", "/admin/accounts/acct_fixture", "", 204)
	if _, ok, err := svc.Get("acct_fixture"); err != nil || ok {
		t.Fatal("deleted account remains in store")
	}
	request("DELETE", "/admin/accounts/acct_fixture", "", 404)
	request("PATCH", "/admin/accounts/acct_missing", `{"label":"unused"}`, 404)
	request("GET", "/admin/accounts/acct_missing/usage", "", 404)
	request("GET", "/admin/accounts/acct_missing/usage?cached=true", "", 404)
	request("POST", "/admin/accounts/acct_missing/refresh", "{}", 404)
	request("GET", "/admin/accounts/device-login/missing", "", 404)
}
