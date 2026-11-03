package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"github.com/vnovick/itervox/internal/config"
)

// ErrStale is returned by a save whose base version is not the file's
// current one: someone changed the file after the editor read it.
var ErrStale = errors.New("rules: the file changed since it was read; reload and try again")

// ErrInvalid wraps a save refused because the result is not a valid rules
// file.
var ErrInvalid = errors.New("rules: invalid rules file")

// newFile is what a form save starts a missing rules file from.
const newFile = `// Itervox rules (JSONC: comments and trailing commas are fine).
// Layers: ~/.itervox/rules.json < .itervox/rules.json < .itervox/rules.local.json.
// See the configuration docs ("Rules") for the fields.
{
  "$schema": "` + SchemaURL + `",
  "rules": [],
}
`

// writeMu serializes rules saves in this process, so a version check and the
// write it guards are one step.
var writeMu sync.Mutex

// SaveRaw replaces the rules file at path with raw (the editor's JSON view),
// as written: comments and layout are the operator's. raw must parse as a
// valid rules file. baseVersion is the Version the editor read ("" for a
// file that did not exist); a different current version is ErrStale.
func SaveRaw(path string, raw []byte, baseVersion string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	if _, err := currentFor(path, baseVersion); err != nil {
		return err
	}
	return write(path, raw)
}

// SaveRules makes the rules file at path hold exactly rules (the editor's
// form view), through the comment-preserving JSONC writer: only the rules
// and fields that changed are rewritten, so comments elsewhere in the file,
// and on rules that did not change, are kept. A missing file is created.
func SaveRules(path string, rules []Rule, baseVersion string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	cur, err := currentFor(path, baseVersion)
	if err != nil {
		return err
	}
	if cur == nil {
		cur = []byte(newFile)
	}
	ops, err := rulesPatch(cur, rules)
	if err != nil {
		return err
	}
	out, err := config.PatchJSONC(cur, ops)
	if err != nil {
		return fmt.Errorf("rules: %s: %w", path, err)
	}
	return write(path, out)
}

// currentFor reads the file and checks it is still at baseVersion. nil bytes
// mean the file does not exist.
func currentFor(path, baseVersion string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("rules: this layer is off (no file)")
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rules: read %s: %w", path, err)
	}
	if Version(raw) != baseVersion {
		return nil, ErrStale
	}
	return raw, nil
}

func write(path string, out []byte) error {
	return config.WriteJSONCFile(path, out, func(b []byte) error {
		if _, err := Parse(b); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return nil
	})
}

// rulesPatch builds the JSON Patch that turns the file's "rules" array into
// rules: per position, only the fields that differ are set or removed; new
// rules are appended and surplus ones removed from the end.
func rulesPatch(cur []byte, rules []Rule) ([]config.JSONPatchOp, error) {
	v, err := config.ParseJSONC(cur)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`%w: a rules file must be a JSON object with a "rules" array`, ErrInvalid)
	}
	want := make([]map[string]any, len(rules))
	for i, r := range rules {
		m, err := ruleValue(r)
		if err != nil {
			return nil, err
		}
		want[i] = m
	}
	old, ok := top["rules"].([]any)
	if !ok {
		return []config.JSONPatchOp{{Op: "add", Path: "/rules", Value: rules}}, nil
	}
	var ops []config.JSONPatchOp
	for i, r := range rules {
		at := "/rules/" + strconv.Itoa(i)
		if i >= len(old) {
			ops = append(ops, config.JSONPatchOp{Op: "add", Path: "/rules/-", Value: r})
			continue
		}
		prev, isObject := old[i].(map[string]any)
		if !isObject {
			ops = append(ops, config.JSONPatchOp{Op: "replace", Path: at, Value: r})
			continue
		}
		for _, k := range ruleKeys {
			nv, has := want[i][k]
			ov, had := prev[k]
			switch {
			case has && (!had || !reflect.DeepEqual(nv, ov)):
				ops = append(ops, config.JSONPatchOp{Op: "add", Path: at + "/" + config.JSONPointer(k), Value: nv})
			case !has && had:
				ops = append(ops, config.JSONPatchOp{Op: "remove", Path: at + "/" + config.JSONPointer(k)})
			}
		}
		for k := range prev {
			if _, known := want[i][k]; !known && !slices.Contains(ruleKeys, k) {
				ops = append(ops, config.JSONPatchOp{Op: "remove", Path: at + "/" + config.JSONPointer(k)})
			}
		}
	}
	for i := len(old) - 1; i >= len(rules); i-- {
		ops = append(ops, config.JSONPatchOp{Op: "remove", Path: "/rules/" + strconv.Itoa(i)})
	}
	return ops, nil
}

// ruleValue is a rule as the generic JSON value ParseJSONC produces, so it
// compares with what the file holds.
func ruleValue(r Rule) (map[string]any, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("rules: encode %s: %w", r.ID, err)
	}
	v, err := config.ParseJSONC(b)
	if err != nil {
		return nil, fmt.Errorf("rules: encode %s: %w", r.ID, err)
	}
	m, _ := v.(map[string]any)
	return m, nil
}
