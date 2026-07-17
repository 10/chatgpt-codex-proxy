package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
)

type fakeEventStream struct {
	headers       http.Header
	events        []*codex.StreamEvent
	index         int
	beforeTailErr func()
	tailErr       error
}

type memoryAccountsStore struct {
	state accounts.State
}

func (m *memoryAccountsStore) Load() (accounts.State, error) {
	return m.state, nil
}

func (m *memoryAccountsStore) Save(state accounts.State) error {
	m.state = state
	return nil
}

func (f *fakeEventStream) NextEvent() (*codex.StreamEvent, error) {
	if f.index >= len(f.events) {
		if f.tailErr != nil {
			if f.beforeTailErr != nil {
				f.beforeTailErr()
				f.beforeTailErr = nil
			}
			err := f.tailErr
			f.tailErr = nil
			return nil, err
		}
		return nil, io.EOF
	}
	event := f.events[f.index]
	f.index++
	return event, nil
}

func (f *fakeEventStream) Close() error { return nil }

func (f *fakeEventStream) Headers() http.Header {
	if f.headers == nil {
		return http.Header{}
	}
	return f.headers
}

func newServerAccounts(t *testing.T, records ...*accounts.Record) *accounts.Service {
	t.Helper()

	svc, err := accounts.NewService(&memoryAccountsStore{state: accounts.State{
		Records: records,
	}}, accounts.RotationLeastUsed)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func mustGetAccount(t *testing.T, svc *accounts.Service, id string) accounts.Record {
	t.Helper()

	record, ok, err := svc.Get(id)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", id, err)
	}
	if !ok {
		t.Fatalf("Get(%q) = false, want true", id)
	}
	return record
}

type sseEvent struct {
	Event string
	Data  map[string]any
	Raw   string
}

func parseSSEEvents(t *testing.T, body string) []sseEvent {
	t.Helper()

	chunks := strings.Split(strings.TrimSpace(body), "\n\n")
	events := make([]sseEvent, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}

		var eventName string
		var rawData string
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(line, "event: ") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				rawData = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}

		entry := sseEvent{Event: eventName, Raw: rawData}
		if rawData != "[DONE]" {
			if err := json.Unmarshal([]byte(rawData), &entry.Data); err != nil {
				t.Fatalf("json.Unmarshal(%q) error = %v", rawData, err)
			}
		}
		events = append(events, entry)
	}
	return events
}

func assertEventTypes(t *testing.T, events []sseEvent, want ...string) {
	t.Helper()

	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Event)
	}
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d (%v)", len(got), len(want), got)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("event[%d] = %q, want %q (all=%v)", idx, got[idx], want[idx], got)
		}
	}
}

func nestedMapFromAny(value any) map[string]any {
	mapped, _ := value.(map[string]any)
	return mapped
}

func sliceOfMapsFromAny(value any) []map[string]any {
	return jsonutil.SliceOfMaps(value)
}
