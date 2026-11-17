package config

import "testing"

func TestLoadRequiresProxyAPIKey(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PROXY_API_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want missing PROXY_API_KEY error")
	}
}

func TestLoadDefaultsToGPT62Sol(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PROXY_API_KEY", "test-key")
	for _, name := range []string{"DATA_DIR", "PORT", "DEBUG_LOG_PAYLOADS"} {
		t.Setenv(name, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultModel != "gpt-6.2-sol" {
		t.Fatalf("DefaultModel = %q, want gpt-6.2-sol", cfg.DefaultModel)
	}
}
