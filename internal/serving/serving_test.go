package serving

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/redtorchinc/node-agent/internal/gpu"
)

type fakeDeps struct {
	procs     map[int32]Proc
	listeners map[int32][]Listener
	units     map[int32]string
	serving   []int32
}

func (f fakeDeps) ServingCandidates() []int32 { return f.serving }

func (f fakeDeps) Proc(pid int32) (Proc, bool)     { p, ok := f.procs[pid]; return p, ok }
func (f fakeDeps) Listeners(pid int32) []Listener  { return f.listeners[pid] }
func (f fakeDeps) Unit(pid int32) (string, string) { return f.units[pid], "" }

// server starts an HTTP server on loopback and returns its port and a hit counter.
func server(t *testing.T, h http.HandlerFunc) (uint32, *int64) {
	t.Helper()
	var hits int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	_, ps, _ := net.SplitHostPort(s.Listener.Addr().String())
	p, _ := strconv.Atoi(ps)
	return uint32(p), &hits
}

func gpus(procs ...gpu.Process) []gpu.GPU { return []gpu.GPU{{Index: 0, Processes: procs}} }

func TestVLLMWorkerResolvesToAPIServer(t *testing.T) {
	port, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"base-30b","owned_by":"vllm"},{"id":"student-lora","owned_by":"vllm"}]}`))
			return
		}
		http.NotFound(w, r)
	})
	// The worker holds NCCL/ZMQ listeners; they must never be probed.
	ncclPort, ncclHits := server(t, func(w http.ResponseWriter, r *http.Request) {})
	d := fakeDeps{
		procs: map[int32]Proc{
			300: {PPID: 200, Name: "VLLM::Worker", Cmdline: []string{"VLLM::Worker"}},
			200: {PPID: 100, Name: "VLLM::EngineCore", Cmdline: []string{"VLLM::EngineCore"}},
			100: {PPID: 1, Name: "vllm", Cmdline: []string{"/opt/v/bin/python3", "/opt/v/bin/vllm", "serve", "/m", "--port", strconv.Itoa(int(port))}},
		},
		listeners: map[int32][]Listener{
			300: {{Addr: "10.0.0.5", Port: ncclPort}},
			100: {{Addr: "0.0.0.0", Port: port}},
		},
		units: map[int32]string{100: "rt-link-student-shadow.service"},
	}
	got := NewWithDeps(d).Services(context.Background(), gpus(gpu.Process{PID: 300, VRAMUsedMB: 41616}))
	if len(got) != 1 {
		t.Fatalf("want 1 service, got %+v", got)
	}
	s := got[0]
	if s.PID != 100 || s.Kind != "vllm" || s.Unit != "rt-link-student-shadow.service" {
		t.Errorf("root/kind/unit wrong: %+v", s)
	}
	if s.Endpoint != "http://127.0.0.1:"+strconv.Itoa(int(port)) || len(s.Models) != 2 {
		t.Errorf("endpoint/models wrong: %+v", s)
	}
	if s.VRAMUsedMB != 41616 || len(s.GPUPIDs) != 1 || s.GPUPIDs[0] != 300 {
		t.Errorf("gpu attribution wrong: %+v", s)
	}
	if atomic.LoadInt64(ncclHits) != 0 {
		t.Errorf("worker listener was probed %d times", *ncclHits)
	}
}

func TestTrainingRendezvousIsNeverProbed(t *testing.T) {
	storePort, storeHits := server(t, func(w http.ResponseWriter, r *http.Request) {})
	d := fakeDeps{
		procs: map[int32]Proc{
			20: {PPID: 10, Name: "python", Cmdline: []string{"python", "train.py"}},
			10: {PPID: 5, Name: "torchrun", Cmdline: []string{"/usr/bin/python", "/usr/bin/torchrun", "--nproc-per-node", "1", "train.py"}},
			5:  {PPID: 1, Name: "bash", Cmdline: []string{"bash"}},
		},
		listeners: map[int32][]Listener{10: {{Addr: "0.0.0.0", Port: storePort}}},
	}
	got := NewWithDeps(d).Services(context.Background(), gpus(gpu.Process{PID: 20, VRAMUsedMB: 9000}))
	if len(got) != 1 || got[0].PID != 10 || got[0].Kind != "torch_distributed" || got[0].Endpoint != "" {
		t.Fatalf("training service wrong: %+v", got)
	}
	if atomic.LoadInt64(storeHits) != 0 {
		t.Errorf("rendezvous store was probed %d times", *storeHits)
	}
}

func TestHTTPServiceAnswersHealth(t *testing.T) {
	port, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.NotFound(w, r)
	})
	d := fakeDeps{
		procs: map[int32]Proc{
			40: {PPID: 1, Name: "python", Cmdline: []string{"python", "-m", "uvicorn", "service.main:app", "--port", strconv.Itoa(int(port))}},
		},
		listeners: map[int32][]Listener{40: {{Addr: "0.0.0.0", Port: port}}},
	}
	got := NewWithDeps(d).Services(context.Background(), gpus(gpu.Process{PID: 40, VRAMUsedMB: 2586}))
	if len(got) != 1 || got[0].Kind != "http" || got[0].Endpoint == "" || got[0].PID != 40 {
		t.Fatalf("http service wrong: %+v", got)
	}
}

func TestWorkersGroupUnderOneService(t *testing.T) {
	d := fakeDeps{
		procs: map[int32]Proc{
			61: {PPID: 60, Name: "VLLM::Worker_TP0", Cmdline: []string{"VLLM::Worker_TP0"}},
			62: {PPID: 60, Name: "VLLM::Worker_TP1", Cmdline: []string{"VLLM::Worker_TP1"}},
			60: {PPID: 1, Name: "vllm", Cmdline: []string{"vllm", "serve", "/m"}},
		},
	}
	got := NewWithDeps(d).Services(context.Background(), gpus(
		gpu.Process{PID: 61, VRAMUsedMB: 100}, gpu.Process{PID: 62, VRAMUsedMB: 50}))
	if len(got) != 1 || got[0].PID != 60 || got[0].VRAMUsedMB != 150 || len(got[0].GPUPIDs) != 2 {
		t.Fatalf("grouping wrong: %+v", got)
	}
	if got[0].Kind != "vllm" || got[0].Endpoint != "" {
		t.Errorf("no listener: kind/endpoint wrong: %+v", got[0])
	}
}

func TestVLLMEndpointsFromLastDiscovery(t *testing.T) {
	port, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m","owned_by":"vllm"}]}`))
	})
	d := fakeDeps{
		procs:     map[int32]Proc{7: {PPID: 1, Name: "vllm", Cmdline: []string{"vllm", "serve", "m"}}},
		listeners: map[int32][]Listener{7: {{Addr: "127.0.0.1", Port: port}}},
	}
	x := NewWithDeps(d)
	x.Services(context.Background(), gpus(gpu.Process{PID: 7}))
	eps := x.VLLMEndpoints()
	if len(eps) != 1 || eps[0] != "http://127.0.0.1:"+strconv.Itoa(int(port)) {
		t.Errorf("VLLMEndpoints = %v", eps)
	}
}

