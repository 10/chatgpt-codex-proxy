package codex

import (
	"testing"
)

func TestBuildHeadersUsesDesktopIdentity(t *testing.T) {
	headers := BuildHeaders("token", HeaderOptions{IncludeBeta: true})

	if got := headers.Get("User-Agent"); got != "Codex Desktop/26.707.31428 (win32; x64)" {
		t.Fatalf("unexpected user-agent: %q", got)
	}
	if got := headers.Get("sec-ch-ua"); got != `"Chromium";v="149", "Not:A-Brand";v="24"` {
		t.Fatalf("unexpected chromium client hint: %q", got)
	}
	if got := headers.Get("sec-ch-ua-platform"); got != `"Windows"` {
		t.Fatalf("unexpected client hint platform: %q", got)
	}
}
