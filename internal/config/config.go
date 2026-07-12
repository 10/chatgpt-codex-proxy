package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	ListenAddr       string
	DataDir          string
	ProxyAPIKey      string
	DebugLogPayloads bool
	DefaultModel     string
	RotationStrategy string
	CodexBaseURL     string
	AuthIssuer       string
	OAuthClientID    string
	LoginTimeout     time.Duration
	ContinuationTTL  time.Duration
	RequestTimeout   time.Duration
	RefreshSkew      time.Duration
}

func Load() (Config, error) {
	if err := godotenv.Load(".env"); err != nil && !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "data"
	}
	if !filepath.IsAbs(dataDir) {
		cwd, err := os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("resolve cwd: %w", err)
		}
		dataDir = filepath.Join(cwd, dataDir)
	}

	listenAddr, err := loadListenAddr()
	if err != nil {
		return Config{}, err
	}
	debugLogPayloads := false
	if raw := strings.TrimSpace(os.Getenv("DEBUG_LOG_PAYLOADS")); raw != "" {
		var err error
		debugLogPayloads, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("DEBUG_LOG_PAYLOADS must be a boolean")
		}
	}

	cfg := Config{
		ListenAddr:       listenAddr,
		DataDir:          dataDir,
		ProxyAPIKey:      strings.TrimSpace(os.Getenv("PROXY_API_KEY")),
		DebugLogPayloads: debugLogPayloads,
		DefaultModel:     "gpt-5.6-sol",
		RotationStrategy: "least_used",
		CodexBaseURL:     "https://chatgpt.com/backend-api",
		AuthIssuer:       "https://auth.openai.com",
		OAuthClientID:    "app_EMoamEEZ73f0CkXaXp7hrann",
		LoginTimeout:     15 * time.Minute,
		ContinuationTTL:  time.Hour,
		RequestTimeout:   30 * time.Minute,
		RefreshSkew:      time.Minute,
	}

	if cfg.ProxyAPIKey == "" {
		return Config{}, fmt.Errorf("PROXY_API_KEY must be set")
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create data dir: %w", err)
	}

	return cfg, nil
}

func loadListenAddr() (string, error) {
	raw := strings.TrimSpace(os.Getenv("PORT"))
	if raw == "" {
		return ":8080", nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return "", fmt.Errorf("PORT must be a valid TCP port")
	}
	return ":" + strconv.Itoa(port), nil
}
