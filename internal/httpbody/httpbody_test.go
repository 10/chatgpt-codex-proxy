package httpbody

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadLimitedErrorBodyIncludesReadFailures(t *testing.T) {
	t.Parallel()

	body := ReadLimitedErrorBody(iotest.ErrReader(errors.New("boom")))
	if !strings.Contains(body, "failed to read upstream response body") {
		t.Fatalf("ReadLimitedErrorBody() = %q, want read failure marker", body)
	}
	if !strings.Contains(body, "boom") {
		t.Fatalf("ReadLimitedErrorBody() = %q, want underlying read error", body)
	}
}
