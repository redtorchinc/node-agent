// Package config loads rt-node-agent configuration from file + env.
//
// Load order (later wins): defaults → config.yaml → env vars.
// Default config path is /etc/rt-node-agent/config.yaml on Linux/macOS and
// %ProgramData%\rt-node-agent\config.yaml on Windows; override with
// RT_AGENT_CONFIG.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/redtorchinc/node-agent/internal/allocators"
)

// Config is the effective runtime config after all sources merge.
type Config struct {
	ConfigVersion int `yaml:"config_version"`

	Port           int    `yaml:"port"`
	Bind           string `yaml:"bind"`
	Token          string `yaml:"token"`
	TokenFile      string `yaml:"token_file"`
	MetricsEnabled bool   `yaml:"metrics_enabled"`

	// Platforms is the v0.2.0 home for inference-platform endpoints. The
	// legacy top-level `ollama_endpoint` is still honored and folds into
	// Platforms.Ollama.Endpoint when set.
	Platforms PlatformsConfig `yaml:"platforms"`

	// OllamaEndpoint is the v0.1.x location; deprecated, scheduled for
	// removal in v0.3.0. Still loaded so existing configs work.
	OllamaEndpoint string `yaml:"ollama_endpoint"`

	ServiceAllocators []allocators.ServiceConfig `yaml:"service_allocators"`

	Services ServicesConfig `yaml:"services"`
	Disk     DiskConfig     `yaml:"disk"`

	TrainingMode TrainingModeConfig `yaml:"training_mode"`
	RDMA         RDMAConfig         `yaml:"rdma"`
}

// PlatformsConfig wires per-platform detection.
type PlatformsConfig struct {
	Ollama PlatformEntry `yaml:"ollama"`
	VLLM   PlatformEntry `yaml:"vllm"`
}

// PlatformEntry is one platform's settings.
//
// Enabled: "auto" (default), "true"/"false". Auto probes once at startup and
// keeps checking on every /health request if reachable.
type PlatformEntry struct {
	Enabled         string `yaml:"enabled"`
	Endpoint        string `yaml:"endpoint"`
	MetricsEndpoint string `yaml:"metrics_endpoint"`
	Required        bool   `yaml:"required"`
}

// ServicesConfig declares the allowlist of systemd/launchd units the agent
// is permitted to start/stop/restart via POST /actions/service.
type ServicesConfig struct {
	Manager string                `yaml:"manager"` // systemd | launchd | windows-svc | "" (detect)
	Allowed []ServiceAllowedEntry `yaml:"allowed"`
}

// ServiceAllowedEntry is one allowlisted unit.
type ServiceAllowedEntry struct {
	Name        string   `yaml:"name"`
	Actions     []string `yaml:"actions"`
	Description string   `yaml:"description"`
}

// DiskConfig configures the disk[] surface in /health.
type DiskConfig struct {
	Paths []string `yaml:"paths"`
}

// TrainingModeConfig configures training-mode coordination (Phase B).
type TrainingModeConfig struct {
	StateFile          string `yaml:"state_file"`
	GracePeriodS       int    `yaml:"grace_period_s"`
	DisableOllamaProbe *bool  `yaml:"disable_ollama_probe"`
}

// RDMAConfig configures RDMA collection (Phase B, Linux only).
type RDMAConfig struct {
	Enabled                  string `yaml:"enabled"` // auto | true | false
	CollectIntervalS         int    `yaml:"collect_interval_s"`
	PFCStormThresholdRxRate  int    `yaml:"pfc_storm_threshold_rx_rate"`
	PFCStormWindowS          int    `yaml:"pfc_storm_window_s"`
	ErrorsGrowingWindowS     int    `yaml:"errors_growing_window_s"`
}

// Defaults returns the baseline config used when no file and no env vars
// are present. These match SPEC.md §HTTP API.
func Defaults() Config {
	return Config{
		ConfigVersion:  SchemaVersion,
		Port:           11435,
		Bind:           "0.0.0.0",
		OllamaEndpoint: "http://localhost:11434",
		Platforms: PlatformsConfig{
			Ollama: PlatformEntry{Enabled: "auto", Endpoint: "http://localhost:11434"},
			VLLM:   PlatformEntry{Enabled: "auto", Endpoint: "http://localhost:8000", MetricsEndpoint: "http://localhost:8000/metrics"},
		},
		Disk: DiskConfig{Paths: nil},
		TrainingMode: TrainingModeConfig{
			StateFile:    defaultStateFilePath(),
			GracePeriodS: 3600,
		},
		RDMA: RDMAConfig{
			Enabled:                 "auto",
			CollectIntervalS:        5,
			PFCStormThresholdRxRate: 1000,
			PFCStormWindowS:         30,
			ErrorsGrowingWindowS:    60,
		},
	}
}

