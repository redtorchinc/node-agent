// Package serving names what holds each GPU (v0.3.8, issue #50).
//
// Starting from the GPU compute processes nvidia-smi reports, it walks each
// process up to the service that owns it, finds that service's listening TCP
// ports, and identifies the serving platform behind them. A vLLM worker
// (`VLLM::Worker`) leads to its `vllm serve` API process and port without
// any configuration, and non-inference GPU users (an embedding service, a
// training run) are named too.
//
// Probing is read-only, loopback-first and deliberately narrow. Only the
// service root's ports are probed (the outermost process in the chain that
// owns a listener; for vLLM that is the API server, never the workers, whose
// listeners are NCCL/ZMQ). Only kinds known to speak HTTP are probed, or a
// port the process names itself with --port. Training (torchrun / torch.
// distributed / deepspeed) and Ray processes are reported but never probed:
// their listeners are rendezvous stores and gRPC, not HTTP.
//
// Multi-node serving: a Ray leaf's GPU is held by Ray workers under raylet,
// with no API and no model on any local command line (vLLM does not keep
// weight files mapped after loading). AnnotateRay marks those services
// role "leaf" and takes the model names from the head's agent. On a Ray
// head the GPU workers also sit under raylet, not under the API process, so
// serving API processes are found by a host scan as well and their models
// are attached to the head's Ray service. A non-Ray leaf (`vllm serve
// --headless`) is marked role "leaf" with the model from its command line.
package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redtorchinc/node-agent/internal/gpu"
	"github.com/redtorchinc/node-agent/internal/netown"
)

// Service is one /health.gpu_services[] entry: a process tree that holds GPU
// memory, grouped at its root.
type Service struct {
	PID         int32  `json:"pid"`                    // service root
	Name        string `json:"name"`                   // root process name
	CmdlineHead string `json:"cmdline_head,omitempty"` // redacted, capped
	Unit        string `json:"unit,omitempty"`         // systemd unit (Linux)
	ContainerID string `json:"container_id,omitempty"`
	Kind        string `json:"kind"` // vllm | sglang | ollama | llama.cpp | triton | tgi | http | torch_distributed | ray | process
	// Role is "head" or "leaf" for multi-node serving: a Ray member (from
	// /health.ray) or a vLLM `--headless` leaf. Empty for a standalone serve.
	Role        string   `json:"role,omitempty"`
	Head        string   `json:"head,omitempty"` // a Ray leaf's head host (from ray.gcs_address)
	ListenPorts []uint32 `json:"listen_ports,omitempty"`
	Endpoint    string   `json:"endpoint,omitempty"` // base URL of the HTTP API that answered
	// Models: ids from /v1/models; else the model on the serve's command
	// line; for a Ray leaf, what its head serves.
	Models     []string `json:"models,omitempty"`
	GPUPIDs    []int32  `json:"gpu_pids"`
	VRAMUsedMB int64    `json:"vram_used_mb"`
}

// Proc is the per-PID view discovery needs. Test seam.
type Proc struct {
	PPID    int32
	Name    string
	Cmdline []string
	Created int64 // process start, unix ms — distinguishes a reused PID
}

// Listener is one LISTEN TCP socket.
type Listener struct {
	Addr string
	Port uint32
}

// Deps is the OS access discovery needs; System() is the real one.
type Deps interface {
	Proc(pid int32) (Proc, bool)
	Listeners(pid int32) []Listener
	Unit(pid int32) (unit, containerID string)
	// ServingCandidates lists serving API processes (vllm / sglang / tgi /
	// triton / llama.cpp, not worker titles) whether or not they hold GPU
	// memory: a Ray head's vLLM API process holds none.
	ServingCandidates() []int32
}

const (
	maxDepth   = 8
	maxProbes  = 4
	cmdlineCap = 240
)

// stopNames end the walk up the process tree: init systems, shells,
// multiplexers, privilege wrappers and container plumbing never own a GPU
// service, and the service root must not be pinned to them.
var stopPrefixes = []string{
	"systemd", "init", "containerd", "dockerd", "docker-init", "conmon", "runc",
	"tini", "sshd", "bash", "sh", "dash", "zsh", "fish", "tmux", "screen",
	"sudo", "su", "login", "cron", "(sd-pam)", "supervisord", "s6-supervise",
	"runsv",
}

