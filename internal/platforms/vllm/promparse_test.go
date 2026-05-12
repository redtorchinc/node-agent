package vllm

import (
	"math"
	"testing"
)

const sample = `# HELP vllm:num_requests_running Running.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model_name="qwen3-vl:32b"} 2
vllm:num_requests_running{model_name="other"} 0
# HELP vllm:num_requests_waiting Waiting.
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{model_name="qwen3-vl:32b"} 5
# HELP vllm:gpu_cache_usage_perc KV cache GPU usage [0..1].
# TYPE vllm:gpu_cache_usage_perc gauge
vllm:gpu_cache_usage_perc{model_name="qwen3-vl:32b"} 0.743
# HELP vllm:prompt_tokens_total Counter.
# TYPE vllm:prompt_tokens_total counter
vllm:prompt_tokens_total{model_name="qwen3-vl:32b"} 1000000
# HELP vllm:time_to_first_token_seconds TTFT.
# TYPE vllm:time_to_first_token_seconds histogram
vllm:time_to_first_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.05"} 0
vllm:time_to_first_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.1"} 50
vllm:time_to_first_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.2"} 90
vllm:time_to_first_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.5"} 99
vllm:time_to_first_token_seconds_bucket{model_name="qwen3-vl:32b",le="1.0"} 100
vllm:time_to_first_token_seconds_bucket{model_name="qwen3-vl:32b",le="+Inf"} 100
vllm:time_to_first_token_seconds_sum{model_name="qwen3-vl:32b"} 12.5
vllm:time_to_first_token_seconds_count{model_name="qwen3-vl:32b"} 100
`

func TestParsePrometheus_GaugeCounter(t *testing.T) {
	s := parsePrometheus(sample)
	lbl := map[string]string{"model_name": "qwen3-vl:32b"}

	if v, ok := s.gauge("vllm:num_requests_running", lbl); !ok || v != 2 {
		t.Errorf("running gauge: %v, %v", v, ok)
	}
	if v, ok := s.gauge("vllm:num_requests_waiting", lbl); !ok || v != 5 {
		t.Errorf("waiting gauge: %v, %v", v, ok)
	}
	if v, ok := s.counter("vllm:prompt_tokens_total", lbl); !ok || v != 1000000 {
		t.Errorf("counter: %v, %v", v, ok)
	}
	if v, ok := s.gauge("vllm:gpu_cache_usage_perc", lbl); !ok || math.Abs(v-0.743) > 1e-6 {
		t.Errorf("kv cache: %v, %v", v, ok)
	}
	other := map[string]string{"model_name": "other"}
	if v, ok := s.gauge("vllm:num_requests_running", other); !ok || v != 0 {
		t.Errorf("other model gauge: %v, %v", v, ok)
	}
}

func TestHistogramQuantile_LinearInterp(t *testing.T) {
	s := parsePrometheus(sample)
	lbl := map[string]string{"model_name": "qwen3-vl:32b"}

	// 50th percentile: target = 50, bucket [0.05..0.1] has 0..50.
	// width 0.05, inBucket 50, target-prev=50 → 0.05 + 0.05*(50/50)=0.1.
	p50, ok := s.histogramQuantile("vllm:time_to_first_token_seconds", lbl, 0.5)
	if !ok {
		t.Fatalf("p50 not found")
	}
	if math.Abs(p50-0.1) > 1e-9 {
		t.Errorf("p50: got %v, want ~0.1", p50)
	}

	// 99th percentile: target = 99, falls exactly at end of bucket [0.2..0.5] (count=99).
	p99, ok := s.histogramQuantile("vllm:time_to_first_token_seconds", lbl, 0.99)
	if !ok {
		t.Fatalf("p99 not found")
	}
	if math.Abs(p99-0.5) > 1e-9 {
		t.Errorf("p99: got %v, want ~0.5", p99)
	}
}

func TestHistogramQuantile_AbsentReturnsFalse(t *testing.T) {
	s := parsePrometheus(sample)
	if _, ok := s.histogramQuantile("nonexistent", map[string]string{}, 0.5); ok {
		t.Errorf("expected ok=false for missing histogram")
	}
}

func TestParseLabelSet_Quoted(t *testing.T) {
	got := parseLabelSet(`a="1",b="two",c="three"`)
	if len(got) != 3 {
		t.Fatalf("expected 3 labels, got %d: %v", len(got), got)
	}
	if got[0][0] != "a" || got[0][1] != "1" {
		t.Errorf("unexpected: %v", got)
	}
}