// DefaultConfigPath returns the platform-conventional location.
func DefaultConfigPath() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "rt-node-agent", "config.yaml")
	}
	return "/etc/rt-node-agent/config.yaml"
}

// DefaultTokenPath returns the platform-conventional token file location.
func DefaultTokenPath() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "rt-node-agent", "token")
	}
	return "/etc/rt-node-agent/token"
}

// defaultStateFilePath is the per-OS location for /var/lib/rt-node-agent/training_mode.json
// (or its Windows equivalent under %ProgramData%).
func defaultStateFilePath() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "rt-node-agent", "training_mode.json")
	}
	return "/var/lib/rt-node-agent/training_mode.json"
}

// Load builds a Config from file (if present) overridden by env. Missing
// config file is not an error — the agent must run out of the box.
func Load() (Config, error) {
	c := Defaults()

	path := os.Getenv("RT_AGENT_CONFIG")
	if path == "" {
		path = DefaultConfigPath()
	}
	if err := loadFile(path, &c); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, fmt.Errorf("read %s: %w", path, err)
	}

	applyEnv(&c)
	normalize(&c)

	// Resolve token_file if set and token not explicitly set.
	if c.Token == "" {
		tp := c.TokenFile
		if tp == "" {
			tp = DefaultTokenPath()
		}
		if b, err := os.ReadFile(tp); err == nil {
			c.Token = strings.TrimSpace(string(b))
		}
	}

	return c, nil
}

func loadFile(path string, c *Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, c)
}

func applyEnv(c *Config) {
	if v := os.Getenv("RT_AGENT_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Port = n
		}
	}
	if v := os.Getenv("RT_AGENT_BIND"); v != "" {
		c.Bind = v
	}
	if v := os.Getenv("RT_AGENT_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("RT_AGENT_OLLAMA"); v != "" {
		c.OllamaEndpoint = v
		c.Platforms.Ollama.Endpoint = v
	}
	if v := os.Getenv("RT_AGENT_VLLM"); v != "" {
		c.Platforms.VLLM.Endpoint = v
	}
	if v := os.Getenv("RT_AGENT_METRICS"); v == "1" || strings.EqualFold(v, "true") {
		c.MetricsEnabled = true
	}
}

// normalize reconciles legacy fields with their v0.2.0 equivalents and
// fills in zero-value subfields with defaults so callers can read straight
// from Config without nil-checks.
func normalize(c *Config) {
	// Fold the legacy `ollama_endpoint` into platforms.ollama.endpoint
	// only if the new key wasn't set explicitly. Reverse-fold so v0.1.x
	// consumers reading c.OllamaEndpoint keep working.
	if c.Platforms.Ollama.Endpoint == "" && c.OllamaEndpoint != "" {
		c.Platforms.Ollama.Endpoint = c.OllamaEndpoint
	}
	if c.OllamaEndpoint == "" && c.Platforms.Ollama.Endpoint != "" {
		c.OllamaEndpoint = c.Platforms.Ollama.Endpoint
	}
	if c.Platforms.Ollama.Enabled == "" {
		c.Platforms.Ollama.Enabled = "auto"
	}
	if c.Platforms.VLLM.Enabled == "" {
		c.Platforms.VLLM.Enabled = "auto"
	}
	if c.Platforms.VLLM.Endpoint == "" {
		c.Platforms.VLLM.Endpoint = "http://localhost:8000"
	}
	if c.Platforms.VLLM.MetricsEndpoint == "" {
		c.Platforms.VLLM.MetricsEndpoint = strings.TrimRight(c.Platforms.VLLM.Endpoint, "/") + "/metrics"
	}
	if c.TrainingMode.StateFile == "" {
		c.TrainingMode.StateFile = defaultStateFilePath()
	}
	if c.TrainingMode.GracePeriodS == 0 {
		c.TrainingMode.GracePeriodS = 3600
	}
	if c.RDMA.Enabled == "" {
		c.RDMA.Enabled = "auto"
	}
	if c.RDMA.CollectIntervalS == 0 {
		c.RDMA.CollectIntervalS = 5
	}
	if c.RDMA.PFCStormThresholdRxRate == 0 {
		c.RDMA.PFCStormThresholdRxRate = 1000
	}
	if c.RDMA.PFCStormWindowS == 0 {
		c.RDMA.PFCStormWindowS = 30
	}
	if c.RDMA.ErrorsGrowingWindowS == 0 {
		c.RDMA.ErrorsGrowingWindowS = 60
	}
}
