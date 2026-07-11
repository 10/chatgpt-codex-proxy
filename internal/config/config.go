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

const (
	defaultListenPort            = 8080
	defaultDataDir               = "data"
	defaultDefaultModel          = "gpt-5.6-sol"
	defaultRotationStrategy      = "least_used"
	defaultCodexBaseURL          = "https://chatgpt.com/backend-api"
	defaultAuthIssuer            = "https://auth.openai.com"
	defaultClientID              = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultLoginTimeoutSeconds   = 900
	defaultContinuationTTLMinute = 60
	defaultRequestTimeoutSecond  = 1800
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
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}

	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = defaultDataDir
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
	debugLogPayloads, err := loadBoolEnv("DEBUG_LOG_PAYLOADS", false)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		ListenAddr:       listenAddr,
		DataDir:          dataDir,
		ProxyAPIKey:      strings.TrimSpace(os.Getenv("PROXY_API_KEY")),
		DebugLogPayloads: debugLogPayloads,
		DefaultModel:     defaultDefaultModel,
		RotationStrategy: defaultRotationStrategy,
		CodexBaseURL:     defaultCodexBaseURL,
		AuthIssuer:       defaultAuthIssuer,
		OAuthClientID:    defaultClientID,
		LoginTimeout:     time.Duration(defaultLoginTimeoutSeconds) * time.Second,
		ContinuationTTL:  time.Duration(defaultContinuationTTLMinute) * time.Minute,
		RequestTimeout:   time.Duration(defaultRequestTimeoutSecond) * time.Second,
		RefreshSkew:      60 * time.Second,
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
		return ":" + strconv.Itoa(defaultListenPort), nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return "", fmt.Errorf("PORT must be a valid TCP port")
	}
	return ":" + strconv.Itoa(port), nil
}

func loadBoolEnv(key string, defaultValue bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}

func loadDotEnv() error {
	if err := godotenv.Load(".env"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load .env: %w", err)
	}
	return nil
}
