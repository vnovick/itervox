package main

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// `itervox import WORKFLOW.md` (#158) converts a Symphony workflow, or an
// Itervox v0.x one, into the current layout: the layered settings (agent,
// polling, server, workspace) move to .itervox/settings.jsonc, written with
// the generic comment-preserving writer; profile prompts move to prompt files
// (.itervox/agents/<name>/SOUL.md and INSTRUCTIONS.md, as `itervox init
// --update` does); the tracker, hooks and the worker prompt (the body) stay in
// WORKFLOW.md, stamped itervox_schema_version 2. The original is kept as
// WORKFLOW.md.bak, and every key it could not map is printed.

const importUsage = `usage: itervox import [--force] WORKFLOW.md`

func runImport(args []string) {
	fatalExit(importCmd(args, time.Now(), os.Stdout, os.Stderr))
}

// symphonyCodexKeys maps the Symphony codex.* keys Itervox has an equivalent
// for to that key. The other codex.* keys stay in WORKFLOW.md, unread, so
// `itervox export --symphony` can write them back.
var symphonyCodexKeys = map[string]string{
	"codex.command":          "agent.command",
	"codex.turn_timeout_ms":  "agent.turn_timeout_ms",
	"codex.read_timeout_ms":  "agent.read_timeout_ms",
	"codex.stall_timeout_ms": "agent.stall_timeout_ms",
}

// symphonyAppServer is Symphony's default codex.command. Itervox runs `codex
// exec`, so it is imported as plain "codex" and exported back as this.
const symphonyAppServer = "codex app-server"

// importResult is what an import did, for the report.
type importResult struct {
	source   string
	backup   string
	settings []string
	profiles []string
	unmapped []string
	warnings []string
}

func importCmd(args []string, now time.Time, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(errOut)
	force := fs.Bool("force", false, "overwrite an existing WORKFLOW.md.bak (and generated profile files)")
	var positional []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) != 1 {
		_, _ = fmt.Fprintln(errOut, importUsage)
		return 2
	}
	res, err := importWorkflow(positional[0], *force, now)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox import: %v\n", err)
		return 1
	}
	printImportReport(out, positional[0], res)
	return 0
}

func importWorkflow(path string, force bool, now time.Time) (importResult, error) {
	var res importResult
	original, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	wf, err := workflow.Load(path)
	if err != nil {
		return res, err
	}
	backup := path + ".bak"
	if _, statErr := os.Stat(backup); statErr == nil && !force {
		return res, fmt.Errorf("%s already exists; move it or pass --force", backup)
	}
	symphony := isSymphonyWorkflow(wf.Config)
	if symphony {
		res.source = "Symphony"
	} else {
		res.source = fmt.Sprintf("Itervox (schema %d)", intValue(wf.Config["itervox_schema_version"]))
		// Profile prompts move to prompt files and the schema marker is
		// stamped, exactly as `itervox init --update` does.
		mig, err := migrateWorkflowToSchema2(path, force, now)
		if err != nil {
			return res, err
		}
		res.profiles, res.warnings, res.backup = mig.Profiles, mig.Warnings, mig.BackupPath
		if wf, err = workflow.Load(path); err != nil {
			return res, err
		}
	}
	if res.backup == "" {
		if err := atomicfs.WriteFile(backup, original, 0o644); err != nil {
			return res, err
		}
		res.backup = backup
	}

	front := wf.Config
	var assignments []config.SettingAssignment
	var remove []string
	for _, f := range config.SettingFields {
		v, ok := lookupSetting(front, f.Key)
		if !ok || f.Editor != "" || f.Key == "agent.available_models" {
			continue // profiles and models stay where their editors write them
		}
		if err := config.ValidateSettingValue(f.Key, normalizeYAMLValue(v)); err != nil {
			res.unmapped = append(res.unmapped, fmt.Sprintf("%s: %v (left in WORKFLOW.md)", f.Key, err))
			continue
		}
		assignments = append(assignments, config.SettingAssignment{Key: f.Key, Value: normalizeYAMLValue(v)})
		remove = append(remove, f.Key)
	}
	if symphony {
		for _, key := range slices.Sorted(maps.Keys(symphonyCodexKeys)) {
			v, ok := lookupSetting(front, key)
			if !ok {
				continue
			}
			v = normalizeYAMLValue(v)
			if key == "codex.command" && v == symphonyAppServer {
				v = "codex"
			}
			assignments = append(assignments, config.SettingAssignment{Key: symphonyCodexKeys[key], Value: v})
			remove = append(remove, key)
		}
		if _, ok := lookupSetting(front, "codex.command"); ok {
			assignments = append(assignments, config.SettingAssignment{Key: "agent.backend", Value: "codex"})
		}
	}
	res.unmapped = append(res.unmapped, unmappedKeys(front, remove, symphony)...)
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].Key < assignments[j].Key })
	for _, a := range assignments {
		res.settings = append(res.settings, a.Key)
	}

	jsonc := config.ProjectSettingsPath(path)
	write := func(p string) error { return config.SetSettings(p, assignments...) }
	if len(assignments) == 0 {
		write = func(string) error { return nil }
	}
	extra := []workflow.Mutator{dropEmptyBlocks(append(slices.Clone(config.LayeredSections), "codex")...)}
	if symphony {
		extra = append([]workflow.Mutator{stampSchemaVersion}, extra...)
	}
	if err := persistLayer(path, jsonc, write, nil, remove, extra...); err != nil {
		return res, err
	}
	if _, err := config.Load(path); err != nil {
		res.warnings = append(res.warnings, fmt.Sprintf("the imported workflow does not load yet: %v", err))
	}
	return res, nil
}

