package codex

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
)

func TestParseQuotaFromHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header func(http.Header)
		assert func(*testing.T, *accounts.QuotaSnapshot)
	}{
		{
			name: "includes secondary window and credits",
			header: func(headers http.Header) {
				headers.Set("X-Codex-Primary-Used-Percent", "82.5")
				headers.Set("X-Codex-Primary-Window-Minutes", "300")
				headers.Set("X-Codex-Primary-Reset-At", "4102444800")
				headers.Set("X-Codex-Secondary-Used-Percent", "12")
				headers.Set("X-Codex-Secondary-Window-Minutes", "10080")
				headers.Set("X-Codex-Secondary-Reset-At", "4103049600")
				headers.Set("X-Codex-Credits-Has-Credits", "true")
				headers.Set("X-Codex-Credits-Unlimited", "false")
				headers.Set("X-Codex-Credits-Balance", "19.5")
				headers.Set("X-Codex-Active-Limit", "plus")
			},
			assert: func(t *testing.T, snapshot *accounts.QuotaSnapshot) {
				t.Helper()
				if snapshot == nil {
					t.Fatal("ParseQuotaFromHeaders() = nil")
				}
				if snapshot.SecondaryRateLimit == nil {
					t.Fatal("expected secondary rate limit")
				}
				if snapshot.Credits == nil {
					t.Fatal("expected credits snapshot")
				}
				if !snapshot.Credits.HasCredits || snapshot.Credits.Unlimited {
					t.Fatalf("credits flags = %#v", snapshot.Credits)
				}
				if snapshot.Credits.Balance == nil || *snapshot.Credits.Balance != 19.5 {
					t.Fatalf("balance = %#v, want 19.5", snapshot.Credits.Balance)
				}
				if snapshot.Credits.ActiveLimit != "plus" {
					t.Fatalf("active_limit = %q, want plus", snapshot.Credits.ActiveLimit)
				}
			},
		},
		{
			name: "ignores credits-only headers",
			header: func(headers http.Header) {
				headers.Set("X-Codex-Credits-Has-Credits", "true")
				headers.Set("X-Codex-Credits-Unlimited", "false")
				headers.Set("X-Codex-Credits-Balance", "19.5")
				headers.Set("X-Codex-Active-Limit", "plus")
			},
			assert: func(t *testing.T, snapshot *accounts.QuotaSnapshot) {
				t.Helper()
				if snapshot != nil {
					t.Fatalf("ParseQuotaFromHeaders() = %#v, want nil for credits-only headers", snapshot)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			headers := http.Header{}
			tc.header(headers)
			tc.assert(t, ParseQuotaFromHeaders(headers))
		})
	}
}

func TestParseQuotaFromEvent(t *testing.T) {
	t.Parallel()

	event := &StreamEvent{
		Type: "codex.rate_limits",
		Raw: map[string]any{
			"rate_limits": map[string]any{
				"primary": map[string]any{
					"used_percent":   100.0,
					"window_minutes": 300.0,
					"reset_at":       float64(4102444800),
				},
				"secondary": map[string]any{
					"used_percent":   45.0,
					"window_minutes": 10080.0,
					"reset_at":       float64(4103049600),
				},
				"code_review": map[string]any{
					"used_percent":         100.0,
					"limit_window_seconds": 86400.0,
					"reset_at":             float64(4103136000),
				},
			},
		},
	}

	snapshot := ParseQuotaFromEvent(event, "plus")
	if snapshot == nil {
		t.Fatal("ParseQuotaFromEvent() = nil")
	}
	if snapshot.Source != "response_event" {
		t.Fatalf("source = %q, want response_event", snapshot.Source)
	}
	if snapshot.RateLimit.UsedPercent == nil || *snapshot.RateLimit.UsedPercent != 100 {
		t.Fatalf("primary used_percent = %#v, want 100", snapshot.RateLimit.UsedPercent)
	}
	if !snapshot.RateLimit.LimitReached {
		t.Fatal("expected primary limit_reached")
	}
	if snapshot.SecondaryRateLimit == nil || snapshot.SecondaryRateLimit.UsedPercent == nil || *snapshot.SecondaryRateLimit.UsedPercent != 45 {
		t.Fatalf("secondary = %#v, want used_percent 45", snapshot.SecondaryRateLimit)
	}
	if snapshot.CodeReviewRateLimit == nil || snapshot.CodeReviewRateLimit.UsedPercent == nil || *snapshot.CodeReviewRateLimit.UsedPercent != 100 {
		t.Fatalf("code_review = %#v, want used_percent 100", snapshot.CodeReviewRateLimit)
	}
	if snapshot.CodeReviewRateLimit.LimitWindowSeconds == nil || *snapshot.CodeReviewRateLimit.LimitWindowSeconds != 86400 {
		t.Fatalf("code_review limit_window_seconds = %#v, want 86400", snapshot.CodeReviewRateLimit.LimitWindowSeconds)
	}
}

