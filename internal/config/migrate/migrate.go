// Package migrate detects config-schema drift and offers operators a
// commented preview of new keys on upgrade.
//
// Strategy: compare the existing /etc/rt-node-agent/config.yaml top-level
// keys against the embedded default YAML. Any top-level key present in the
// default but absent in the existing file is appended to the existing file
// (or rather, to a .new sibling) as fully-commented YAML. Existing values
// and the operator's comments are preserved verbatim — the file is never
// overwritten in place.
//
// This is the v0.1→v0.2 quiet-bug fix: previously, reinstall left a v0.1.x
// config in place with no indication that v0.2 had introduced new keys
// (platforms, services, rdma, training_mode). Operators had to read the
// docs to discover them; many didn't.
package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Result describes what Migrate did (or chose not to do).
type Result struct {
	// NewConfigPath is the path of the generated `.new` file. Empty when no
	// migration was needed (e.g. existing config already at CurrentVersion,
	// or no missing keys).
	NewConfigPath string

	// AddedKeys lists the top-level keys appended (commented) to the new file.
	AddedKeys []string

	// OldVersion is the existing config's config_version (0 if absent).
	OldVersion int

	// NewVersion is the version targeted by the current binary.
	NewVersion int

	// AlreadyCurrent is true when no work was needed.
	AlreadyCurrent bool
}

// CurrentVersion mirrors config.SchemaVersion. Pinned here to avoid an
// import cycle (config imports allocators which we don't need here).
const CurrentVersion = 2

// ErrBrokenYAML is returned by Migrate when the existing file cannot be
// parsed as YAML. Callers (typically the install path) should respond by
// calling ForceReset to back up the broken file and lay down defaults —
// see internal/service/bootstrap.go for the standard recovery flow.
var ErrBrokenYAML = errors.New("existing config.yaml is not valid YAML")

// Migrate compares the YAML at existingPath against defaultYAML and, if any
// top-level keys are missing from existingPath, writes a `<existingPath>.new`
// file with those keys appended as commented YAML. Existing values and
// comments are preserved.
//
// Migrate never overwrites existingPath. Operators promote the new file
// with `mv config.yaml{.new,}` (or `diff` first).
//
// If existingPath does not exist, returns (Result{AlreadyCurrent: true}, nil)
// since first-install writes a fresh config from the default elsewhere.
//
// If existingPath cannot be parsed as YAML, returns ErrBrokenYAML — callers
// should respond with ForceReset.
func Migrate(existingPath, defaultYAML string) (Result, error) {
	existingBytes, err := os.ReadFile(existingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{AlreadyCurrent: true, NewVersion: CurrentVersion}, nil
		}
		return Result{}, fmt.Errorf("read %s: %w", existingPath, err)
	}

	var existingDoc yaml.Node
	if err := yaml.Unmarshal(existingBytes, &existingDoc); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBrokenYAML, err)
	}

	var defaultDoc yaml.Node
	if err := yaml.Unmarshal([]byte(defaultYAML), &defaultDoc); err != nil {
		return Result{}, fmt.Errorf("parse default yaml: %w", err)
	}

	existingVersion := readVersion(&existingDoc)
	missing := missingTopKeys(&existingDoc, &defaultDoc)

	if existingVersion >= CurrentVersion && len(missing) == 0 {
		return Result{
			AlreadyCurrent: true,
			OldVersion:     existingVersion,
			NewVersion:     CurrentVersion,
		}, nil
	}

	var buf bytes.Buffer
	// Preserve existing content verbatim — comments, whitespace, value order.
	buf.Write(existingBytes)
	if len(existingBytes) > 0 && existingBytes[len(existingBytes)-1] != '\n' {
		buf.WriteByte('\n')
	}

	buf.WriteString("\n# " + strings.Repeat("─", 8) +
		" v0.2.0 additions (review and uncomment as needed) " +
		strings.Repeat("─", 8) + "\n")
	if existingVersion < CurrentVersion {
		buf.WriteString("# Bump config_version once you've reviewed the keys below.\n")
		buf.WriteString("# config_version: " + strconv.Itoa(CurrentVersion) + "\n")
	}

	for _, key := range missing {
		val := extractTopKey(&defaultDoc, key)
		if val == nil {
			continue
		}
		rendered, err := renderKey(key, val)
		if err != nil {
			return Result{}, fmt.Errorf("render %s: %w", key, err)
		}
		buf.WriteString("\n")
		for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
			if line == "" {
				buf.WriteString("#\n")
				continue
			}
			buf.WriteString("# " + line + "\n")
		}
	}

	newPath := existingPath + ".new"
	if err := os.WriteFile(newPath, buf.Bytes(), 0o644); err != nil {
		return Result{}, fmt.Errorf("write %s: %w", newPath, err)
	}

	return Result{
		NewConfigPath: newPath,
		AddedKeys:     missing,
		OldVersion:    existingVersion,
		NewVersion:    CurrentVersion,
	}, nil
}

