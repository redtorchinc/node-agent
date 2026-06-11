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

// vLLM ≥0.6 metric exposition: kv_cache_usage_perc gauge,
// prefix_cache_hits_total/queries_total counters, and the renamed
// request_time_per_output_token_seconds histogram.
const sampleV06 = `# HELP vllm:kv_cache_usage_perc KV cache usage [0..1].
# TYPE vllm:kv_cache_usage_perc gauge
vllm:kv_cache_usage_perc{model_name="qwen3-vl:32b"} 0.5
# HELP vllm:prefix_cache_hits_total Prefix cache hits.
# TYPE vllm:prefix_cache_hits_total counter
vllm:prefix_cache_hits_total{model_name="qwen3-vl:32b"} 80
# HELP vllm:prefix_cache_queries_total Prefix cache queries.
# TYPE vllm:prefix_cache_queries_total counter
vllm:prefix_cache_queries_total{model_name="qwen3-vl:32b"} 100
# HELP vllm:request_time_per_output_token_seconds TPOT.
# TYPE vllm:request_time_per_output_token_seconds histogram
vllm:request_time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.01"} 0
vllm:request_time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.02"} 50
vllm:request_time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.05"} 90
vllm:request_time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.1"} 99
vllm:request_time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="+Inf"} 100
vllm:request_time_per_output_token_seconds_sum{model_name="qwen3-vl:32b"} 2.0
vllm:request_time_per_output_token_seconds_count{model_name="qwen3-vl:32b"} 100
`

// Pre-0.6 exposition: the legacy gauge names + time_per_output_token_seconds.
// Same observable values as sampleV06 so the fallback paths must produce
// identical Model output.
const sampleLegacy = `# HELP vllm:gpu_cache_usage_perc KV cache GPU usage [0..1].
# TYPE vllm:gpu_cache_usage_perc gauge
vllm:gpu_cache_usage_perc{model_name="qwen3-vl:32b"} 0.5
# HELP vllm:gpu_prefix_cache_hit_rate Prefix cache hit rate.
# TYPE vllm:gpu_prefix_cache_hit_rate gauge
vllm:gpu_prefix_cache_hit_rate{model_name="qwen3-vl:32b"} 0.8
# HELP vllm:time_per_output_token_seconds TPOT.
# TYPE vllm:time_per_output_token_seconds histogram
vllm:time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.01"} 0
vllm:time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.02"} 50
vllm:time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.05"} 90
vllm:time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="0.1"} 99
vllm:time_per_output_token_seconds_bucket{model_name="qwen3-vl:32b",le="+Inf"} 100
vllm:time_per_output_token_seconds_sum{model_name="qwen3-vl:32b"} 2.0
vllm:time_per_output_token_seconds_count{model_name="qwen3-vl:32b"} 100
`

// buildModel must produce identical kv_cache + tpot output whether the node
// runs vLLM ≥0.6 (renamed metrics) or an older build (legacy names via the
// backward-compat fallback). Regression guard for the metric-name drift that
// silently emptied kv_cache/tpot until v0.2.12.
func TestBuildModel_MetricRenames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		metrics string
	}{
		{"vllm>=0.6", sampleV06},
		{"legacy", sampleLegacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := vllmModel{ID: "qwen3-vl:32b"}
			got := buildModel(m, parsePrometheus(tc.metrics), 0, nil, 0)

			if got.KVCache == nil {
				t.Fatalf("KVCache nil — kv_cache_usage/prefix metrics not mapped")
			}
			// 0.5 fraction → 50 percent.
			if got.KVCache.GPUUsagePct == nil || math.Abs(*got.KVCache.GPUUsagePct-50) > 1e-6 {
				t.Errorf("GPUUsagePct = %v, want 50", got.KVCache.GPUUsagePct)
			}
			// hits/queries = 80/100 = 0.8 (legacy gauge is 0.8 directly).
			if got.KVCache.PrefixCacheHitRate == nil || math.Abs(*got.KVCache.PrefixCacheHitRate-0.8) > 1e-6 {
				t.Errorf("PrefixCacheHitRate = %v, want 0.8", got.KVCache.PrefixCacheHitRate)
			}

			if got.Latency == nil {
				t.Fatalf("Latency nil — tpot histogram not mapped")
			}
			// p50 bucket [0.01..0.02] → 0.02s → 20ms; p99 → 0.1s → 100ms.
			if got.Latency.TPOTp50 == nil || math.Abs(*got.Latency.TPOTp50-20) > 1e-6 {
				t.Errorf("TPOTp50 = %v, want 20ms", got.Latency.TPOTp50)
			}
			if got.Latency.TPOTp99 == nil || math.Abs(*got.Latency.TPOTp99-100) > 1e-6 {
				t.Errorf("TPOTp99 = %v, want 100ms", got.Latency.TPOTp99)
			}
		})
	}
}

