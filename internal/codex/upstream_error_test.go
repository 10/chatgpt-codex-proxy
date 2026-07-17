package codex

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestNewUpstreamErrorParsesRetryAfter(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().UTC().Add(15 * time.Second).Unix()
	tests := []struct {
		name      string
		body      string
		headers   http.Header
		wantCode  string
		wantExact int
		wantMin   int
	}{
		{
			name:      "from json resets_in_seconds",
			body:      `{"error":{"message":"rate limited","resets_in_seconds":12}}`,
			wantExact: 12,
		},
		{
			name:      "from quoted decimal resets_in_seconds",
			body:      `{"error":{"message":"rate limited","resets_in_seconds":"12.9"}}`,
			wantExact: 12,
		},
		{
			name:      "empty json resets_in_seconds falls back to header",
			body:      `{"error":{"message":"rate limited","resets_in_seconds":""}}`,
			headers:   http.Header{"Retry-After": []string{"7"}},
			wantExact: 7,
		},
		{
			name:      "blank field preserves code and valid sibling",
			body:      `{"error":{"code":"rate_limit_exceeded","resets_in_seconds":"","retry_after":" 11 "}}`,
			wantCode:  "rate_limit_exceeded",
			wantExact: 11,
		},
		{
			name:      "from retry-after header",
			body:      `{"error":{"message":"rate limited"}}`,
			headers:   http.Header{"Retry-After": []string{"9"}},
			wantExact: 9,
		},
		{
			name:    "from json resets_at timestamp",
			body:    `{"error":{"message":"rate limited","resets_at":` + strconv.FormatInt(resetAt, 10) + `}}`,
			wantMin: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := NewUpstreamError("codex response", http.StatusTooManyRequests, tc.body, tc.headers)
			if err.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", err.Code, tc.wantCode)
			}
			if tc.wantExact > 0 && err.RetryAfter != tc.wantExact {
				t.Fatalf("RetryAfter = %d, want %d", err.RetryAfter, tc.wantExact)
			}
			if tc.wantMin > 0 && err.RetryAfter < tc.wantMin {
				t.Fatalf("RetryAfter = %d, want >= %d", err.RetryAfter, tc.wantMin)
			}
		})
	}
}