func TestClassifyAndCmdlinePort(t *testing.T) {
	cases := map[string]string{
		"vllm|vllm serve /m":                    "vllm",
		"python|python -m sglang.launch_server": "sglang",
		"ollama|ollama runner --model x":        "ollama",
		"python|python -m uvicorn app:app":      "http",
		"python|python train.py":                "process",
		"raylet|raylet --gcs-address x":         "ray",
	}
	for in, want := range cases {
		var name, cmd string
		for i, c := range in {
			if c == '|' {
				name, cmd = in[:i], in[i+1:]
				break
			}
		}
		if got := Classify(name, []string{cmd}); got != want {
			t.Errorf("Classify(%q, %q) = %q, want %q", name, cmd, got, want)
		}
	}
	if p := CmdlinePort([]string{"vllm", "serve", "--port=8102"}); p != 8102 {
		t.Errorf("--port= form: %d", p)
	}
	if p := CmdlinePort([]string{"tritonserver", "--http-port", "8000"}); p != 8000 {
		t.Errorf("--http-port form: %d", p)
	}
}

// A Ray leaf: the GPU is held by a Ray worker under raylet, with no API and
// no model on any local command line. It is marked ray / leaf, and the model
// comes from the head's agent.
func TestRayLeafTakesModelFromHeadAgent(t *testing.T) {
	headPort, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"gpu_services":[{"pid":1,"kind":"vllm","endpoint":"http://127.0.0.1:8000","models":["big-model-tp2"],"gpu_pids":[]},{"pid":2,"kind":"ray","gpu_pids":[3]}]}`))
			return
		}
		http.NotFound(w, r)
	})
	raylet, _ := server(t, func(w http.ResponseWriter, r *http.Request) {})
	d := fakeDeps{
		procs: map[int32]Proc{
			90: {PPID: 80, Name: "ray::RayWorkerWrapper", Cmdline: []string{"ray::RayWorkerWrapper.execute_method"}},
			80: {PPID: 1, Name: "raylet", Cmdline: []string{"/opt/ray/raylet", "--gcs-address=127.0.0.1:6379"}},
		},
		listeners: map[int32][]Listener{80: {{Addr: "0.0.0.0", Port: raylet}}, 90: {{Addr: "10.0.0.9", Port: 41000}}},
	}
	x := NewWithDeps(d)
	svcs := x.Services(context.Background(), gpus(gpu.Process{PID: 90, VRAMUsedMB: 60000}))
	x.AnnotateRay(context.Background(), svcs, "worker", "127.0.0.1:6379", int(headPort))
	if len(svcs) != 1 {
		t.Fatalf("want 1 service, got %+v", svcs)
	}
	s := svcs[0]
	if s.PID != 80 || s.Kind != "ray" || s.Role != "leaf" || s.Head != "127.0.0.1" || s.VRAMUsedMB != 60000 {
		t.Errorf("ray leaf wrong: %+v", s)
	}
	if len(s.Models) != 1 || s.Models[0] != "big-model-tp2" {
		t.Errorf("leaf models = %v, want the head's model", s.Models)
	}
	if s.Endpoint != "" {
		t.Errorf("raylet must not be probed: endpoint %q", s.Endpoint)
	}
}

// A Ray head: the GPU workers sit under raylet, the vLLM API process holds
// no GPU. The API is found by the host scan and its models label the head.
func TestRayHeadFindsAPIAndLabelsWorkers(t *testing.T) {
	api, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"big-model-tp2","owned_by":"vllm"}]}`))
			return
		}
		http.NotFound(w, r)
	})
	d := fakeDeps{
		procs: map[int32]Proc{
			91: {PPID: 81, Name: "ray::RayWorkerWrapper", Cmdline: []string{"ray::RayWorkerWrapper"}},
			81: {PPID: 1, Name: "raylet", Cmdline: []string{"raylet"}},
			70: {PPID: 1, Name: "vllm", Cmdline: []string{"vllm", "serve", "/models/big", "--port", strconv.Itoa(int(api))}},
		},
		listeners: map[int32][]Listener{81: {{Addr: "0.0.0.0", Port: 1}}, 70: {{Addr: "0.0.0.0", Port: api}}},
		serving:   []int32{70},
	}
	x := NewWithDeps(d)
	svcs := x.Services(context.Background(), gpus(gpu.Process{PID: 91, VRAMUsedMB: 60000}))
	x.AnnotateRay(context.Background(), svcs, "head", "10.0.0.1:6379", 1)
	var rayS, apiS *Service
	for i := range svcs {
		switch svcs[i].PID {
		case 81:
			rayS = &svcs[i]
		case 70:
			apiS = &svcs[i]
		}
	}
	if apiS == nil || apiS.Kind != "vllm" || apiS.Endpoint == "" || len(apiS.GPUPIDs) != 0 {
		t.Fatalf("API front-end wrong: %+v", svcs)
	}
	if rayS == nil || rayS.Role != "head" || len(rayS.Models) != 1 || rayS.Models[0] != "big-model-tp2" {
		t.Errorf("ray head wrong: %+v", rayS)
	}
}