// ForceReset is the recovery path for broken or unrecoverable configs.
// It backs up the existing file (if any) to `<path>.broken-<unix-ts>` and
// writes the embedded default to `path`. The token file and other agent
// state are untouched.
//
// Returns the backup path (empty string if there was no existing file)
// alongside any I/O error. Designed to be called by install paths and by
// `rt-node-agent config migrate-force`.
func ForceReset(path, defaultYAML string) (backupPath string, err error) {
	if existing, statErr := os.Stat(path); statErr == nil && !existing.IsDir() {
		backupPath = fmt.Sprintf("%s.broken-%d", path, time.Now().Unix())
		if err := os.Rename(path, backupPath); err != nil {
			return "", fmt.Errorf("back up %s → %s: %w", path, backupPath, err)
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("stat %s: %w", path, statErr)
	}
	if err := os.WriteFile(path, []byte(defaultYAML), 0o644); err != nil {
		return backupPath, fmt.Errorf("write defaults to %s: %w", path, err)
	}
	return backupPath, nil
}

// Banner returns a multi-line operator-facing message describing what
// Migrate did. Empty string when nothing changed.
func (r Result) Banner(existingPath string) string {
	if r.AlreadyCurrent || r.NewConfigPath == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n*** rt-node-agent: new config keys available ***\n")
	if r.OldVersion == 0 {
		b.WriteString(fmt.Sprintf("  existing config has no config_version; assuming v1 (v0.1.x).\n"))
	} else {
		b.WriteString(fmt.Sprintf("  existing config: v%d\n", r.OldVersion))
	}
	b.WriteString(fmt.Sprintf("  new schema:      v%d\n", r.NewVersion))
	b.WriteString("  appended (commented): " + strings.Join(r.AddedKeys, ", ") + "\n\n")
	b.WriteString("  review:  diff " + existingPath + " " + r.NewConfigPath + "\n")
	b.WriteString("  apply:   mv " + r.NewConfigPath + " " + existingPath + "\n")
	b.WriteString("           sudo systemctl restart rt-node-agent\n")
	b.WriteString("\nThe agent will keep running with the existing config until you apply.\n")
	return b.String()
}

// readVersion returns the config_version value or 0 if absent.
func readVersion(root *yaml.Node) int {
	m := topMap(root)
	if m == nil {
		return 0
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		v := m.Content[i+1]
		if k.Value == "config_version" && v.Kind == yaml.ScalarNode {
			n, err := strconv.Atoi(strings.TrimSpace(v.Value))
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// topMap returns the top-level mapping node from a document root.
func topMap(root *yaml.Node) *yaml.Node {
	if root == nil {
		return nil
	}
	n := root
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

func missingTopKeys(existing, def *yaml.Node) []string {
	have := map[string]bool{}
	em := topMap(existing)
	if em != nil {
		for i := 0; i+1 < len(em.Content); i += 2 {
			have[em.Content[i].Value] = true
		}
	}
	var missing []string
	dm := topMap(def)
	if dm == nil {
		return nil
	}
	for i := 0; i+1 < len(dm.Content); i += 2 {
		k := dm.Content[i].Value
		if k == "config_version" {
			continue
		}
		if !have[k] {
			missing = append(missing, k)
		}
	}
	return missing
}

func extractTopKey(root *yaml.Node, key string) *yaml.Node {
	m := topMap(root)
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// renderKey marshals a single {key: val} pair to YAML, dropping the
// document-end marker. Used to render the commented additions block.
func renderKey(key string, val *yaml.Node) (string, error) {
	mapping := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			val,
		},
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(mapping); err != nil {
		return "", err
	}
	_ = enc.Close()
	return buf.String(), nil
}
