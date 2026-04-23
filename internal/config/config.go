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
	Port              int                          `yaml:"port"`
	Bind              string                       `yaml:"bind"`
	Token             string                       `yaml:"token"`
	TokenFile         string                       `yaml:"token_file"`
	OllamaEndpoint    string                       `yaml:"ollama_endpoint"`
	MetricsEnabled    bool                         `yaml:"metrics_enabled"`
	ServiceAllocators []allocators.ServiceConfig   `yaml:"service_allocators"`
}

// Defaults returns the baseline config used when no file and no env vars
// are present. These match SPEC.md §HTTP API.
func Defaults() Config {
	return Config{
		Port:           11435,
		Bind:           "0.0.0.0",
		OllamaEndpoint: "http://localhost:11434",
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
	}
	if v := os.Getenv("RT_AGENT_METRICS"); v == "1" || strings.EqualFold(v, "true") {
		c.MetricsEnabled = true
	}
}