// A non-Ray multi-node leaf: `vllm serve --headless`, whose only listeners are
// its workers' NCCL ports. Root is the serve process, role leaf, model from argv.
func TestHeadlessLeafRoleAndModel(t *testing.T) {
	d := fakeDeps{
		procs: map[int32]Proc{
			51: {PPID: 50, Name: "VLLM::Worker_TP1", Cmdline: []string{"VLLM::Worker_TP1"}},
			50: {PPID: 49, Name: "vllm", Cmdline: []string{"vllm", "serve", "/models/Big-Model-FP8/", "--headless", "--tensor-parallel-size", "2"}},
			49: {PPID: 1, Name: "uv", Cmdline: []string{"uv", "run", "vllm", "serve"}},
		},
		listeners: map[int32][]Listener{51: {{Addr: "10.0.0.9", Port: 41001}}},
	}
	svcs := NewWithDeps(d).Services(context.Background(), gpus(gpu.Process{PID: 51, VRAMUsedMB: 50000}))
	if len(svcs) != 1 {
		t.Fatalf("want 1, got %+v", svcs)
	}
	s := svcs[0]
	if s.Kind != "vllm" || len(s.Models) != 1 {
		t.Fatalf("headless leaf wrong: %+v", s)
	}
	if s.PID != 50 || s.Role != "leaf" || s.Models[0] != "Big-Model-FP8" {
		t.Errorf("want the serve process (not its worker or uv wrapper), role leaf, its model: %+v", s)
	}
}

