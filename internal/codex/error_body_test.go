package codex

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLimitedErrorBodyIncludesReadFailures(t *testing.T) {
	t.Parallel()

	body := readLimitedErrorBody(errReader{err: errors.New("boom")})
	if !strings.Contains(body, "failed to read upstream response body") {
		t.Fatalf("readLimitedErrorBody() = %q, want read failure marker", body)
	}
	if !strings.Contains(body, "boom") {
		t.Fatalf("readLimitedErrorBody() = %q, want underlying read error", body)
	}
}

type errReader struct {
	err error
}

func (r errReader) Read(_ []byte) (int, error) {
	if r.err == nil {
		return 0, io.EOF
	}
	return 0, r.err
}
