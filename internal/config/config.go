// Package config loads runtime configuration from environment variables with
// safe defaults. No external config library is used; every option is explicit.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	Addr            string
	DataDir         string
	PolicyPath      string
	UpstreamURL     string
	UpstreamModel   string
	UpstreamKey     string
	DisableThinking bool
	UpstreamTimeout time.Duration
	MaxImageMB      int64
	MaxVideoMB      int64
	MaxVideoSec     float64
	FramesMax       int
	MasterKey       string
	JobTTL          time.Duration
	WebhookTimeout  time.Duration
	ShutdownGrace   time.Duration
}

// Load reads configuration from the environment and applies defaults.
func Load() (Config, error) {
	c := Config{
		Addr:            env("MOD_ADDR", ":8096"),
		DataDir:         env("MOD_DATA_DIR", "./data"),
		PolicyPath:      env("MOD_POLICY", "./policy.json"),
		UpstreamURL:     env("MOD_UPSTREAM_URL", "http://127.0.0.1:8000/v1/chat/completions"),
		UpstreamModel:   env("MOD_MODEL", "Qwen/Qwen3.8-27B"),
		UpstreamKey:     env("MOD_UPSTREAM_KEY", ""),
		DisableThinking: boolEnv("MOD_DISABLE_THINKING", true),
		UpstreamTimeout: time.Duration(intEnv("MOD_UPSTREAM_TIMEOUT_SEC", 180)) * time.Second,
		MaxImageMB:      int64(intEnv("MOD_MAX_IMAGE_MB", 12)),
		MaxVideoMB:      int64(intEnv("MOD_MAX_VIDEO_MB", 30)),
		MaxVideoSec:     float64(intEnv("MOD_MAX_VIDEO_SEC", 120)),
		FramesMax:       intEnv("MOD_FRAMES_MAX", 8),
		MasterKey:       env("MOD_MASTER_KEY", ""),
		JobTTL:          time.Duration(intEnv("MOD_JOB_TTL_SEC", 3600)) * time.Second,
		WebhookTimeout:  time.Duration(intEnv("MOD_WEBHOOK_TIMEOUT_SEC", 15)) * time.Second,
		ShutdownGrace:   time.Duration(intEnv("MOD_SHUTDOWN_GRACE_SEC", 10)) * time.Second,
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("config: create data dir: %w", err)
	}
	if c.MasterKey == "" {
		key, err := loadOrCreateMasterKey(c.DataDir)
		if err != nil {
			return Config{}, err
		}
		c.MasterKey = key
	}
	return c, nil
}

// loadOrCreateMasterKey persists an operator key in data/master.key so admin
// access survives restarts without putting secrets in the environment.
func loadOrCreateMasterKey(dir string) (string, error) {
	path := filepath.Join(dir, "master.key")
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 8 {
		return string(raw), nil
	}
	key, err := randomHex(32)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return "", fmt.Errorf("config: write master key: %w", err)
	}
	return key, nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Read(buf); err != nil {
		return "", err
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, b := range buf {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return "amk_master_" + string(out), nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
