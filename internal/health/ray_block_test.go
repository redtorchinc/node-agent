package health

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/ray"
)

// The ray block follows the rdma contract: absent from the JSON entirely
// when this node isn't in a Ray cluster, so a consumer can treat presence
// as membership. A `"ray": null` or `"ray": {}` would break that reading.
func TestRayBlockOmittedWhenNil(t *testing.T) {
	b, err := json.Marshal(Report{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"ray"`) {
		t.Errorf("ray key present on a report with no Ray:\n%s", b)
	}
}

func TestRayBlockCarriesGroupingFields(t *testing.T) {
	alive := 8
	rep := Report{Ray: &ray.Info{
		Running:    true,
		Role:       "worker",
		GCSAddress: "192.168.50.96:6379",
		NodeIP:     "10.10.10.7",
		ClusterID:  "session_2026-08-25_10-00-00_123456_789",
		AliveNodes: &alive,
		Resources:  map[string]float64{"GPU": 1, "CPU": 20},
	}}

	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Ray struct {
			Running    bool               `json:"running"`
			Role       string             `json:"role"`
			GCSAddress string             `json:"gcs_address"`
			NodeIP     string             `json:"node_ip"`
			ClusterID  string             `json:"cluster_id"`
			AliveNodes *int               `json:"alive_nodes"`
			Resources  map[string]float64 `json:"resources"`
		} `json:"ray"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	// gcs_address is the field the backend groups a TP serving unit on.
	// If this name or shape changes it is a cross-repo break.
	if got.Ray.GCSAddress != "192.168.50.96:6379" {
		t.Errorf("gcs_address = %q", got.Ray.GCSAddress)
	}
	if got.Ray.Role != "worker" {
		t.Errorf("role = %q", got.Ray.Role)
	}
	if !got.Ray.Running {
		t.Error("running should be true")
	}
	if got.Ray.ClusterID == "" {
		t.Error("cluster_id should round-trip")
	}
	if got.Ray.AliveNodes == nil || *got.Ray.AliveNodes != 8 {
		t.Errorf("alive_nodes = %v, want 8", got.Ray.AliveNodes)
	}
	if got.Ray.Resources["GPU"] != 1 {
		t.Errorf("resources[GPU] = %v, want 1", got.Ray.Resources["GPU"])
	}
}

// alive_nodes must serialize as an explicit null when unknown, never be
// dropped. A missing key would let a consumer read it as zero — i.e. an
// empty cluster — when the truth is "the dashboard wasn't reachable".
func TestRayAliveNodesNullWhenUnknown(t *testing.T) {
	b, err := json.Marshal(Report{Ray: &ray.Info{Running: true, Role: "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"alive_nodes":null`) {
		t.Errorf("want an explicit alive_nodes:null, got:\n%s", b)
	}
}

func TestRayDisabledByConfigYieldsNoDetector(t *testing.T) {
	cfg := config.Defaults()
	cfg.Ray.Enabled = "false"
	if cfg.RayEnabled() {
		t.Fatal("RayEnabled() should be false for enabled:false")
	}
	r, err := NewReporter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ray != nil {
		t.Error("no Ray detector should be constructed when disabled")
	}
}

func TestRayEnabledByDefaultAndForAuto(t *testing.T) {
	for _, v := range []string{"", "auto", "true", "AUTO"} {
		cfg := config.Defaults()
		cfg.Ray.Enabled = v
		if !cfg.RayEnabled() {
			t.Errorf("RayEnabled() = false for enabled:%q, want true", v)
		}
	}
}

func TestRayDefaultsAreSane(t *testing.T) {
	cfg := config.Defaults()
	if cfg.Ray.Enabled != "auto" {
		t.Errorf("default Ray.Enabled = %q, want auto", cfg.Ray.Enabled)
	}
	if cfg.Ray.ProbeIntervalS != 30 {
		t.Errorf("default Ray.ProbeIntervalS = %d, want 30", cfg.Ray.ProbeIntervalS)
	}
	if cfg.Ray.DashboardURL != "" {
		t.Errorf("default Ray.DashboardURL = %q, want empty (derive it)", cfg.Ray.DashboardURL)
	}
}

// The probe must not add a degraded reason: being a TP worker is a topology
// fact, not a health problem, and degraded_reasons is a cross-repo contract.
func TestRayDoesNotAddDegradedReason(t *testing.T) {
	now := time.Unix(1713820000, 0)
	alive := 8

	base := cleanReport(now)
	baseDeg, baseReasons := Evaluate(base, config.Config{}, now)

	// Worst realistic case: a worker whose dashboard fetch failed and whose
	// reading is stale. None of that is a health problem.
	withRay := cleanReport(now)
	withRay.Ray = &ray.Info{
		Running: true, Role: "worker",
		GCSAddress: "10.0.0.1:6379", AliveNodes: &alive,
		Error: "dashboard query failed: connection refused",
		Stale: true,
	}
	deg, reasons := Evaluate(withRay, config.Config{}, now)

	if deg != baseDeg {
		t.Errorf("ray block flipped degraded from %v to %v", baseDeg, deg)
	}
	if len(reasons) != len(baseReasons) {
		t.Errorf("ray block changed reasons from %v to %v", baseReasons, reasons)
	}
	for _, r := range reasons {
		if strings.Contains(r, "ray") || strings.Contains(r, "tp_") {
			t.Errorf("ray produced degraded reason %q; docs/degraded-reasons.md must not grow silently", r)
		}
	}
}