// isWorkerTitle: engine/worker processes retitle themselves; their own
// listeners are NCCL / ZMQ / Ray plumbing, never the service's API.
func isWorkerTitle(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "vllm::") || strings.HasPrefix(n, "ray::") || strings.HasPrefix(n, "sglang::")
}

// servingKinds are the API servers the host scan seeds discovery with.
var servingKinds = map[string]bool{"vllm": true, "sglang": true, "llama.cpp": true, "triton": true, "tgi": true}

// CmdlineModel returns the model a serving command line names: the
// --served-model-name, else --model / --model-path, else `serve <model>`.
// Local paths are cut to their last element.
func CmdlineModel(argv []string) string {
	val := func(flags ...string) string {
		for i, a := range argv {
			for _, f := range flags {
				if a == f && i+1 < len(argv) {
					return argv[i+1]
				}
				if strings.HasPrefix(a, f+"=") {
					return strings.TrimPrefix(a, f+"=")
				}
			}
		}
		return ""
	}
	if v := val("--served-model-name"); v != "" {
		return v
	}
	v := val("--model", "--model-path")
	if v == "" {
		for i, a := range argv {
			if a == "serve" && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				v = argv[i+1]
				break
			}
		}
	}
	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "~") {
		v = path.Base(strings.TrimRight(v, "/"))
	}
	return v
}

func isStop(name string) bool {
	n := strings.ToLower(name)
	for _, p := range stopPrefixes {
		if n == p || (len(p) > 2 && strings.HasPrefix(n, p)) {
			return true
		}
	}
	return false
}

// Classify names the kind of service from its process name and argv.
func Classify(name string, argv []string) string {
	n := strings.ToLower(name)
	lc := strings.ToLower(strings.Join(argv, " "))
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(lc, x) {
				return true
			}
		}
		return false
	}
	switch {
	case n == "vllm" || strings.HasPrefix(n, "vllm::") || has("vllm serve", "vllm.entrypoints", "/vllm serve"):
		return "vllm"
	case has("sglang.launch_server", "sglang serve", "sglang::"):
		return "sglang"
	case n == "ollama" || has("ollama serve", "ollama runner"):
		return "ollama"
	case has("llama-server"):
		return "llama.cpp"
	case has("tritonserver"):
		return "triton"
	case has("text-generation-launcher", "text-generation-router", "text-generation-server"):
		return "tgi"
	case has("torchrun", "torch.distributed", "deepspeed", "accelerate launch", "accelerate-launch"):
		return "torch_distributed"
	case strings.HasPrefix(n, "ray::") || has("raylet", "gcs_server", "ray::"):
		return "ray"
	case has("uvicorn", "gunicorn", "hypercorn", "granian"):
		return "http"
	}
	return "process"
}

// probeable kinds speak HTTP on their listeners.
var probeable = map[string]bool{
	"vllm": true, "sglang": true, "ollama": true, "llama.cpp": true,
	"triton": true, "tgi": true, "http": true,
}

// CmdlinePort returns the port a process names for itself (--port N,
// --port=N, --http-port N), or 0.
func CmdlinePort(argv []string) uint32 {
	for i, a := range argv {
		for _, flag := range []string{"--port", "--http-port"} {
			v := ""
			if a == flag && i+1 < len(argv) {
				v = argv[i+1]
			} else if strings.HasPrefix(a, flag+"=") {
				v = strings.TrimPrefix(a, flag+"=")
			}
			if v != "" {
				if p, err := strconv.ParseUint(v, 10, 16); err == nil {
					return uint32(p)
				}
			}
		}
	}
	return 0
}

// Discoverer caches discovery: the tree walk and socket lookup run at most
// every ttl, and each (service, port) is probed at most every probeTTL.
type Discoverer struct {
	deps     Deps
	http     *http.Client
	now      func() time.Time
	ttl      time.Duration
	probeTTL time.Duration

	mu     sync.Mutex
	key    string
	at     time.Time
	last   []Service
	probed map[string]probeResult
	heads  map[string]headModels
}

