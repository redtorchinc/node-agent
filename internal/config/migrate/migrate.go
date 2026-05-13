// Package migrate keeps the operator's config.yaml in sync with the
// agent's embedded default schema across upgrades.
//
// Strategy (changed in v0.2.7):
//
//   - On install, if the existing config.yaml has the same schema as the
//     embedded default AND all top-level keys present, leave it alone.
//   - Otherwise: back up the existing file to `<path>.bak` (single file,
//     overwritten on each migration), write the embedded default to the
//     live path, then graft every top-level value the operator had set
//     in the old file onto the new tree. Result: a fresh, well-formatted
//     config that shows new features in their default state and
//     preserves the operator's customised values.
//
// The previous v0.2-era approach (appending commented additions to a
// `.yaml.new` sidecar) compounded across re-installs because commented
// keys aren't parsed as "present" — each install re-detected them as
// "missing" and appended another block. The new approach is idempotent:
// running Migrate twice in a row on the same file is a no-op on the
// second call.
//
// To enable a new feature, the operator edits config.yaml directly and
// restarts the agent. There is no `.new` sidecar to merge from.
package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Action describes what Migrate did to the operator's config.
//
//   - ActionFresh: no existing file was present. Migrate didn't write
//     one — the install path elsewhere drops config.yaml.example, and
//     the runtime config loader falls back to defaults when the live
//     file is absent. This keeps fresh installs minimal.
//   - ActionNoChange: existing file already matches the new schema.
//     Nothing to do.
//   - ActionMerged: existing file was backed up to BackupPath and a
//     new file written in its place. Operator's top-level values were
//     grafted onto the new default tree.
type Action string

const (
	ActionFresh    Action = "fresh"
	ActionNoChange Action = "no_change"
	ActionMerged   Action = "merged"
)

// Result describes what Migrate did.
type Result struct {
	Action Action

	// BackupPath is set only when Action==ActionMerged. The previous
	// config file is at this path; operator can compare or recover.
	BackupPath string

	// PreservedKeys lists the top-level keys whose operator-set values
	// were grafted from the backup into the new config. Empty when the
	// operator hadn't customised anything beyond the defaults.
	PreservedKeys []string

	// DroppedKeys lists top-level keys that were in the old config but
	// no longer appear in the new schema. Logged for operator
	// visibility; values remain in the .bak file.
	DroppedKeys []string

	OldVersion int
	NewVersion int
}

// AlreadyCurrent reports whether the migration was a no-op (existing
// file already at the current schema, nothing grafted). Kept as a
// method for backward compatibility with callers that switch on it.
func (r Result) AlreadyCurrent() bool { return r.Action == ActionNoChange || r.Action == ActionFresh }

// CurrentVersion mirrors config.SchemaVersion. Pinned here to avoid an
// import cycle (config imports allocators which we don't need here).
const CurrentVersion = 2

// ErrBrokenYAML is returned by Migrate when the existing file cannot be
// parsed as YAML. Callers (typically the install path) should respond by
// calling ForceReset to back up the broken file and lay down defaults —
// see internal/service/bootstrap.go for the standard recovery flow.
var ErrBrokenYAML = errors.New("existing config.yaml is not valid YAML")