func TestCmdlineModel(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"vllm", "serve", "/home/u/models/Model-A/", "--port", "8000"}, "Model-A"},
		{[]string{"vllm", "serve", "org/model-b"}, "org/model-b"},
		{[]string{"python", "-m", "sglang.launch_server", "--model-path", "/m/Model-C"}, "Model-C"},
		{[]string{"vllm", "serve", "/m/x", "--served-model-name", "nice-name"}, "nice-name"},
		{[]string{"python", "-m", "uvicorn", "app:app"}, ""},
	}
	for _, c := range cases {
		if got := CmdlineModel(c.argv); got != c.want {
			t.Errorf("CmdlineModel(%v) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// Ollama's runner (a bundled llama-server on a private port) belongs to
// `ollama serve`; the service is kind ollama with the loaded models from /api/ps.
func TestOllamaRunnerGroupsUnderServe(t *testing.T) {
	api, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			_, _ = w.Write([]byte(`{"models":[{"name":"nomic-embed-text-v2-moe:latest"}]}`))
			return
		}
		http.NotFound(w, r)
	})
	runnerPort, runnerHits := server(t, func(w http.ResponseWriter, r *http.Request) {})
	d := fakeDeps{
		procs: map[int32]Proc{
			12: {PPID: 11, Name: "llama-server", Cmdline: []string{"/usr/local/lib/ollama/llama-server", "--model", "/blobs/sha256-ab", "--port", strconv.Itoa(int(runnerPort))}},
			11: {PPID: 1, Name: "ollama", Cmdline: []string{"/usr/local/bin/ollama", "serve"}},
		},
		listeners: map[int32][]Listener{
			12: {{Addr: "127.0.0.1", Port: runnerPort}},
			11: {{Addr: "0.0.0.0", Port: api}},
		},
		units:   map[int32]string{11: "ollama.service"},
		serving: []int32{12},
	}
	svcs := NewWithDeps(d).Services(context.Background(), gpus(gpu.Process{PID: 12, VRAMUsedMB: 776}))
	if len(svcs) != 1 {
		t.Fatalf("want 1 service, got %+v", svcs)
	}
	s := svcs[0]
	if s.PID != 11 || s.Kind != "ollama" || s.Unit != "ollama.service" || s.VRAMUsedMB != 776 {
		t.Errorf("ollama service wrong: %+v", s)
	}
	if len(s.Models) != 1 || s.Models[0] != "nomic-embed-text-v2-moe:latest" {
		t.Errorf("models = %v, want the loaded model from /api/ps", s.Models)
	}
	if atomic.LoadInt64(runnerHits) != 0 {
		t.Errorf("runner's private port was probed %d times", *runnerHits)
	}
}

