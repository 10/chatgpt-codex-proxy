package codex

import (
	"testing"
)

func TestBuildHeadersUsesDesktopIdentity(t *testing.T) {
	headers := BuildHeaders("token", HeaderOptions{IncludeBeta: true})

	if got := headers.Get("User-Agent"); got != "Codex Desktop/26.409.61251 (win32; x64)" {
		t.Fatalf("unexpected user-agent: %q", got)
	}
	if got := headers.Get("sec-ch-ua-platform"); got != `"Windows"` {
		t.Fatalf("unexpected client hint platform: %q", got)
	}
}
