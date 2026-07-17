package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAPIKeyAuthentication(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		prepare    func(*http.Request)
		wantStatus int
	}{
		{
			name: "accepts X-API-Key header",
			prepare: func(request *http.Request) {
				request.Header.Set("X-API-Key", "secret")
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "accepts bearer authorization header",
			prepare: func(request *http.Request) {
				request.Header.Set("Authorization", "Bearer secret")
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects missing key",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			engine := gin.New()
			engine.Use(APIKeyWithUnauthorized("secret", func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) }))
			engine.GET("/private", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			request := httptest.NewRequest(http.MethodGet, "/private", nil)
			if tc.prepare != nil {
				tc.prepare(request)
			}
			engine.ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestAPIKeyWithUnauthorizedUsesProtocolRenderer(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	engine.Use(APIKeyWithUnauthorized("secret", func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"type": "anthropic_error"})
	}))
	engine.GET("/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "anthropic_error") {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}