type headModels struct {
	at     time.Time
	models []string
}

type probeResult struct {
	at       time.Time
	endpoint string
	kind     string
	models   []string
}

// New returns a Discoverer over the real OS.
func New() *Discoverer { return NewWithDeps(System()) }

// NewWithDeps is the test constructor.
func NewWithDeps(d Deps) *Discoverer {
	return &Discoverer{
		deps:     d,
		http:     &http.Client{Timeout: time.Second},
		now:      time.Now,
		ttl:      30 * time.Second,
		probeTTL: 5 * time.Minute,
		probed:   map[string]probeResult{},
		heads:    map[string]headModels{},
	}
}

// Services returns the GPU services for the given GPU processes, from cache
// when the same PID set was resolved within ttl.
func (x *Discoverer) Services(ctx context.Context, gpus []gpu.GPU) []Service {
	procs := map[int32]int64{}
	for _, g := range gpus {
		for _, p := range g.Processes {
			if p.PID > 0 {
				procs[int32(p.PID)] += p.VRAMUsedMB
			}
		}
	}
	key := pidKey(procs)
	x.mu.Lock()
	if key == x.key && x.now().Sub(x.at) < x.ttl {
		out := append([]Service(nil), x.last...)
		x.mu.Unlock()
		return out
	}
	x.mu.Unlock()

	out := x.resolve(ctx, procs)

	x.mu.Lock()
	x.key, x.at, x.last = key, x.now(), out
	x.mu.Unlock()
	// A copy: callers (AnnotateRay) edit entries in place.
	return append([]Service(nil), out...)
}

// VLLMEndpoints lists the base URLs of the vLLM serves found by the last
// discovery. The vLLM platform detector probes them like configured ones.
func (x *Discoverer) VLLMEndpoints() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []string
	for _, s := range x.last {
		if s.Kind == "vllm" && s.Endpoint != "" {
			out = append(out, s.Endpoint)
		}
	}
	return out
}

func pidKey(procs map[int32]int64) string {
	ids := make([]int, 0, len(procs))
	for p := range procs {
		ids = append(ids, int(p))
	}
	sort.Ints(ids)
	return fmt.Sprint(ids)
}

func (x *Discoverer) resolve(ctx context.Context, procs map[int32]int64) []Service {
	listeners := map[int32][]Listener{}
	listen := func(pid int32) []Listener {
		if l, ok := listeners[pid]; ok {
			return l
		}
		l := x.deps.Listeners(pid)
		listeners[pid] = l
		return l
	}

	byRoot := map[int32]*Service{}
	var order []int32
	gpuPIDs := make([]int32, 0, len(procs))
	for p := range procs {
		gpuPIDs = append(gpuPIDs, p)
	}
	sort.Slice(gpuPIDs, func(i, j int) bool { return gpuPIDs[i] < gpuPIDs[j] })

	service := func(pid int32) *Service {
		root, rootProc, ok := x.root(pid, listen)
		if !ok {
			return nil
		}
		if s := byRoot[root]; s != nil {
			return s
		}
		s := &Service{
			PID:         root,
			Name:        rootProc.Name,
			CmdlineHead: netown.RedactCmdline(rootProc.Cmdline, cmdlineCap),
			Kind:        Classify(rootProc.Name, rootProc.Cmdline),
			GPUPIDs:     []int32{},
		}
		s.Unit, s.ContainerID = x.deps.Unit(root)
		for _, l := range listen(root) {
			s.ListenPorts = appendUnique(s.ListenPorts, l.Port)
		}
		sort.Slice(s.ListenPorts, func(i, j int) bool { return s.ListenPorts[i] < s.ListenPorts[j] })
		x.probe(ctx, s, rootProc, listen(root))
		if servingKinds[s.Kind] {
			if len(s.Models) == 0 {
				if m := CmdlineModel(rootProc.Cmdline); m != "" {
					s.Models = []string{m}
				}
			}
			for _, a := range rootProc.Cmdline {
				if a == "--headless" {
					s.Role = "leaf"
				}
			}
		}
		byRoot[root] = s
		order = append(order, root)
		return s
	}

	for _, pid := range gpuPIDs {
		if s := service(pid); s != nil {
			s.GPUPIDs = append(s.GPUPIDs, pid)
			s.VRAMUsedMB += procs[pid]
		}
	}
	// Serving API processes that hold no GPU memory themselves (a Ray head's
	// vLLM API) are listed too, with gpu_pids [].
	cands := x.deps.ServingCandidates()
	sort.Slice(cands, func(i, j int) bool { return cands[i] < cands[j] })
	for _, pid := range cands {
		service(pid)
	}

	out := make([]Service, 0, len(order))
	for _, r := range order {
		out = append(out, *byRoot[r])
	}
	return out
}