// Migrate reconciles the YAML at existingPath with defaultYAML.
//
//   - Missing file → ActionFresh, no I/O.
//   - Parse error → ErrBrokenYAML; caller invokes ForceReset.
//   - Same schema, all top-level keys present → ActionNoChange, no I/O.
//   - Schema drift or missing keys → ActionMerged. Existing file is
//     moved to "<existingPath>.bak" (overwriting any prior .bak), the
//     embedded default is written to existingPath, and every top-level
//     value the operator had in the backup is grafted into the new
//     tree. config_version is always taken from the new schema, never
//     the backup.
//
// Comments on subtrees the operator customised are lost (the entire
// subtree is replaced verbatim with the operator's old value). Comments
// elsewhere in the default — explaining the keys, sectioning the file
// — are preserved.
func Migrate(existingPath, defaultYAML string) (Result, error) {
	existingBytes, err := os.ReadFile(existingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Action: ActionFresh, NewVersion: CurrentVersion}, nil
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

	oldVersion := readVersion(&existingDoc)
	missing := missingTopKeys(&existingDoc, &defaultDoc)
	deprecated := extraTopKeys(&existingDoc, &defaultDoc)

	// Same schema, same top-level shape — nothing to do. Avoid rewriting
	// the file on every install just to no-op it.
	if oldVersion == CurrentVersion && len(missing) == 0 && len(deprecated) == 0 {
		return Result{
			Action:     ActionNoChange,
			OldVersion: oldVersion,
			NewVersion: CurrentVersion,
		}, nil
	}

	// Graft the operator's top-level values onto the default tree.
	preserved := graftValues(&defaultDoc, &existingDoc)

	// Render the merged tree.
	var merged bytes.Buffer
	enc := yaml.NewEncoder(&merged)
	enc.SetIndent(2)
	if err := enc.Encode(&defaultDoc); err != nil {
		return Result{}, fmt.Errorf("encode merged yaml: %w", err)
	}
	_ = enc.Close()

	// Back up the existing file, then write the new content in place.
	// Use a single .bak (overwritten); operators wanting longer history
	// should commit to git or take their own snapshots.
	backupPath := existingPath + ".bak"
	if err := os.Rename(existingPath, backupPath); err != nil {
		return Result{}, fmt.Errorf("backup %s → %s: %w", existingPath, backupPath, err)
	}
	if err := os.WriteFile(existingPath, merged.Bytes(), 0o644); err != nil {
		// Try to restore the original on write failure so the operator
		// isn't left with no live config.
		_ = os.Rename(backupPath, existingPath)
		return Result{}, fmt.Errorf("write %s: %w", existingPath, err)
	}

	return Result{
		Action:        ActionMerged,
		BackupPath:    backupPath,
		PreservedKeys: preserved,
		DroppedKeys:   deprecated,
		OldVersion:    oldVersion,
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
// Migrate did. Empty when ActionFresh or ActionNoChange — silence is
// the right output when nothing changed.
func (r Result) Banner(existingPath string) string {
	if r.Action != ActionMerged {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n*** rt-node-agent: config updated in place ***\n")
	if r.OldVersion != r.NewVersion {
		if r.OldVersion == 0 {
			b.WriteString("  schema: pre-v1 → v" + strconv.Itoa(r.NewVersion) + "\n")
		} else {
			b.WriteString(fmt.Sprintf("  schema: v%d → v%d\n", r.OldVersion, r.NewVersion))
		}
	}
	b.WriteString("  previous: " + r.BackupPath + "\n")
	if len(r.PreservedKeys) > 0 {
		b.WriteString("  preserved your settings: " + strings.Join(r.PreservedKeys, ", ") + "\n")
	}
	if len(r.DroppedKeys) > 0 {
		b.WriteString("  dropped (recoverable from .bak): " + strings.Join(r.DroppedKeys, ", ") + "\n")
	}
	b.WriteString("\nEdit " + existingPath + " to enable new features, then:\n")
	b.WriteString("  " + restartCommand() + "\n")
	return b.String()
}

// restartCommand returns the OS-appropriate "restart the service" line
// for operator-facing prompts.
func restartCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "sudo launchctl kickstart -k system/com.redtorch.rt-node-agent"
	case "windows":
		return "Restart-Service rt-node-agent  (elevated PowerShell)"
	default:
		return "sudo systemctl restart rt-node-agent"
	}
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

// missingTopKeys lists top-level keys in def that aren't in existing.
// config_version is excluded — it's owned by the schema, not the operator.
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

// extraTopKeys lists top-level keys in existing that aren't in def
// (i.e. the operator carried over an old key that's no longer in the
// schema). config_version is excluded.
func extraTopKeys(existing, def *yaml.Node) []string {
	have := map[string]bool{}
	dm := topMap(def)
	if dm != nil {
		for i := 0; i+1 < len(dm.Content); i += 2 {
			have[dm.Content[i].Value] = true
		}
	}
	var extras []string
	em := topMap(existing)
	if em == nil {
		return nil
	}
	for i := 0; i+1 < len(em.Content); i += 2 {
		k := em.Content[i].Value
		if k == "config_version" {
			continue
		}
		if !have[k] {
			extras = append(extras, k)
		}
	}
	return extras
}

// graftValues walks every top-level key in defNode that ALSO exists in
// oldDoc and replaces defNode's value subtree with the operator's old
// value subtree. The config_version key is always retained from the
// new schema. Returns the list of keys actually grafted.
//
// Subtree replacement is intentional and wholesale — the operator's
// nested values (e.g. all of platforms.*) come over together. Any
// comments the operator added in their old file come with the subtree;
// comments the default had inside the replaced subtree are lost (which
// is fine — operator's content takes precedence by design).
func graftValues(defNode, oldDoc *yaml.Node) []string {
	defMap := topMap(defNode)
	oldMap := topMap(oldDoc)
	if defMap == nil || oldMap == nil {
		return nil
	}
	oldByKey := map[string]*yaml.Node{}
	for i := 0; i+1 < len(oldMap.Content); i += 2 {
		oldByKey[oldMap.Content[i].Value] = oldMap.Content[i+1]
	}
	var preserved []string
	for i := 0; i+1 < len(defMap.Content); i += 2 {
		key := defMap.Content[i].Value
		if key == "config_version" {
			continue
		}
		oldValue, ok := oldByKey[key]
		if !ok {
			continue
		}
		defMap.Content[i+1] = oldValue
		preserved = append(preserved, key)
	}
	return preserved
}
