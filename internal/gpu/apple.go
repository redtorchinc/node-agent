//go:build darwin && arm64

package gpu

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
)

// AppleSilicon probes the M-series integrated GPU via system_profiler.
//
// There is no public macOS API for per-process VRAM accounting (unlike
// nvidia-smi compute-apps), so Processes is always empty. VRAMTotalMB is
// the full system RAM — unified memory means there is no separate VRAM
// pool. The caller (internal/health) sets memory.unified=true on this
// platform so the ranker knows to treat RAM pressure as GPU pressure.
type AppleSilicon struct{}

// NewAppleSilicon returns an Apple Silicon GPU probe.
func NewAppleSilicon() *AppleSilicon { return &AppleSilicon{} }

type spDisplays struct {
	Displays []struct {
		Name     string `json:"_name"`
		Model    string `json:"sppci_model"`
		VRAMShared string `json:"spdisplays_vram_shared"`
		Cores    string `json:"sppci_cores"`
	} `json:"SPDisplaysDataType"`
}

// Probe implements Probe.
func (AppleSilicon) Probe(ctx context.Context) ([]GPU, error) {
	cmd := exec.CommandContext(ctx, "system_profiler", "-json", "SPDisplaysDataType")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Return an empty slice rather than erroring — on an Apple Silicon box
		// without a functional system_profiler, /health is still useful.
		return []GPU{}, nil
	}
	var data spDisplays
	if err := json.Unmarshal(stdout.Bytes(), &data); err != nil {
		return []GPU{}, nil
	}
	gpus := make([]GPU, 0, len(data.Displays))
	for i, d := range data.Displays {
		name := d.Model
		if name == "" {
			name = d.Name
		}
		gpus = append(gpus, GPU{
			Index:       i,
			Name:        name,
			VRAMUnified: true,
			Processes:   []Process{},
		})
	}
	return gpus, nil
}
