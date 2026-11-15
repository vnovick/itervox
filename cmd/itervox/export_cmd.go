package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
)

// `itervox export --symphony` (#158) writes a Symphony WORKFLOW.md from this
// project's effective configuration: the Symphony core keys a layer or
// WORKFLOW.md sets (defaults are left to Symphony), mapped back from where
// Itervox keeps them, and the worker prompt. Everything else the project sets
// is an Itervox-only feature Symphony has no place for; each one is listed.

const exportUsage = `usage: itervox export --symphony [--workflow WORKFLOW.md] [--out PATH|-] [--force]`

func runExport(args []string) {
	fatalExit(exportCmd(args, os.Stdout, os.Stderr))
}

// symphonyCoreKey is one key of the Symphony front matter and the Itervox key
// it comes from.
type symphonyCoreKey struct {
	symphony, itervox string
}

// symphonyCoreKeys are the Symphony front-matter keys, in the order they are
// written. Itervox keeps all but the codex.* ones under the same name; codex
// keys without an Itervox equivalent (approval_policy, thread_sandbox,
// turn_sandbox_policy) are written back from an imported workflow's codex
// block, which Itervox keeps but does not read.
var symphonyCoreKeys = []symphonyCoreKey{
	{"tracker.kind", "tracker.kind"},
	{"tracker.endpoint", "tracker.endpoint"},
	{"tracker.api_key", "tracker.api_key"},
	{"tracker.project_slug", "tracker.project_slug"},
	{"tracker.active_states", "tracker.active_states"},
	{"tracker.terminal_states", "tracker.terminal_states"},
	{"polling.interval_ms", "polling.interval_ms"},
	{"workspace.root", "workspace.root"},
	{"hooks.after_create", "hooks.after_create"},
	{"hooks.before_run", "hooks.before_run"},
	{"hooks.after_run", "hooks.after_run"},
	{"hooks.before_remove", "hooks.before_remove"},
	{"hooks.timeout_ms", "hooks.timeout_ms"},
	{"agent.max_concurrent_agents", "agent.max_concurrent_agents"},
	{"agent.max_turns", "agent.max_turns"},
	{"agent.max_retry_backoff_ms", "agent.max_retry_backoff_ms"},
	{"agent.max_concurrent_agents_by_state", "agent.max_concurrent_agents_by_state"},
	{"codex.command", "agent.command"},
	{"codex.approval_policy", "codex.approval_policy"},
	{"codex.thread_sandbox", "codex.thread_sandbox"},
	{"codex.turn_sandbox_policy", "codex.turn_sandbox_policy"},
	{"codex.turn_timeout_ms", "agent.turn_timeout_ms"},
	{"codex.read_timeout_ms", "agent.read_timeout_ms"},
	{"codex.stall_timeout_ms", "agent.stall_timeout_ms"},
	{"server.port", "server.port"},
}

// exportResult is a Symphony WORKFLOW.md and what it left out.
type exportResult struct {
	text    []byte
	dropped []string
	notes   []string
}