// root walks from pid up the parent chain (stopping at init, shells,
// supervisors and container plumbing) and picks the service root:
//  1. the outermost process listening on the port its own argv names
//     (--port N) — a serving API, even under a wrapper like `uv run`;
//  2. else the outermost non-worker process owning any listener — e.g. a
//     torchrun rendezvous store, Ray's raylet;
//  3. else the innermost process that is not a worker title — the serve
//     itself, not its launcher: a `vllm serve --headless` leaf (whose only
//     listeners are its workers' NCCL ports) under `uv run`, or a training
//     script.
func (x *Discoverer) root(pid int32, listen func(int32) []Listener) (int32, Proc, bool) {
	p, ok := x.deps.Proc(pid)
	if !ok {
		return 0, Proc{}, false
	}
	type node struct {
		pid int32
		p   Proc
	}
	chain := []node{{pid, p}}
	for len(chain) < maxDepth {
		cur := chain[len(chain)-1]
		if cur.p.PPID <= 1 {
			break
		}
		parent, ok := x.deps.Proc(cur.p.PPID)
		if !ok || isStop(parent.Name) {
			break
		}
		chain = append(chain, node{cur.p.PPID, parent})
	}
	pick := func(match func(node) bool) (node, bool) {
		for i := len(chain) - 1; i >= 0; i-- {
			if match(chain[i]) {
				return chain[i], true
			}
		}
		return node{}, false
	}
	if n, ok := pick(func(n node) bool {
		own := CmdlinePort(n.p.Cmdline)
		for _, l := range listen(n.pid) {
			if own != 0 && l.Port == own {
				return true
			}
		}
		return false
	}); ok {
		return n.pid, n.p, true
	}
	if n, ok := pick(func(n node) bool { return !isWorkerTitle(n.p.Name) && len(listen(n.pid)) > 0 }); ok {
		return n.pid, n.p, true
	}
	for _, n := range chain {
		if !isWorkerTitle(n.p.Name) {
			return n.pid, n.p, true
		}
	}
	top := chain[len(chain)-1]
	return top.pid, top.p, true
}

// AnnotateRay marks the Ray-owned services with this node's Ray role and,
// when they have no model of their own, the model the Ray cluster serves:
// on the head, the models of the local serving APIs; on a leaf, what the
// head's agent reports (GET http://<head>:<agentPort>/health, cached 60s).
func (x *Discoverer) AnnotateRay(ctx context.Context, svcs []Service, rayRole, gcsAddress string, agentPort int) {
	role := "leaf"
	if rayRole == "head" {
		role = "head"
	}
	var local []string
	for _, s := range svcs {
		if servingKinds[s.Kind] && s.Endpoint != "" {
			local = append(local, s.Models...)
		}
	}
	head, _, err := net.SplitHostPort(gcsAddress)
	if err != nil {
		head = gcsAddress
	}
	for i := range svcs {
		if svcs[i].Kind != "ray" {
			continue
		}
		svcs[i].Role = role
		if role == "leaf" {
			svcs[i].Head = head
		}
		if len(svcs[i].Models) > 0 {
			continue
		}
		if role == "head" {
			svcs[i].Models = local
		} else if head != "" {
			svcs[i].Models = x.headModels(ctx, head, agentPort)
		}
	}
}

