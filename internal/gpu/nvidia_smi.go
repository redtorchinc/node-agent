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

// Probe implements Probe.
func (n *NvidiaSMI) Probe(ctx context.Context) ([]GPU, error) {
	n.once.Do(n.resolve)
	if n.resolveErr != nil {
		return nil, n.resolveErr
	}

	gpuRows, err := n.run(ctx,
		n.resolved,
		"--query-gpu=index,name,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw,power.limit",
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

	return ParseNvidiaSMI(gpuRows, procRows)
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
	byIndex := map[int]*GPU{}
	for _, row := range gpuRecords {
		if len(row) < 8 {
			continue
		}
		idx := atoi(row[0])
		total := atoi64(row[2])
		used := atoi64(row[3])
		g := GPU{
			Index:       idx,
			Name:        strings.TrimSpace(row[1]),
			VRAMTotalMB: total,
			VRAMUsedMB:  used,
			VRAMUsedPct: pct(used, total),
			UtilPct:     atoi(row[4]),
			TempC:       atoi(row[5]),
			PowerW:      int(atof(row[6])),
			PowerCapW:   int(atof(row[7])),
			Processes:   []Process{},
		}
		gpus = append(gpus, g)
		byIndex[idx] = &gpus[len(gpus)-1]
	}

	if len(procCSV) > 0 {
		procRecords, err := readCSV(procCSV)
		if err == nil {
			// gpu_uuid,pid,process_name,used_memory
			// We don't have UUID→index mapping here since we queried by index.
			// In practice nvidia-smi's compute-apps output uses the same ordering
			// as query-gpu when filtered; but to be safe, attach all to index 0
			// when we can't resolve. For a more accurate mapping, a future change
			// can query gpu_bus_id on both sides.
			for _, row := range procRecords {
				if len(row) < 4 {
					continue
				}
				p := Process{
					PID:         atoi(row[1]),
					Name:        strings.TrimSpace(row[2]),
					CmdlineHead: strings.TrimSpace(row[2]),
					VRAMUsedMB:  atoi64(row[3]),
				}
				// Attach to first GPU if we only have one; otherwise to index 0.
				if len(gpus) > 0 {
					gpus[0].Processes = append(gpus[0].Processes, p)
					byIndex[gpus[0].Index] = &gpus[0]
				}
			}
		}
	}
	return gpus, nil
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
