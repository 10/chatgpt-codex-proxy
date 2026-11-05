package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

type StreamReader struct {
	reader *bufio.Reader
	io.Closer
	headers http.Header
}

func (r *StreamReader) Headers() http.Header {
	return r.headers.Clone()
}

func (r *StreamReader) NextEvent() (*turn.StreamEvent, error) {
	var eventName string
	var dataLines []string
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF && len(dataLines) > 0 {
				return parseStreamEvent(eventName, strings.Join(dataLines, "\n"))
			}
			return nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			return parseStreamEvent(eventName, strings.Join(dataLines, "\n"))
		}
		lowerLine := strings.ToLower(line)
		if strings.HasPrefix(lowerLine, "event:") {
			eventName = strings.TrimSpace(line[6:])
			continue
		}
		if strings.HasPrefix(lowerLine, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(line[5:]))
		}
	}
}

func parseStreamEvent(eventName, data string) (*turn.StreamEvent, error) {
	if data == "" || data == "[DONE]" {
		return nil, io.EOF
	}
	event, err := decodeStreamEvent([]byte(data))
	if err != nil {
		return nil, err
	}
	if name := strings.TrimSpace(eventName); name != "" {
		event.Type = name
	}
	return event, nil
}

func decodeStreamEvent(data []byte) (*turn.StreamEvent, error) {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values in upstream event")
		}
		return nil, err
	}
	return &turn.StreamEvent{Type: jsonutil.StringValue(raw["type"]), Raw: raw}, nil
}
