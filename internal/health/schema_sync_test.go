package health

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// TestDegradedReasonsDocsInSync guards the cross-repo contract documented in
// CLAUDE.md and docs/degraded-reasons.md: every Reason* constant in
// degraded.go must appear in the markdown reference table, and vice versa.
//
// The backend's rank_nodes() is coded against the markdown — if a constant
// is added in code but not documented (or vice versa), the docs and the
// implementation drift silently and the backend's _HARD_REASONS /
// _SOFT_REASONS sets fall out of sync.
//
// This test reads docs/degraded-reasons.md from the repo (two levels up
// from internal/health) and asserts both directions.
func TestDegradedReasonsDocsInSync(t *testing.T) {
	const docPath = "../../docs/degraded-reasons.md"
	b, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v (run from repo root or internal/health)", docPath, err)
	}

	// Pull every backtick-quoted snake_case identifier out of the table rows.
	// The table format is "| `reason_name` | description |" — the leading
	// `^|` anchors to row starts and avoids picking up inline-code reasons in
	// prose paragraphs.
	re := regexp.MustCompile("(?m)^\\| `([a-z][a-z0-9_]+)`")
	matches := re.FindAllStringSubmatch(string(b), -1)
	if len(matches) == 0 {
		t.Fatalf("no reasons found in %s — regex probably broke", docPath)
	}
	documented := map[string]bool{}
	for _, m := range matches {
		documented[m[1]] = true
	}

	// constants is the authoritative list of Reason* values from degraded.go.
	// Adding a new Reason* constant requires adding the name here AND
	// documenting it in docs/degraded-reasons.md.
	constants := []string{
		// v0.1.x hard
		ReasonOllamaDown,
		ReasonSwapOver75pct,
		ReasonVRAMOver95pct,
		ReasonAgentStale,
		ReasonVRAMServiceCreepCritical,
		// v0.1.x soft
		ReasonSwapOver50pct,
		ReasonVRAMOver90pct,
		ReasonLoadAvgOver2xCores,
		ReasonOllamaRunnerStuck,
		ReasonVRAMServiceCreepWarn,
		// v0.2.0 hard
		ReasonDiskOver98pct,
		ReasonGPUECCUncorrected,
		ReasonVLLMRequiredDown,
		ReasonRDMAPortDown,
		ReasonRDMAPeermemMissing,
		ReasonRDMACollectorStale,
		ReasonTrainingInProgress,
		// v0.2.0 soft
		ReasonDiskOver90pct,
		ReasonClockSkewHigh,
		ReasonCPUThermalThrottling,
		ReasonGPUThermalThrottling,
		ReasonGPUPowerThrottling,
		ReasonVLLMDown,
		ReasonRDMAErrorsGrowing,
		ReasonRDMAPFCStorm,
		ReasonRDMALinkDegraded,
	}
	inCode := map[string]bool{}
	for _, c := range constants {
		inCode[c] = true
	}

	// 1) Every constant must be documented.
	var missingDoc []string
	for _, c := range constants {
		if !documented[c] {
			missingDoc = append(missingDoc, c)
		}
	}
	if len(missingDoc) > 0 {
		sort.Strings(missingDoc)
		t.Errorf("constants missing from docs/degraded-reasons.md: %v\n"+
			"Add a row to the appropriate table (hard/soft × v0.1.x/v0.2.0).", missingDoc)
	}

	// 2) Every documented reason must have a constant.
	var missingConst []string
	for name := range documented {
		if !inCode[name] {
			missingConst = append(missingConst, name)
		}
	}
	if len(missingConst) > 0 {
		sort.Strings(missingConst)
		t.Errorf("reasons documented in docs/degraded-reasons.md without a "+
			"matching Reason* constant in degraded.go: %v\n"+
			"Either add the constant or remove the table row.", missingConst)
	}
}