func exportCmd(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(errOut)
	symphony := fs.Bool("symphony", false, "write a Symphony WORKFLOW.md (the only export format)")
	wfPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	outPath := fs.String("out", "", "where to write it; - for standard output (default: WORKFLOW.symphony.md next to WORKFLOW.md)")
	force := fs.Bool("force", false, "overwrite --out when it exists")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || !*symphony {
		_, _ = fmt.Fprintln(errOut, exportUsage)
		return 2
	}
	res, err := exportSymphony(*wfPath)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox export: %v\n", err)
		return 1
	}
	dest := *outPath
	if dest == "" {
		dest = filepath.Join(filepath.Dir(*wfPath), "WORKFLOW.symphony.md")
	}
	report := out
	if dest == "-" {
		_, _ = out.Write(res.text)
		report = errOut
	} else {
		if _, statErr := os.Stat(dest); statErr == nil && !*force {
			_, _ = fmt.Fprintf(errOut, "itervox export: %s exists; pass --force to overwrite it\n", dest)
			return 1
		}
		if err := atomicfs.WriteFile(dest, res.text, 0o644); err != nil {
			_, _ = fmt.Fprintf(errOut, "itervox export: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(report, "Wrote %s (Symphony core keys and the worker prompt).\n", dest)
	}
	for _, n := range res.notes {
		_, _ = fmt.Fprintf(report, "note: %s\n", n)
	}
	if len(res.dropped) == 0 {
		_, _ = fmt.Fprintln(report, "No Itervox-only features to drop.")
		return 0
	}
	_, _ = fmt.Fprintln(report, "Dropped Itervox-only features (Symphony has no place for them):")
	for _, d := range res.dropped {
		_, _ = fmt.Fprintf(report, "  %s\n", d)
	}
	return 0
}

func exportSymphony(workflowPath string) (exportResult, error) {
	var res exportResult
	merged, prompt, err := config.MergedFrontMatter(workflowPath)
	if err != nil {
		return res, err
	}
	doc := &yaml.Node{Kind: yaml.MappingNode}
	used := map[string]bool{}
	for _, k := range symphonyCoreKeys {
		v, ok := lookupSetting(merged, k.itervox)
		if !ok {
			continue
		}
		used[k.itervox] = true
		v = normalizeYAMLValue(v)
		switch k.symphony {
		case "codex.command":
			cmd, _ := v.(string)
			backend, _ := lookupSetting(merged, "agent.backend")
			if config.BackendFromCommand(cmd) != "codex" && backend != "codex" {
				res.notes = append(res.notes, fmt.Sprintf("agent.command %q does not run Codex; Symphony runs the Codex app server, so codex.command was left out", cmd))
				continue
			}
			if strings.TrimSpace(cmd) == "codex" {
				v = symphonyAppServer
			}
			used["agent.backend"] = true
		case "tracker.api_key":
			if s, _ := v.(string); !strings.HasPrefix(strings.TrimSpace(s), "$") {
				v = "$LINEAR_API_KEY"
				res.notes = append(res.notes, "tracker.api_key holds a literal secret; it was not exported, $LINEAR_API_KEY stands in for it")
			}
		case "tracker.kind":
			if v != "linear" {
				res.notes = append(res.notes, fmt.Sprintf("tracker.kind %q: Symphony's tracker is Linear", v))
			}
		}
		if err := setNodePath(doc, strings.Split(k.symphony, "."), v); err != nil {
			return res, err
		}
	}
	if !used["tracker.kind"] {
		return res, errors.New("tracker.kind is not set; Symphony needs it")
	}
	if strings.Contains(prompt, "{{ run.") || strings.Contains(prompt, "{{run.") || strings.Contains(prompt, "{{ rules") {
		res.notes = append(res.notes, "the worker prompt uses Itervox-only Liquid variables (run.*, rules); Symphony renders them empty")
	}
	res.dropped = droppedFeatures(merged, used)
	if profiles, _ := asMap(merged["agent"]); profiles != nil {
		if p, ok := asMap(profiles["profiles"]); ok && len(p) > 0 {
			res.dropped = append(res.dropped, "agent profile files under .itervox/agents/ (SOUL.md, INSTRUCTIONS.md)")
		}
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return res, err
	}
	_ = enc.Close()
	buf.WriteString("---\n")
	buf.WriteString(prompt)
	buf.WriteString("\n")
	res.text = buf.Bytes()
	return res, nil
}

// droppedFeatures lists every key the project sets that the export left out:
// leaf keys, except that a registered setting (or another map read whole,
// like automations) is one entry.
func droppedFeatures(merged map[string]any, used map[string]bool) []string {
	var out []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if used[prefix] || prefix == "itervox_schema_version" {
			return
		}
		if _, known := config.SettingFieldFor(prefix); known {
			out = append(out, prefix)
			return
		}
		m, isMap := asMap(v)
		if !isMap || len(m) == 0 || prefix == "automations" {
			out = append(out, prefix)
			return
		}
		for k, e := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			walk(key, e)
		}
	}
	walk("", merged)
	out = slices.DeleteFunc(out, func(s string) bool { return s == "" })
	sort.Strings(out)
	return out
}

// setNodePath sets parts to value in a YAML mapping node, creating nested
// mappings in insertion order.
func setNodePath(node *yaml.Node, parts []string, value any) error {
	for i, p := range parts {
		var child *yaml.Node
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == p {
				child = node.Content[j+1]
			}
		}
		if i == len(parts)-1 {
			var v yaml.Node
			if err := v.Encode(value); err != nil {
				return err
			}
			if child != nil {
				*child = v
				return nil
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: p}, &v)
			return nil
		}
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: p}, child)
		}
		node = child
	}
	return nil
}
