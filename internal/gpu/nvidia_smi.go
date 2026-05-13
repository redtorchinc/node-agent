package gpu

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// NvidiaSMI probes via the `nvidia-smi` CLI. Shell-out chosen deliberately
// over NVML bindings (no CGO; debuggable by hand; identical output across
// Linux, Windows, and Intel-Mac-with-eGPU).
type NvidiaSMI struct {
	// Exec is overridable for tests. When nil, exec.CommandContext is used.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

	once       sync.Once
	resolved   string
	resolveErr error
}

// NewNvidiaSMI returns a probe that uses the real binary.
func NewNvidiaSMI() *NvidiaSMI { return &NvidiaSMI{} }

// Available reports whether nvidia-smi can be located. Cached after first call.
func (n *NvidiaSMI) Available() bool {
	n.once.Do(n.resolve)
	return n.resolveErr == nil
}

func (n *NvidiaSMI) resolve() {
	// Standard PATH lookup first.
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		n.resolved = p
		return
	}
	// Windows common install locations.
	if runtime.GOOS == "windows" {
		for _, p := range []string{
			`C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
			`C:\Windows\System32\nvidia-smi.exe`,
		} {
			if _, err := exec.LookPath(p); err == nil {
				n.resolved = p
				return
			}
		}
	}
	n.resolveErr = errors.New("nvidia-smi not found")
}

// queryFields is the column list passed to --query-gpu. KEEP IN SYNC with
// the index constants below; ParseNvidiaSMI indexes by position.
//
// We deliberately query a long line in a single shell-out — one round-trip
// is cheaper than five even with longer parse time.
const queryFields = "index,uuid,name,driver_version,pci.bus_id," +
	"compute_cap," +
	"memory.total,memory.used," +
	"utilization.gpu,utilization.memory," +
	"temperature.gpu,temperature.memory," +
	"power.draw,power.limit," +
	"clocks.gr,clocks.mem,clocks.sm,clocks.max.gr," +
	"clocks_throttle_reasons.active," +
	"ecc.errors.uncorrected.volatile.total,ecc.errors.uncorrected.aggregate.total," +
	"fan.speed,persistence_mode,compute_mode,mig.mode.current"

// Field indices for the above list. Keep contiguous with queryFields.
const (
	fIndex = iota
	fUUID
	fName
	fDriver
	fPCIBus
	fComputeCap
	fMemTotal
	fMemUsed
	fUtilGPU
	fUtilMem
	fTempGPU
	fTempMem
	fPowerDraw
	fPowerLimit
	fClockGr
	fClockMem
	fClockSM
	fClockGrMax
	fThrottle
	fECCVolatile
	fECCAggregate
	fFanSpeed
	fPersistence
	fComputeMode
	fMIGMode
)

// Probe implements Probe.
func (n *NvidiaSMI) Probe(ctx context.Context) ([]GPU, error) {
	n.once.Do(n.resolve)
	if n.resolveErr != nil {
		return nil, n.resolveErr
	}

	gpuRows, err := n.run(ctx,
		n.resolved,
		"--query-gpu="+queryFields,
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi query-gpu: %w", err)
	}
	procRows, err := n.run(ctx,
		n.resolved,
		"--query-compute-apps=gpu_uuid,pid,process_name,used_memory",
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		// Compute-apps is optional (driver support varies). Don't fail the whole probe.
		procRows = nil
	}

	gpus, err := ParseNvidiaSMI(gpuRows, procRows)
	if err != nil {
		return gpus, err
	}

	// NVLink is queried separately because the output shape is text, not CSV.
	if nvOut, err := n.run(ctx, n.resolved, "nvlink", "--status"); err == nil {
		applyNVLink(gpus, nvOut)
	}

	// CUDA version: best-effort, single line text output.
	if out, err := n.run(ctx, n.resolved, "--query-gpu=cuda_version", "--format=csv,noheader,nounits"); err == nil {
		v := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		for i := range gpus {
			gpus[i].CUDAVersion = v
		}
	}

	return gpus, nil
}

func (n *NvidiaSMI) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if n.Exec != nil {
		return n.Exec(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// ParseNvidiaSMI parses the CSV output of the two --query-* calls into GPUs.
// Exposed for tests: parsing is a pure function, shell-out lives separately.
func ParseNvidiaSMI(gpuCSV, procCSV []byte) ([]GPU, error) {
	gpuRecords, err := readCSV(gpuCSV)
	if err != nil {
		return nil, fmt.Errorf("parse gpu csv: %w", err)
	}
	gpus := make([]GPU, 0, len(gpuRecords))
	byUUID := map[string]*GPU{}
	for _, row := range gpuRecords {
		// Tolerate rows shorter than the full field list — older driver
		// versions emit fewer columns. Treat missing trailing fields as
		// blank / zero values rather than rejecting the row.
		get := func(i int) string {
			if i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		idx := atoi(get(fIndex))
		memTotalRaw := get(fMemTotal)
		total := atoi64(memTotalRaw)
		used := atoi64(get(fMemUsed))
		// Unified-memory NVIDIA parts (GB10 Grace-Blackwell / DGX Spark) have
		// no discrete VRAM pool, so nvidia-smi reports memory.total as [N/A].
		// Flag the GPU so the health composer can derive a VRAM ceiling from
		// system memory; otherwise vram_over_*pct never fires on these nodes.
		unified := memTotalRaw == "[N/A]" || memTotalRaw == "N/A"
		g := GPU{
			Index:             idx,
			UUID:              get(fUUID),
			Name:              get(fName),
			DriverVersion:     get(fDriver),
			PCIBusID:          get(fPCIBus),
			ComputeCapability: get(fComputeCap),
			VRAMTotalMB:       total,
			VRAMUsedMB:        used,
			VRAMUsedPct:       pct(used, total),
			VRAMUnified:       unified,
			UtilPct:           atoi(get(fUtilGPU)),
			MemoryUtilPct:     atoi(get(fUtilMem)),
			TempC:             atoi(get(fTempGPU)),
			TempMemoryC:       atoi(get(fTempMem)),
			PowerW:            int(atof(get(fPowerDraw))),
			PowerCapW:         int(atof(get(fPowerLimit))),
			ClockGraphicsMHz:    atoi(get(fClockGr)),
			ClockMemoryMHz:      atoi(get(fClockMem)),
			ClockSMMHz:          atoi(get(fClockSM)),
			ClockGraphicsMaxMHz: atoi(get(fClockGrMax)),
			ThrottleReasons:     parseThrottleHex(get(fThrottle)),
			PersistenceMode:     get(fPersistence),
			ComputeMode:         get(fComputeMode),
			MIGMode:             get(fMIGMode),
			Processes:           []Process{},
		}
		if v := get(fECCVolatile); v != "" && v != "[N/A]" && v != "N/A" {
			n := atoi64(v)
			g.ECCVolatileUncorrected = &n
		}
		if v := get(fECCAggregate); v != "" && v != "[N/A]" && v != "N/A" {
			n := atoi64(v)
			g.ECCAggregateUncorrected = &n
		}
		if v := get(fFanSpeed); v != "" && v != "[N/A]" && v != "N/A" {
			n := atoi(v)
			g.FanPct = &n
		}
		gpus = append(gpus, g)
		if g.UUID != "" {
			byUUID[g.UUID] = &gpus[len(gpus)-1]
		}
	}

	if len(procCSV) > 0 {
		procRecords, err := readCSV(procCSV)
		if err == nil {
			// gpu_uuid,pid,process_name,used_memory
			for _, row := range procRecords {
				if len(row) < 4 {
					continue
				}
				p := Process{
					PID:         atoi(strings.TrimSpace(row[1])),
					Name:        strings.TrimSpace(row[2]),
					CmdlineHead: strings.TrimSpace(row[2]),
					VRAMUsedMB:  atoi64(strings.TrimSpace(row[3])),
				}
				uuid := strings.TrimSpace(row[0])
				if g, ok := byUUID[uuid]; ok {
					g.Processes = append(g.Processes, p)
				} else if len(gpus) > 0 {
					gpus[0].Processes = append(gpus[0].Processes, p)
				}
			}
		}
	}
	return gpus, nil
}

// parseThrottleHex turns nvidia-smi's hex bitmask
// (e.g. "0x0000000000000001") into a stable list of reason strings the
// degraded evaluator can match on. Unknown bits are surfaced as
// "unknown_0xNN" so an operator can grep for them.
//
// Bit definitions per NVML reference: GPU_IDLE=1, APP_CLOCKS_SETTING=2,
// SW_POWER_CAP=4, HW_SLOWDOWN=8, SYNC_BOOST=10, SW_THERMAL_SLOWDOWN=20,
// HW_THERMAL_SLOWDOWN=40, HW_POWER_BRAKE_SLOWDOWN=80, DISPLAY_CLOCK_SETTING=100.
func parseThrottleHex(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" || s == "0x0" || s == "0x00" {
		return nil
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil || v == 0 {
		return nil
	}
	type bit struct {
		mask uint64
		name string
	}
	bits := []bit{
		{0x0001, "GPU_IDLE"},
		{0x0002, "APP_CLOCKS_SETTING"},
		{0x0004, "SW_POWER_CAP"},
		{0x0008, "HW_SLOWDOWN"},
		{0x0010, "SYNC_BOOST"},
		{0x0020, "SW_THERMAL_SLOWDOWN"},
		{0x0040, "HW_THERMAL_SLOWDOWN"},
		{0x0080, "HW_POWER_BRAKE_SLOWDOWN"},
		{0x0100, "DISPLAY_CLOCK_SETTING"},
	}
	var out []string
	for _, b := range bits {
		if v&b.mask != 0 {
			out = append(out, b.name)
			v &^= b.mask
		}
	}
	if v != 0 {
		out = append(out, fmt.Sprintf("unknown_0x%x", v))
	}
	return out
}

// applyNVLink folds `nvidia-smi nvlink --status` output into each GPU's
// NVLink field. The output looks like:
//
//	GPU 0: NVIDIA H100 80GB HBM3 (UUID: GPU-...)
//	         Link 0: 25 GB/s
//	         Link 1: <inactive>
//	GPU 1: ...
//
// Some driver versions add a "Rx Throughput KBp" suffix per link; we only
// extract the link index and headline speed.
func applyNVLink(gpus []GPU, raw []byte) {
	var cur *GPU
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		// "GPU N: ..." marks a new section.
		if strings.HasPrefix(line, "GPU ") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) < 1 {
				cur = nil
				continue
			}
			idxStr := strings.TrimPrefix(strings.TrimSpace(parts[0]), "GPU ")
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				cur = nil
				continue
			}
			cur = nil
			for i := range gpus {
				if gpus[i].Index == idx {
					cur = &gpus[i]
					if cur.NVLink == nil {
						cur.NVLink = &NVLink{Supported: true, Links: []NVLinkLink{}}
					}
					break
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Link ") {
			continue
		}
		// Examples: "Link 0: 25 GB/s" or "Link 3: <inactive>"
		colon := strings.Index(trimmed, ":")
		if colon < 0 {
			continue
		}
		linkStr := strings.TrimPrefix(trimmed[:colon], "Link ")
		linkIdx, err := strconv.Atoi(strings.TrimSpace(linkStr))
		if err != nil {
			continue
		}
		rest := strings.TrimSpace(trimmed[colon+1:])
		ll := NVLinkLink{Link: linkIdx, State: "Down"}
		if !strings.Contains(strings.ToLower(rest), "inactive") {
			ll.State = "Up"
			// Parse leading "<num> GB/s"
			parts := strings.Fields(rest)
			if len(parts) >= 2 {
				if v, err := strconv.Atoi(parts[0]); err == nil {
					ll.SpeedGBPerS = v
				}
			}
		}
		cur.NVLink.Links = append(cur.NVLink.Links, ll)
	}
}

func readCSV(b []byte) ([][]string, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	r := csv.NewReader(bytes.NewReader(b))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	return r.ReadAll()
}

func atoi(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

func atoi64(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func atof(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func pct(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}
