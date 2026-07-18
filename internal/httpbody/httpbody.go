// Package httpbody provides helpers for bounded HTTP response bodies.
package httpbody

import (
	"fmt"
	"io"
)

const Limit int64 = 32 * 1024

// ReadLimitedErrorBody reads at most Limit bytes from an upstream error response.
func ReadLimitedErrorBody(r io.Reader) string {
	if r == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(r, Limit))
	if err != nil {
		return fmt.Sprintf("<failed to read upstream response body: %v>", err)
	}
	return string(body)
}