func (x *Discoverer) headModels(ctx context.Context, head string, agentPort int) []string {
	x.mu.Lock()
	h, ok := x.heads[head]
	x.mu.Unlock()
	if ok && x.now().Sub(h.at) < time.Minute {
		return h.models
	}
	var models []string
	base := "http://" + hostFor(head) + ":" + strconv.Itoa(agentPort)
	if body, ok := x.get(ctx, base+"/health"); ok {
		var d struct {
			GPUServices []Service `json:"gpu_services"`
			Platforms   struct {
				VLLM struct {
					Models []struct {
						Name string `json:"name"`
					} `json:"models"`
				} `json:"vllm"`
			} `json:"platforms"`
		}
		if json.Unmarshal(body, &d) == nil {
			for _, s := range d.GPUServices {
				if servingKinds[s.Kind] && s.Endpoint != "" {
					models = append(models, s.Models...)
				}
			}
			if len(models) == 0 { // head agent < 0.3.8: its vLLM platform block
				for _, m := range d.Platforms.VLLM.Models {
					models = append(models, m.Name)
				}
			}
		}
	}
	x.mu.Lock()
	x.heads[head] = headModels{at: x.now(), models: models}
	x.mu.Unlock()
	return models
}

// probe fills Endpoint/Models (and refines Kind) for a service whose kind
// speaks HTTP, or whose root names its own --port.
func (x *Discoverer) probe(ctx context.Context, s *Service, root Proc, ls []Listener) {
	own := CmdlinePort(root.Cmdline)
	var cand []Listener
	for _, l := range ls {
		if own != 0 && l.Port == own {
			cand = []Listener{l}
			break
		}
	}
	if cand == nil {
		if !probeable[s.Kind] {
			return
		}
		cand = ls
	}
	if len(cand) > maxProbes {
		cand = cand[:maxProbes]
	}
	for _, l := range cand {
		key := fmt.Sprintf("%d/%d/%d", s.PID, root.Created, l.Port)
		x.mu.Lock()
		r, ok := x.probed[key]
		x.mu.Unlock()
		if !ok || x.now().Sub(r.at) >= x.probeTTL {
			r = x.probeOne(ctx, s.Kind, l)
			r.at = x.now()
			x.mu.Lock()
			x.probed[key] = r
			x.mu.Unlock()
		}
		if r.endpoint != "" {
			s.Endpoint, s.Models = r.endpoint, r.models
			if r.kind != "" {
				s.Kind = r.kind
			}
			return
		}
	}
}

func (x *Discoverer) probeOne(ctx context.Context, kind string, l Listener) probeResult {
	base := "http://" + hostFor(l.Addr) + ":" + strconv.FormatUint(uint64(l.Port), 10)
	if body, ok := x.get(ctx, base+"/v1/models"); ok {
		var m struct {
			Data []struct {
				ID      string `json:"id"`
				OwnedBy string `json:"owned_by"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &m) == nil && m.Data != nil {
			r := probeResult{endpoint: base}
			for _, d := range m.Data {
				r.models = append(r.models, d.ID)
				if (kind == "http" || kind == "process") && (d.OwnedBy == "vllm" || d.OwnedBy == "sglang") {
					r.kind = d.OwnedBy
				}
			}
			return r
		}
	}
	if kind == "ollama" {
		if _, ok := x.get(ctx, base+"/api/version"); ok {
			return probeResult{endpoint: base}
		}
		return probeResult{}
	}
	if _, ok := x.get(ctx, base+"/health"); ok {
		return probeResult{endpoint: base}
	}
	return probeResult{}
}

func (x *Discoverer) get(ctx context.Context, url string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	resp, err := x.http.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, err == nil
}

// hostFor maps a listen address to the address to dial: wildcard and
// loopback binds are reached on loopback, anything else on its own address.
func hostFor(addr string) string {
	switch addr {
	case "", "0.0.0.0", "*", "::", "127.0.0.1":
		return "127.0.0.1"
	case "::1":
		return "[::1]"
	}
	if strings.Contains(addr, ":") {
		return "[" + addr + "]"
	}
	return addr
}

func appendUnique(s []uint32, v uint32) []uint32 {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
