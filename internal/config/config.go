// Package config loads sitesync configuration from a YAML file and overlays
// environment variables. The data directory, server port and timeouts are all
// overridable so the same binary runs in development, tests and the eval image.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the resolved process configuration.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Trial     TrialConfig     `yaml:"trial"`
	Backfill  BackfillConfig  `yaml:"backfill"`
	Log       LogConfig       `yaml:"log"`
}

type ServerConfig struct {
	Port            int           `yaml:"port"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type StorageConfig struct {
	DataDir       string `yaml:"data_dir"`
	DBFile        string `yaml:"db_file"`
	MaxOpenConns  int    `yaml:"max_open_conns"`
	BusyTimeoutMS int    `yaml:"busy_timeout_ms"`
}

type SchedulerConfig struct {
	PollInterval      time.Duration `yaml:"poll_interval"`
	EscalatorInterval time.Duration `yaml:"escalator_interval"`
	RecoveryOnStart   bool          `yaml:"recovery_on_start"`
	MaxRetries        int           `yaml:"max_retries"`
	BaseBackoff       time.Duration `yaml:"base_backoff"`
	MaxBackoff        time.Duration `yaml:"max_backoff"`
	LeaseTTL          time.Duration `yaml:"lease_ttl"`
}

type TrialConfig struct {
	DefaultWindowHours    int `yaml:"default_window_hours"`
	AcceptanceWindowHours int `yaml:"acceptance_window_hours"`
}

type BackfillConfig struct {
	WindowHours            int `yaml:"window_hours"`
	ManualReviewAfterHours int `yaml:"manual_review_after_hours"`
}

type LogConfig struct {
	Level       string `yaml:"level"`
	Development bool   `yaml:"development"`
}

// Default returns a configuration that boots out of the box: port 48557,
// a ./data directory, sane scheduler intervals and a one-week backfill window.
func Default() Config {
	return Config{
		Server: ServerConfig{
			Port: 48557, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second,
			IdleTimeout: 60 * time.Second, ShutdownTimeout: 10 * time.Second,
		},
		Storage: StorageConfig{
			DataDir: "./data", DBFile: "sitesync.db", MaxOpenConns: 1, BusyTimeoutMS: 15000,
		},
		Scheduler: SchedulerConfig{
			PollInterval: 8 * time.Hour, EscalatorInterval: 1 * time.Hour,
			RecoveryOnStart: true, MaxRetries: 5, BaseBackoff: 2 * time.Second,
			MaxBackoff: 2 * time.Minute, LeaseTTL: 10 * time.Minute,
		},
		Trial:    TrialConfig{DefaultWindowHours: 24 * 30, AcceptanceWindowHours: 24 * 14},
		Backfill: BackfillConfig{WindowHours: 168, ManualReviewAfterHours: 336},
		Log:      LogConfig{Level: "info", Development: false},
	}
}

// Load reads the YAML file at path (if any) then overlays environment variables
// prefixed with SITESYNC_. Missing files fall back to Default.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		if err == nil {
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// applyEnv overlays SITESYNC_* variables onto the resolved config. Each helper
// ignores unparseable values so a bad env var never prevents startup.
func applyEnv(cfg *Config) {
	envPosInt("SITESYNC_SERVER_PORT", &cfg.Server.Port)
	envStr("SITESYNC_STORAGE_DATA_DIR", &cfg.Storage.DataDir)
	envStr("SITESYNC_STORAGE_DB_FILE", &cfg.Storage.DBFile)
	envDur("SITESYNC_SCHEDULER_POLL_INTERVAL", &cfg.Scheduler.PollInterval)
	envDur("SITESYNC_SCHEDULER_ESCALATOR_INTERVAL", &cfg.Scheduler.EscalatorInterval)
	envPosInt("SITESYNC_BACKFILL_WINDOW_HOURS", &cfg.Backfill.WindowHours)
	envStr("SITESYNC_LOG_LEVEL", &cfg.Log.Level)
}

func envStr(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func envPosInt(key string, dst *int) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			*dst = n
		}
	}
}

func envDur(key string, dst *time.Duration) {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}
}

// Validate enforces invariants and returns the first violation found.
func (c Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port %d", c.Server.Port)
	}
	if strings.TrimSpace(c.Storage.DataDir) == "" {
		return fmt.Errorf("storage.data_dir must not be empty")
	}
	if c.Storage.DBFile == "" {
		return fmt.Errorf("storage.db_file must not be empty")
	}
	if c.Scheduler.PollInterval <= 0 {
		return fmt.Errorf("scheduler.poll_interval must be positive")
	}
	if c.Scheduler.EscalatorInterval <= 0 {
		return fmt.Errorf("scheduler.escalator_interval must be positive")
	}
	if c.Scheduler.MaxRetries < 0 {
		return fmt.Errorf("scheduler.max_retries must be non-negative")
	}
	if c.Trial.DefaultWindowHours <= 0 || c.Trial.AcceptanceWindowHours <= 0 {
		return fmt.Errorf("trial windows must be positive")
	}
	if c.Backfill.WindowHours <= 0 {
		return fmt.Errorf("backfill.window_hours must be positive")
	}
	return nil
}

// DSN returns the SQLite connection string for the configured data directory.
func (c Config) DSN() string {
	dbPath := filepath.Join(c.Storage.DataDir, c.Storage.DBFile)
	return fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=synchronous(NORMAL)", dbPath, c.Storage.BusyTimeoutMS)
}
