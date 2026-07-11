package codex

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const (
	desktopClientVersion = "26.707.31428"
	desktopOriginator    = "Codex Desktop"
	openAIBeta           = "responses_websockets=2026-02-06"
	codexResidency       = "us"
	desktopUserAgent     = "Codex Desktop/" + desktopClientVersion + " (win32; x64)"
	chromiumPreset       = "chrome-149"
	chromiumVersion      = "149"
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
	headers.Set("sec-ch-ua", fmt.Sprintf(`"Chromium";v="%s", "Not:A-Brand";v="24"`, chromiumVersion))
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

func cookieHeader(cookies map[string]string) string {
	pairs := make([]string, 0, len(cookies))
	keys := make([]string, 0, len(cookies))
	for key := range cookies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, cookies[key]))
	}
	return strings.Join(pairs, "; ")
}
