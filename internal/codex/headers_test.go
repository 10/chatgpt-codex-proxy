package codex

import (
	"testing"
)

func TestBuildHeadersUsesDesktopIdentity(t *testing.T) {
	headers := BuildHeaders("token", HeaderOptions{IncludeBeta: true})

	if got := headers.Get("User-Agent"); got != "Codex Desktop/26.1007.71326 (Windows NT 10.0; x64)" {
		t.Fatalf("unexpected user-agent: %q", got)
	}
	if got := headers.Get("sec-ch-ua"); got != `"Chromium";v="155", "Not(A:Brand";v="24"` {
		t.Fatalf("unexpected chromium client hint: %q", got)
	}
	if got := headers.Get("sec-ch-ua-platform"); got != `"Windows"` {
		t.Fatalf("unexpected client hint platform: %q", got)
	}
}

func TestChromiumSecCHUAOrdersGreaseByMajorVersion(t *testing.T) {
	if got, want := chromiumSecCHUA(150), `"Not;A=Brand";v="8", "Chromium";v="150"`; got != want {
		t.Fatalf("chromiumSecCHUA(150) = %q, want %q", got, want)
	}
}
