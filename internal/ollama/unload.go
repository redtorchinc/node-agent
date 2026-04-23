package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
)

// UnloadResult reports what /actions/unload-model actually freed.
type UnloadResult struct {
	Unloaded []string `json:"unloaded"`
}

// Unload frees a model from Ollama. Tries `ollama stop <model>` first
// (Ollama 0.5+); falls back to POST /api/generate {"model":..,"keep_alive":0},
// which drops the model from memory immediately on all Ollama versions.
//
// Idempotent: if the model isn't loaded, returns ({Unloaded:[]}, nil).
func (c *Client) Unload(ctx context.Context, model string) (UnloadResult, error) {
	// Check presence first so we can be cleanly idempotent.
	info := c.Probe(ctx)
	present := false
	for _, m := range info.Models {
		if m.Name == model {
			present = true
			break
		}
	}
	if !present {
		return UnloadResult{Unloaded: []string{}}, nil
	}

	// Try CLI first.
	if _, err := exec.LookPath("ollama"); err == nil {
		cmd := exec.CommandContext(ctx, "ollama", "stop", model)
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		if err := cmd.Run(); err == nil {
			return UnloadResult{Unloaded: []string{model}}, nil
		}
		// Known case: `stop` subcommand missing on pre-0.5. Fall through to HTTP.
	}

	// HTTP fallback: keep_alive=0 unloads immediately.
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"keep_alive": 0,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return UnloadResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return UnloadResult{}, fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return UnloadResult{}, fmt.Errorf("ollama /api/generate: http %d", resp.StatusCode)
	}
	return UnloadResult{Unloaded: []string{model}}, nil
}
