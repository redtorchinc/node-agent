package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/redtorchinc/node-agent/internal/config/migrate"
)

func TestLoad_DefaultsOnly(t *testing.T) {
	os.Unsetenv("RT_AGENT_CONFIG")
	os.Unsetenv("RT_AGENT_PORT")
	os.Unsetenv("RT_AGENT_BIND")
	os.Unsetenv("RT_AGENT_TOKEN")
	os.Unsetenv("RT_AGENT_METRICS")
	t.Setenv("RT_AGENT_CONFIG", "/nonexistent/path.yaml")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 11435 || c.Bind != "0.0.0.0" {
		t.Errorf("defaults wrong: %+v", c)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	must(t, os.WriteFile(path, []byte("port: 9000\nbind: 127.0.0.1\nmetrics_enabled: true\n"), 0o644))

	t.Setenv("RT_AGENT_CONFIG", path)
	t.Setenv("RT_AGENT_PORT", "12345")
	t.Setenv("RT_AGENT_BIND", "::1")
	t.Setenv("RT_AGENT_TOKEN", "from-env")
	t.Setenv("RT_AGENT_METRICS", "1")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Port != 12345 || c.Bind != "::1" || c.Token != "from-env" || !c.MetricsEnabled {
		t.Errorf("env overrides missing: %+v", c)
	}
}

func TestLoad_TokenFromFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	must(t, os.WriteFile(tokenFile, []byte("  secret-token\n"), 0o600))

	cfgPath := filepath.Join(dir, "config.yaml")
	must(t, os.WriteFile(cfgPath, []byte("token_file: "+tokenFile+"\n"), 0o644))

	t.Setenv("RT_AGENT_CONFIG", cfgPath)
	os.Unsetenv("RT_AGENT_TOKEN")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Token != "secret-token" {
		t.Errorf("token = %q", c.Token)
	}
}

func TestLoad_ServiceAllocators(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	must(t, os.WriteFile(path, []byte(`
service_allocators:
  - name: gliner2-service
    url: http://localhost:8077/v1/debug/gpu
    threshold_warn_mb: 4096
    threshold_critical_mb: 10240
    scrape_interval_s: 30
`), 0o644))

	t.Setenv("RT_AGENT_CONFIG", path)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.ServiceAllocators) != 1 {
		t.Fatalf("want 1 allocator, got %d", len(c.ServiceAllocators))
	}
	a := c.ServiceAllocators[0]
	if a.Name != "gliner2-service" || a.ThresholdCritMB != 10240 {
		t.Errorf("bad parse: %+v", a)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestConfigKeysAreKnownToMigrator pins the contract between the schema and
// the install-time migrator: every top-level Config key must either appear in
// the embedded default template or be listed in migrate.OptionalTopKeys —
// otherwise an operator's setting for it is dropped on the next upgrade (the
// fate of the services allowlist before v0.3.6).
func TestConfigKeysAreKnownToMigrator(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(DefaultYAML), &doc); err != nil {
		t.Fatal(err)
	}
	inDefault := map[string]bool{}
	if len(doc.Content) > 0 {
		m := doc.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			inDefault[m.Content[i].Value] = true
		}
	}
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		key := strings.Split(rt.Field(i).Tag.Get("yaml"), ",")[0]
		if key == "" || key == "-" || key == "config_version" {
			continue
		}
		if !inDefault[key] && !migrate.OptionalTopKeys[key] {
			t.Errorf("config key %q is neither in DefaultYAML nor migrate.OptionalTopKeys — the migrator would drop it", key)
		}
	}
}