// Eval-telemetry series (v0.2.13): prefill/decode phase histograms, cached
// prompt tokens, and request_success_total fanned out over finished_reason
// (the live ≥0.6 shape — engine label included to mirror real exposition).
const sampleEvalTelemetry = `# HELP vllm:prefix_cache_hits_total Prefix cache hits.
# TYPE vllm:prefix_cache_hits_total counter
vllm:prefix_cache_hits_total{engine="0",model_name="m"} 80
# HELP vllm:prefix_cache_queries_total Prefix cache queries.
# TYPE vllm:prefix_cache_queries_total counter
vllm:prefix_cache_queries_total{engine="0",model_name="m"} 100
# HELP vllm:prompt_tokens_total Number of prefill tokens processed.
# TYPE vllm:prompt_tokens_total counter
vllm:prompt_tokens_total{engine="0",model_name="m"} 100
# HELP vllm:prompt_tokens_cached_total Number of cached prompt tokens.
# TYPE vllm:prompt_tokens_cached_total counter
vllm:prompt_tokens_cached_total{engine="0",model_name="m"} 80
# HELP vllm:request_success_total Count of finished requests.
# TYPE vllm:request_success_total counter
vllm:request_success_total{engine="0",finished_reason="stop",model_name="m"} 200
vllm:request_success_total{engine="0",finished_reason="length",model_name="m"} 30
vllm:request_success_total{engine="0",finished_reason="repetition",model_name="m"} 5
vllm:request_success_total{engine="0",finished_reason="abort",model_name="m"} 3
vllm:request_success_total{engine="0",finished_reason="error",model_name="m"} 2
# HELP vllm:request_prefill_time_seconds Histogram of PREFILL phase time.
# TYPE vllm:request_prefill_time_seconds histogram
vllm:request_prefill_time_seconds_bucket{engine="0",model_name="m",le="0.5"} 0
vllm:request_prefill_time_seconds_bucket{engine="0",model_name="m",le="1.0"} 50
vllm:request_prefill_time_seconds_bucket{engine="0",model_name="m",le="2.0"} 99
vllm:request_prefill_time_seconds_bucket{engine="0",model_name="m",le="+Inf"} 100
vllm:request_prefill_time_seconds_sum{engine="0",model_name="m"} 90
vllm:request_prefill_time_seconds_count{engine="0",model_name="m"} 100
# HELP vllm:request_decode_time_seconds Histogram of DECODE phase time.
# TYPE vllm:request_decode_time_seconds histogram
vllm:request_decode_time_seconds_bucket{engine="0",model_name="m",le="5.0"} 0
vllm:request_decode_time_seconds_bucket{engine="0",model_name="m",le="10.0"} 50
vllm:request_decode_time_seconds_bucket{engine="0",model_name="m",le="20.0"} 99
vllm:request_decode_time_seconds_bucket{engine="0",model_name="m",le="+Inf"} 100
vllm:request_decode_time_seconds_sum{engine="0",model_name="m"} 1000
vllm:request_decode_time_seconds_count{engine="0",model_name="m"} 100
`

func TestCounterSum_FinishedReasonFanout(t *testing.T) {
	s := parsePrometheus(sampleEvalTelemetry)
	lbl := map[string]string{"model_name": "m"}

	// Sum across all finished_reason series: 200+30+5+3+2.
	if v, ok := s.counterSum("vllm:request_success_total", lbl); !ok || v != 240 {
		t.Errorf("counterSum all = %v, %v; want 240", v, ok)
	}
	// Narrowed to one reason.
	abort := map[string]string{"model_name": "m", "finished_reason": "abort"}
	if v, ok := s.counterSum("vllm:request_success_total", abort); !ok || v != 3 {
		t.Errorf("counterSum abort = %v, %v; want 3", v, ok)
	}
	if _, ok := s.counterSum("vllm:request_success_total", map[string]string{"model_name": "absent"}); ok {
		t.Errorf("expected ok=false for absent model")
	}
}

