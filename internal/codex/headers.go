package codex

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

const (
	desktopClientVersion = "26.1007.71326"
	desktopOriginator    = "Codex Desktop"
	openAIBeta           = "responses_websockets=2026-02-06"
	codexResidency       = "us"
	desktopUserAgent     = "Codex Desktop/" + desktopClientVersion + " (Windows NT 10.0; x64)"
	chromiumPreset       = "chrome-155"
	chromiumMajor        = 155
	clientHintPlatform   = "Windows"
	acceptLanguage       = "en-US,en;q=0.9"
)

type HeaderOptions struct {
	AccountID      string
	Cookies        map[string]string
	ContentType    string
	TurnState      string
	RequestID      string
	Accept         string
	AcceptEncoding string
	IncludeBeta    bool
}

func BuildHeaders(token string, opts HeaderOptions) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+token)
	if opts.AccountID != "" {
		headers.Set("ChatGPT-Account-Id", opts.AccountID)
	}
	headers.Set("originator", desktopOriginator)
	headers.Set("x-openai-internal-codex-residency", codexResidency)
	if opts.RequestID != "" {
		headers.Set("x-client-request-id", opts.RequestID)
	}
	if opts.TurnState != "" {
		headers.Set("x-codex-turn-state", opts.TurnState)
	}
	if opts.IncludeBeta {
		headers.Set("OpenAI-Beta", openAIBeta)
	}
	headers.Set("User-Agent", desktopUserAgent)
	headers.Set("sec-ch-ua", chromiumSecCHUA(chromiumMajor))
	headers.Set("sec-ch-ua-mobile", "?0")
	headers.Set("sec-ch-ua-platform", fmt.Sprintf(`"%s"`, clientHintPlatform))
	if opts.AcceptEncoding != "" {
		headers.Set("Accept-Encoding", opts.AcceptEncoding)
	} else {
		headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	}
	headers.Set("Accept-Language", acceptLanguage)
	headers.Set("sec-fetch-site", "same-origin")
	headers.Set("sec-fetch-mode", "cors")
	headers.Set("sec-fetch-dest", "empty")
	if opts.ContentType != "" {
		headers.Set("Content-Type", opts.ContentType)
	}
	if opts.Accept != "" {
		headers.Set("Accept", opts.Accept)
	}
	if len(opts.Cookies) > 0 {
		headers.Set("Cookie", cookieHeader(opts.Cookies))
	}
	return headers
}

// chromiumSecCHUA returns the sec-ch-ua an unbranded Chromium build such as Electron
// sends: its own brand and a GREASE brand, both chosen and ordered by the major
// version (Chromium's components/embedder_support/user_agent_utils.cc).
func chromiumSecCHUA(major int) string {
	const greaseChars = " (:-./);=?_"
	greaseVersions := [...]string{"8", "99", "24"}
	brands := []string{
		fmt.Sprintf(`"Not%cA%cBrand";v="%s"`, greaseChars[major%len(greaseChars)], greaseChars[(major+1)%len(greaseChars)], greaseVersions[major%len(greaseVersions)]),
		fmt.Sprintf(`"Chromium";v="%d"`, major),
	}
	if major%2 == 1 {
		brands[0], brands[1] = brands[1], brands[0]
	}
	return strings.Join(brands, ", ")
}

func cookieHeader(cookies map[string]string) string {
	pairs := make([]string, 0, len(cookies))
	for _, key := range slices.Sorted(maps.Keys(cookies)) {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, cookies[key]))
	}
	return strings.Join(pairs, "; ")
}
