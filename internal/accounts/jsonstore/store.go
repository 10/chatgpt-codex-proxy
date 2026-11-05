package jsonstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/jsonfile"
)

type JSONAccountsStore struct {
	path string
}

func NewJSONAccountsStore(dataDir string) *JSONAccountsStore {
	return &JSONAccountsStore{
		path: filepath.Join(dataDir, "accounts.json"),
	}
}

func (s *JSONAccountsStore) Load() (accounts.State, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return accounts.State{}, nil
		}
		return accounts.State{}, fmt.Errorf("read accounts store: %w", err)
	}

	var state accounts.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return accounts.State{}, fmt.Errorf("decode accounts store: %w", err)
	}
	return state, nil
}

func (s *JSONAccountsStore) Save(state accounts.State) error {
	if err := jsonfile.Write(s.path, state); err != nil {
		return fmt.Errorf("save accounts store: %w", err)
	}
	return nil
}