func TestBuildModel_EvalTelemetry(t *testing.T) {
	m := vllmModel{ID: "m"}
	got := buildModel(m, parsePrometheus(sampleEvalTelemetry), 0, nil, 0)

	if got.KVCache == nil {
		t.Fatalf("KVCache nil")
	}
	if got.KVCache.PrefixCacheHitsTotal == nil || *got.KVCache.PrefixCacheHitsTotal != 80 {
		t.Errorf("PrefixCacheHitsTotal = %v, want 80", got.KVCache.PrefixCacheHitsTotal)
	}
	if got.KVCache.PrefixCacheQueriesTotal == nil || *got.KVCache.PrefixCacheQueriesTotal != 100 {
		t.Errorf("PrefixCacheQueriesTotal = %v, want 100", got.KVCache.PrefixCacheQueriesTotal)
	}
	if got.KVCache.PrefixCacheHitRate == nil || math.Abs(*got.KVCache.PrefixCacheHitRate-0.8) > 1e-9 {
		t.Errorf("PrefixCacheHitRate = %v, want 0.8", got.KVCache.PrefixCacheHitRate)
	}

	if got.Latency == nil {
		t.Fatalf("Latency nil — prefill/decode histograms not mapped")
	}
	// prefill p50: bucket [0.5..1.0], target 50 of 100 → 1.0s → 1000ms.
	if got.Latency.PrefillP50 == nil || math.Abs(*got.Latency.PrefillP50-1000) > 1e-6 {
		t.Errorf("PrefillP50 = %v, want 1000ms", got.Latency.PrefillP50)
	}
	// prefill p99: target 99 at end of bucket [1.0..2.0] → 2000ms.
	if got.Latency.PrefillP99 == nil || math.Abs(*got.Latency.PrefillP99-2000) > 1e-6 {
		t.Errorf("PrefillP99 = %v, want 2000ms", got.Latency.PrefillP99)
	}
	if got.Latency.DecodeP50 == nil || math.Abs(*got.Latency.DecodeP50-10000) > 1e-6 {
		t.Errorf("DecodeP50 = %v, want 10000ms", got.Latency.DecodeP50)
	}

	if got.Counters == nil {
		t.Fatalf("Counters nil")
	}
	// success = 240 total - (3 abort + 2 error) = 235; failed = 5. The old
	// first-match read would have reported success=200 (the stop series only).
	if got.Counters.RequestsSuccessTotal == nil || *got.Counters.RequestsSuccessTotal != 235 {
		t.Errorf("RequestsSuccessTotal = %v, want 235", got.Counters.RequestsSuccessTotal)
	}
	if got.Counters.RequestsFailedTotal == nil || *got.Counters.RequestsFailedTotal != 5 {
		t.Errorf("RequestsFailedTotal = %v, want 5", got.Counters.RequestsFailedTotal)
	}
	if got.Counters.PromptTokensCachedTotal == nil || *got.Counters.PromptTokensCachedTotal != 80 {
		t.Errorf("PromptTokensCachedTotal = %v, want 80", got.Counters.PromptTokensCachedTotal)
	}
}

// Legacy exposition (no prefill/decode histograms, no finished_reason
// fan-out beyond what old builds emit) must keep producing the old fields
// and leave the new ones nil — additive contract.
func TestBuildModel_EvalTelemetry_AbsentOnLegacy(t *testing.T) {
	m := vllmModel{ID: "qwen3-vl:32b"}
	got := buildModel(m, parsePrometheus(sampleLegacy), 0, nil, 0)

	if got.KVCache == nil {
		t.Fatalf("KVCache nil on legacy")
	}
	if got.KVCache.PrefixCacheHitsTotal != nil || got.KVCache.PrefixCacheQueriesTotal != nil {
		t.Errorf("prefix counts should be nil on legacy exposition")
	}
	if got.Latency == nil {
		t.Fatalf("Latency nil on legacy")
	}
	if got.Latency.PrefillP50 != nil || got.Latency.DecodeP50 != nil {
		t.Errorf("prefill/decode should be nil on legacy exposition")
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
