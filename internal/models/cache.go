package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"chatgpt-codex-proxy/internal/jsonfile"
)

const cacheFilename = "models-cache.json"

type CacheSnapshot struct {
	Models  []Entry             `json:"models"`
	Support map[string][]string `json:"support"`
}

func LoadCache(dataDir string) (CacheSnapshot, error) {
	path := filepath.Join(dataDir, cacheFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CacheSnapshot{}, nil
		}
		return CacheSnapshot{}, fmt.Errorf("read models cache: %w", err)
	}

	var snapshot CacheSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return CacheSnapshot{}, fmt.Errorf("decode models cache: %w", err)
	}
	return snapshot, nil
}

func SaveCache(dataDir string, snapshot CacheSnapshot) error {
	if err := jsonfile.Write(filepath.Join(dataDir, cacheFilename), snapshot); err != nil {
		return fmt.Errorf("save models cache: %w", err)
	}
	return nil
}