// `docker run … vllm serve …` under a systemd unit is not a service of its
// own; its unit names the container serve it started.
func TestContainerServeTakesLauncherUnit(t *testing.T) {
	api, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"served-name","owned_by":"vllm"}]}`))
	})
	ctr := "33e118c446f6542df12fbef473a3c1d0c33e118c446f6542df12fbef473a3c1"
	d := fakeDeps{
		procs: map[int32]Proc{
			31: {PPID: 30, Name: "VLLM::Worker", Cmdline: []string{"VLLM::Worker"}},
			30: {PPID: 29, Name: "vllm", Cmdline: []string{"vllm", "serve", "/model", "--served-model-name", "served-name", "--port", strconv.Itoa(int(api))}},
			29: {PPID: 1, Name: "containerd-shim-runc-v2", Cmdline: []string{"containerd-shim-runc-v2"}},
			20: {PPID: 1, Name: "docker", Cmdline: []string{"docker", "run", "--rm", "--name", "rt-vllm-x", "img", "vllm", "serve", "/model", "--served-model-name", "served-name", "--port", strconv.Itoa(int(api))}},
		},
		listeners: map[int32][]Listener{30: {{Addr: "0.0.0.0", Port: api}}},
		units:     map[int32]string{30: "docker-" + ctr + ".scope", 20: "rt-vllm@x.service"},
		serving:   []int32{20, 30},
	}
	// fakeDeps.Unit returns no container id; set it via a wrapper.
	svcs := NewWithDeps(ctrDeps{d, map[int32]string{30: ctr}}).Services(context.Background(), gpus(gpu.Process{PID: 31, VRAMUsedMB: 90000}))
	if len(svcs) != 1 {
		t.Fatalf("launcher must not be its own service: %+v", svcs)
	}
	if s := svcs[0]; s.PID != 30 || s.Unit != "rt-vllm@x.service" || s.ContainerID != ctr {
		t.Errorf("container serve wrong: %+v", s)
	}
}

type ctrDeps struct {
	fakeDeps
	ctr map[int32]string
}

func (c ctrDeps) Unit(pid int32) (string, string) { return c.units[pid], c.ctr[pid] }

// llama.cpp distributed: llama-server --rpc is the head, rpc-server a leaf.
func TestLlamaCppRPCRoles(t *testing.T) {
	d := fakeDeps{
		procs: map[int32]Proc{
			41: {PPID: 1, Name: "rpc-server", Cmdline: []string{"/opt/llama/rpc-server", "-H", "0.0.0.0", "-p", "50052"}},
			42: {PPID: 1, Name: "llama-server", Cmdline: []string{"/opt/llama/llama-server", "-m", "/m/x.gguf", "--rpc", "10.0.0.2:50052"}},
		},
		serving: []int32{42},
	}
	svcs := NewWithDeps(d).Services(context.Background(), gpus(gpu.Process{PID: 41, VRAMUsedMB: 70000}))
	roles := map[int32]string{}
	for _, s := range svcs {
		if s.Kind != "llama.cpp" {
			t.Errorf("kind = %q for %d", s.Kind, s.PID)
		}
		roles[s.PID] = s.Role
	}
	if roles[41] != "leaf" || roles[42] != "head" {
		t.Errorf("roles = %v, want rpc-server leaf, llama-server head", roles)
	}
}
