package vllm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/redtorchinc/node-agent/internal/config"
)

// fakeServe is a minimal vLLM: /v1/models returns body, /metrics is empty.
func fakeServe(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		case "/metrics":
			_, _ = w.Write([]byte(""))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

const primaryModels = `{"object":"list","data":[{"id":"resident-4b","max_model_len":4096}]}`
const shadowModels = `{"object":"list","data":[
 {"id":"base-30b","max_model_len":65536,"parent":null},
 {"id":"student-lora","max_model_len":null,"parent":"base-30b"}]}`

// TestProbe_AdditionalEndpointsMerged: a second serve's models (LoRA included)
// land in models[], tagged with their endpoint; endpoints[] lists both serves.
func TestProbe_AdditionalEndpointsMerged(t *testing.T) {
	p, s := fakeServe(t, primaryModels), fakeServe(t, shadowModels)
	d := New(config.PlatformEntry{
		Enabled:             "auto",
		Endpoint:            p.URL,
		AdditionalEndpoints: []config.EndpointEntry{{Endpoint: s.URL}},
	})
	rep := d.Probe(context.Background())

	if !rep.Up || rep.Endpoint != p.URL {
		t.Fatalf("primary: up=%v endpoint=%q", rep.Up, rep.Endpoint)
	}
	if len(rep.Models) != 3 {
		t.Fatalf("want 3 models, got %d: %+v", len(rep.Models), rep.Models)
	}
	byName := map[string]string{}
	for _, m := range rep.Models {
		byName[m.Name] = m.Endpoint
	}
	if byName["resident-4b"] != p.URL || byName["base-30b"] != s.URL || byName["student-lora"] != s.URL {
		t.Errorf("model endpoints wrong: %v", byName)
	}
	for _, m := range rep.Models {
		if m.Name == "student-lora" && m.Parent != "base-30b" {
			t.Errorf("LoRA parent = %q, want base-30b", m.Parent)
		}
		if m.Name == "base-30b" && m.Parent != "" {
			t.Errorf("base model has parent %q", m.Parent)
		}
	}
	if len(rep.Endpoints) != 2 || !rep.Endpoints[0].Up || !rep.Endpoints[1].Up ||
		rep.Endpoints[0].Endpoint != p.URL || rep.Endpoints[1].Endpoint != s.URL {
		t.Errorf("endpoints wrong: %+v", rep.Endpoints)
	}

	// A second (cached) probe must not duplicate the merged models.
	if again := d.Probe(context.Background()); len(again.Models) != 3 {
		t.Errorf("cached probe: want 3 models, got %d", len(again.Models))
	}
}

// TestProbe_AdditionalEndpointDown: a dead extra serve shows up:false in
// endpoints[] and contributes no models; the primary's up is untouched.
func TestProbe_AdditionalEndpointDown(t *testing.T) {
	p, s := fakeServe(t, primaryModels), fakeServe(t, shadowModels)
	dead := s.URL
	s.Close()
	rep := New(config.PlatformEntry{
		Enabled:             "auto",
		Endpoint:            p.URL,
		AdditionalEndpoints: []config.EndpointEntry{{Endpoint: dead}},
	}).Probe(context.Background())

	if !rep.Up || len(rep.Models) != 1 {
		t.Fatalf("primary: up=%v models=%d", rep.Up, len(rep.Models))
	}
	if len(rep.Endpoints) != 2 || rep.Endpoints[1].Up || rep.Endpoints[1].LastError == "" {
		t.Errorf("dead extra endpoint not reported: %+v", rep.Endpoints)
	}
}

// TestProbe_SingleServeUnchanged: without additional_endpoints the report keeps
// its old shape (no endpoints[]), plus the additive per-model endpoint.
func TestProbe_SingleServeUnchanged(t *testing.T) {
	p := fakeServe(t, primaryModels)
	rep := New(config.PlatformEntry{Enabled: "auto", Endpoint: p.URL}).Probe(context.Background())
	if !rep.Up || len(rep.Models) != 1 || rep.Endpoints != nil {
		t.Fatalf("up=%v models=%d endpoints=%+v", rep.Up, len(rep.Models), rep.Endpoints)
	}
	if rep.Models[0].Endpoint != p.URL {
		t.Errorf("model endpoint = %q", rep.Models[0].Endpoint)
	}
}

// TestProbe_DiscoveredServeMerged: a serve found from GPU processes is probed
// like a configured one, tagged source=discovered, and deduplicated by port
// against the primary.
func TestProbe_DiscoveredServeMerged(t *testing.T) {
	p, s := fakeServe(t, primaryModels), fakeServe(t, shadowModels)
	d := New(config.PlatformEntry{Enabled: "auto", Endpoint: p.URL})
	d.SetDiscovered(func() []string { return []string{p.URL, s.URL} }) // primary rediscovered + one new
	rep := d.Probe(context.Background())
	if len(rep.Models) != 3 {
		t.Fatalf("want 3 models, got %d", len(rep.Models))
	}
	if len(rep.Endpoints) != 2 || rep.Endpoints[0].Source != "config" ||
		rep.Endpoints[1].Source != "discovered" || rep.Endpoints[1].Endpoint != s.URL {
		t.Errorf("endpoints wrong: %+v", rep.Endpoints)
	}
}
