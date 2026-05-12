# `GET /metrics`

Prometheus text exposition. Behind `metrics_enabled: true` (or
`RT_AGENT_METRICS=1` env). Off by default — most fleets pull from `/health`
JSON instead.

Label cardinality is bounded by node-local concepts (GPU index, platform
name, model name, RDMA device). `run_id` is also bounded (one active run
per node at a time).

## Series

### Host-level

```
rt_agent_memory_used_pct  <0..100>
rt_agent_swap_used_pct    <0..100>
rt_agent_degraded         <0|1>
rt_agent_cpu_usage_pct    <0..100>
```

### GPU

```
rt_agent_gpu_vram_used_pct{index="0",name="NVIDIA H100 80GB HBM3"}  <0..100>
rt_agent_gpu_util_pct{index="0"}                                    <0..100>
rt_agent_gpu_temp_c{index="0"}                                      <int>
rt_agent_gpu_power_w{index="0"}                                     <int>
rt_agent_gpu_ecc_volatile_uncorrected_total{index="0"}              <counter>
rt_agent_gpu_nvlink_up{index="0",link="0"}                          <0|1>
rt_agent_gpu_nvlink_speed_gbps{index="0",link="0"}                  <int>
```

### Per-platform / per-model

```
rt_node_platform_up{platform="vllm"}                                  <0|1>
rt_node_model_loaded{platform="vllm",model="qwen3-vl:32b"}            <0|1>
rt_node_model_size_mb{platform="vllm",model="qwen3-vl:32b"}           <int>
rt_node_model_vram_used_mb{platform="vllm",model="qwen3-vl:32b"}      <int>
rt_node_model_queue_running{platform="vllm",model="qwen3-vl:32b"}     <int>
rt_node_model_queue_waiting{platform="vllm",model="qwen3-vl:32b"}     <int>
rt_node_model_kv_cache_gpu_usage_pct{...}                             <0..100>
rt_node_model_kv_cache_prefix_hit_rate{...}                           <0..1>
rt_node_model_ttft_seconds{...,quantile="0.5"}                        <float>
rt_node_model_ttft_seconds{...,quantile="0.99"}                       <float>
rt_node_model_tpot_seconds{...,quantile="0.5"}                        <float>
rt_node_model_requests_success_total{...}                             <counter>
rt_node_model_prompt_tokens_total{...}                                <counter>
rt_node_model_generation_tokens_total{...}                            <counter>
```

### Allocators

```
rt_agent_service_reserved_mb{name="gliner2-service"}    <float>
rt_agent_service_allocated_mb{name="gliner2-service"}   <float>
```

### Disk

```
rt_node_disk_used_pct{path="/"}            <0..100>
rt_node_disk_total_gb{path="/"}            <float>
```

### Mode and training

```
rt_node_mode{mode="idle"}            <0|1>
rt_node_mode{mode="inference"}       <0|1>
rt_node_mode{mode="training_mode"}   <0|1>

# Only when in training_mode:
rt_node_training_run_id_info{run_id="9b1f-..."}  1
rt_node_training_seconds_remaining               <int>
```

### RDMA (Linux DGX only)

```
rt_node_rdma_device_active{device="rocep1s0f0",port="1"}      <0|1>
rt_node_rdma_link_rate_gbps{device="rocep1s0f0",port="1"}     <int>
rt_node_rdma_xmit_bytes_total{...}                            <counter>
rt_node_rdma_rcv_bytes_total{...}                             <counter>
rt_node_rdma_symbol_errors_total{...}                         <counter>
rt_node_rdma_link_recovery_total{...}                         <counter>
rt_node_rdma_link_downed_total{...}                           <counter>
```

## Scrape recommendations

- 30s interval matches the case-manager's `/health` cache. Faster scraping
  is cheap (everything is cached internally) but adds little.
- For training jobs, set the dashboard step to 10s — the
  `rt_node_training_seconds_remaining` countdown is most useful at finer
  resolution.
- Use `rate(rt_node_model_prompt_tokens_total[1m])` for throughput.
