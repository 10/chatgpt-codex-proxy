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