func TestQuotaFromUsageResponseKeepsOverallStateOffIndividualWindows(t *testing.T) {
	t.Parallel()

	payload := UsageResponse{PlanType: "plus"}
	payload.RateLimit.Allowed = false
	payload.RateLimit.LimitReached = true
	payload.RateLimit.PrimaryWindow = &UsageWindow{
		UsedPercent:        0,
		LimitWindowSeconds: 18000,
		ResetAt:            time.Now().UTC().Add(5 * time.Hour).Unix(),
	}
	payload.RateLimit.SecondaryWindow = &UsageWindow{
		UsedPercent:        100,
		LimitWindowSeconds: 604800,
		ResetAt:            time.Now().UTC().Add(7 * 24 * time.Hour).Unix(),
	}

	snapshot := QuotaFromUsageResponse(payload)
	if !snapshot.RateLimit.Allowed {
		t.Fatal("primary allowed = false, want true for an unused window")
	}
	if snapshot.RateLimit.LimitReached {
		t.Fatal("primary limit_reached = true, want false for an unused window")
	}
	if snapshot.SecondaryRateLimit == nil || !snapshot.SecondaryRateLimit.LimitReached {
		t.Fatalf("secondary = %#v, want exhausted window", snapshot.SecondaryRateLimit)
	}
}

func TestQuotaFromUsageResponseHandlesSecondaryWindowPresence(t *testing.T) {
	t.Parallel()

	const resetAt = int64(4103049600)
	tests := []struct {
		name           string
		payload        string
		wantSecondary  bool
		wantUsed       float64
		wantWindowSecs int
		wantResetAt    int64
	}{
		{
			name:          "ignores an empty secondary window",
			payload:       `{"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_at":4102444800},"secondary_window":{}}}`,
			wantSecondary: false,
		},
		{
			name:           "keeps a zero-percent secondary window with metadata",
			payload:        `{"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_at":4102444800},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":4103049600}}}`,
			wantSecondary:  true,
			wantUsed:       0,
			wantWindowSecs: 604800,
			wantResetAt:    resetAt,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var usage UsageResponse
			if err := json.Unmarshal([]byte(tc.payload), &usage); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}

			quota := QuotaFromUsageResponse(usage)
			if tc.wantSecondary != (quota.SecondaryRateLimit != nil) {
				t.Fatalf("secondary_rate_limit = %#v, want present = %v", quota.SecondaryRateLimit, tc.wantSecondary)
			}
			if !tc.wantSecondary {
				return
			}
			if quota.SecondaryRateLimit.UsedPercent == nil || *quota.SecondaryRateLimit.UsedPercent != tc.wantUsed {
				t.Fatalf("used_percent = %#v, want %v", quota.SecondaryRateLimit.UsedPercent, tc.wantUsed)
			}
			if quota.SecondaryRateLimit.LimitWindowSeconds == nil || *quota.SecondaryRateLimit.LimitWindowSeconds != tc.wantWindowSecs {
				t.Fatalf("limit_window_seconds = %#v, want %d", quota.SecondaryRateLimit.LimitWindowSeconds, tc.wantWindowSecs)
			}
			if quota.SecondaryRateLimit.ResetAt == nil || quota.SecondaryRateLimit.ResetAt.Unix() != tc.wantResetAt {
				t.Fatalf("reset_at = %#v, want %d", quota.SecondaryRateLimit.ResetAt, tc.wantResetAt)
			}
		})
	}
}

func TestUsageResponseIgnoresCodeReviewSecondaryWindow(t *testing.T) {
	t.Parallel()

	var response UsageResponse
	if err := json.Unmarshal([]byte(`{"code_review_rate_limit":{"secondary_window":{"used_percent":"malformed"}}}`), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
}

func TestQuotaFromUsageResponseIncludesCodeReviewRateLimit(t *testing.T) {
	t.Parallel()

	payload := UsageResponse{PlanType: "plus"}
	payload.RateLimit.Allowed = true
	payload.RateLimit.PrimaryWindow = &UsageWindow{
		UsedPercent:        25,
		LimitWindowSeconds: 3600,
		ResetAt:            time.Now().UTC().Add(time.Hour).Unix(),
	}
	payload.CodeReviewRateLimit = &UsageResponseCodeReviewRateLimit{
		Allowed:      true,
		LimitReached: true,
		PrimaryWindow: &UsageWindow{
			UsedPercent:        100,
			LimitWindowSeconds: 86400,
			ResetAt:            time.Now().UTC().Add(24 * time.Hour).Unix(),
		},
	}

	snapshot := QuotaFromUsageResponse(payload)
	if snapshot.CodeReviewRateLimit == nil {
		t.Fatal("expected code review rate limit")
	}
	if !snapshot.CodeReviewRateLimit.LimitReached {
		t.Fatal("expected code review limit_reached")
	}
}

func TestUsageResponseDecodesCreditBalance(t *testing.T) {
	t.Parallel()
	var usage UsageResponse
	if err := json.Unmarshal([]byte(`{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false},"credits":{"has_credits":true,"unlimited":false,"balance":"12.50"}}`), &usage); err != nil {
		t.Fatal(err)
	}
	quota := QuotaFromUsageResponse(usage)
	if quota.Credits == nil || quota.Credits.Balance == nil || *quota.Credits.Balance != 12.5 {
		t.Fatalf("credit balance lost: %#v", quota.Credits)
	}
}