// isSymphonyWorkflow reports whether front matter is a Symphony workflow: no
// Itervox schema marker, and nothing outside Symphony's sections.
func isSymphonyWorkflow(front map[string]any) bool {
	if _, ok := front["itervox_schema_version"]; ok {
		return false
	}
	if _, ok := front["codex"]; ok {
		return true
	}
	for k := range front {
		if !slices.Contains(symphonySections, k) {
			return false
		}
	}
	if kind, _ := lookupSetting(front, "tracker.kind"); kind != nil && kind != "linear" {
		return false // Symphony's tracker is Linear
	}
	agent, _ := asMap(front["agent"])
	for k := range agent {
		if !slices.Contains(symphonyAgentKeys, k) {
			return false
		}
	}
	return true
}

// symphonyAgentKeys are the agent.* keys of a Symphony workflow.
var symphonyAgentKeys = []string{"max_concurrent_agents", "max_turns", "max_retry_backoff_ms", "max_concurrent_agents_by_state"}

// symphonySections are the top-level front-matter keys of a Symphony
// workflow.
var symphonySections = []string{"tracker", "polling", "workspace", "hooks", "agent", "codex", "server"}

// unmappedKeys lists the leaf keys of the layered sections (and codex) that
// stay behind because Itervox does not read them.
func unmappedKeys(front map[string]any, moved []string, symphony bool) []string {
	var out []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if slices.Contains(moved, prefix) {
			return
		}
		if _, known := config.SettingFieldFor(prefix); known {
			return
		}
		if m, ok := asMap(v); ok && len(m) > 0 && !strings.HasPrefix(prefix, "codex.") {
			for k, e := range m {
				walk(prefix+"."+k, e)
			}
			return
		}
		why := "Itervox does not read it; left in WORKFLOW.md"
		if strings.HasPrefix(prefix, "codex.") {
			why = "Itervox has no equivalent (its codex runner sets its own approval and sandbox flags; see a profile's permission_mode); kept in WORKFLOW.md for `itervox export --symphony`"
		}
		out = append(out, prefix+": "+why)
	}
	sections := slices.Clone(config.LayeredSections)
	if symphony {
		sections = append(sections, "codex")
	}
	for _, s := range sections {
		if v, ok := front[s]; ok {
			walk(s, v)
		}
	}
	slices.Sort(out)
	return out
}

func asMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = e
		}
		return out, true
	}
	return nil, false
}

// normalizeYAMLValue gives a front-matter value the JSON shapes a settings
// file holds (string-keyed maps all the way down).
func normalizeYAMLValue(v any) any {
	switch t := v.(type) {
	case map[string]any, map[any]any:
		m, _ := asMap(t)
		out := make(map[string]any, len(m))
		for k, e := range m {
			out[k] = normalizeYAMLValue(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeYAMLValue(e)
		}
		return out
	}
	return v
}

func intValue(v any) int {
	n, _ := v.(int)
	return n
}

// stampSchemaVersion puts itervox_schema_version first in the front matter
// when it is missing.
func stampSchemaVersion(front []string) ([]string, error) {
	for _, l := range front {
		if strings.HasPrefix(l, "itervox_schema_version:") {
			return front, nil
		}
	}
	return append([]string{fmt.Sprintf("itervox_schema_version: %d", config.LatestWorkflowSchemaVersion)}, front...), nil
}

// dropEmptyBlocks returns a Mutator removing each top-level "<name>:" header
// that has no entries left under it (its keys all moved out).
func dropEmptyBlocks(names ...string) workflow.Mutator {
	return func(front []string) ([]string, error) {
		out := make([]string, 0, len(front))
		for i, l := range front {
			name, _, _ := strings.Cut(l, ":")
			if slices.Contains(names, name) && strings.TrimSpace(strings.TrimPrefix(l, name+":")) == "" && !blockHasEntries(front, i) {
				continue
			}
			out = append(out, l)
		}
		return out, nil
	}
}

func blockHasEntries(front []string, header int) bool {
	for _, l := range front[header+1:] {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return l[0] == ' ' || l[0] == '\t' || strings.HasPrefix(l, "- ")
	}
	return false
}

func printImportReport(out io.Writer, path string, res importResult) {
	_, _ = fmt.Fprintf(out, "Imported %s (%s workflow).\n", path, res.source)
	_, _ = fmt.Fprintf(out, "  Original kept as %s.\n", res.backup)
	if len(res.settings) > 0 {
		_, _ = fmt.Fprintf(out, "  Settings written to %s:\n", config.ProjectSettingsPath(path))
		for _, k := range res.settings {
			_, _ = fmt.Fprintf(out, "    %s\n", k)
		}
	}
	_, _ = fmt.Fprintf(out, "  Worker prompt: the body of %s.\n", path)
	for _, p := range res.profiles {
		_, _ = fmt.Fprintf(out, "  Profile %s: prompt files in .itervox/agents/%s/ (SOUL.md, INSTRUCTIONS.md).\n", p, p)
	}
	if len(res.unmapped) > 0 {
		_, _ = fmt.Fprintln(out, "  Not mapped:")
		for _, u := range res.unmapped {
			_, _ = fmt.Fprintf(out, "    %s\n", u)
		}
	} else {
		_, _ = fmt.Fprintln(out, "  Every key was mapped.")
	}
	for _, w := range res.warnings {
		_, _ = fmt.Fprintf(out, "  warning: %s\n", w)
	}
}
