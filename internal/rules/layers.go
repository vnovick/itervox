package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Layer names, lowest first. The org layer (read-only) arrives later.
const (
	LayerUser    = "user"
	LayerProject = "project"
	LayerLocal   = "local"
)

// LayerNames lists the layers lowest first: a later layer's rule replaces an
// earlier one's with the same id.
var LayerNames = []string{LayerUser, LayerProject, LayerLocal}

// UserRulesEnv overrides the user rules file path; "off" disables the user
// layer.
const UserRulesEnv = "ITERVOX_USER_RULES"

// UserPath is the user layer's file: $ITERVOX_USER_RULES, else
// ~/.itervox/rules.json. Empty when the layer is off or no home directory is
// known. Under `go test` the layer is off unless the variable names a file,
// so a developer's own rules never reach the tests (as with the user
// settings file).
func UserPath() string {
	if p := os.Getenv(UserRulesEnv); p != "" {
		if p == "off" {
			return ""
		}
		return p
	}
	if testing.Testing() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".itervox", "rules.json")
}

// ProjectPath is the committed project layer's file for a workflow; empty
// without a workflow path.
func ProjectPath(workflowPath string) string {
	if workflowPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "rules.json")
}

// LocalPath is the gitignored project-local layer's file; empty without a
// workflow path.
func LocalPath(workflowPath string) string {
	if workflowPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "rules.local.json")
}

// LayerPath returns the file of the named layer, "" when the layer is off or
// unknown.
func LayerPath(workflowPath, name string) string {
	switch name {
	case LayerUser:
		return UserPath()
	case LayerProject:
		return ProjectPath(workflowPath)
	case LayerLocal:
		return LocalPath(workflowPath)
	}
	return ""
}

// Layer is one rules layer as read from disk.
type Layer struct {
	Name string
	// Path is the layer's file; empty when the layer is off.
	Path string
	// Exists is false when the file does not exist (the layer is empty).
	Exists bool
	// Raw is the file's bytes, comments included.
	Raw []byte
	// Version identifies Raw ("" for a missing file); a save names the
	// version it edited so it cannot overwrite a change it never saw.
	Version string
	// Rules are the parsed rules; nil when Err is set.
	Rules []Rule
	// Err is why the file could not be read or parsed. A layer with an
	// error contributes no rules.
	Err error
}

// ReadLayers reads every layer, lowest first. It never fails as a whole: a
// layer that cannot be read or parsed carries its error and no rules.
func ReadLayers(workflowPath string) []Layer {
	out := make([]Layer, 0, len(LayerNames))
	for _, name := range LayerNames {
		out = append(out, ReadLayer(name, LayerPath(workflowPath, name)))
	}
	return out
}

// ReadLayer reads one layer's file.
func ReadLayer(name, path string) Layer {
	l := Layer{Name: name, Path: path}
	if path == "" {
		return l
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l
	}
	if err != nil {
		l.Err = fmt.Errorf("rules: read %s: %w", path, err)
		return l
	}
	l.Exists, l.Raw, l.Version = true, raw, Version(raw)
	rules, err := Parse(raw)
	if err != nil {
		l.Err = fmt.Errorf("rules: %s: %w", path, err)
		return l
	}
	l.Rules = rules
	return l
}

// Version identifies a file's contents: "" for a missing file.
func Version(raw []byte) string {
	if raw == nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}
