package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/atomicfs"
	"gopkg.in/yaml.v3"
)

// runModels is the entrypoint for `itervox models <subcommand>`.
//
// Subcommands:
//
//	list                  print agent.available_models currently in WORKFLOW.md
//	refresh [--backend X] query the configured backend(s) for fresh model lists
//	                      and rewrite agent.available_models in WORKFLOW.md.
//
// The refresh path is the primary "new model just shipped" workflow: an
// operator can run `itervox models refresh` to discover newly-released
// models from Anthropic / OpenAI APIs and have the dashboard's model picker
// pick them up on the next poll. The same logic runs in init's
// generateWorkflow path, so a fresh project gets a current list too.
func runModels(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: itervox models <list|refresh> [flags]")
		fatalExit(2)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		runModelsList(rest)
	case "refresh":
		runModelsRefresh(rest)
	case "-h", "--help":
		fmt.Println(`usage: itervox models <subcommand> [flags]

Subcommands:
  list                  Print agent.available_models from WORKFLOW.md.
  refresh               Query Anthropic / OpenAI APIs for available models
                        and rewrite agent.available_models in WORKFLOW.md.

Refresh flags:
  --workflow PATH       Path to WORKFLOW.md (default: WORKFLOW.md)
  --backend STRING      claude | codex | all (default: all)
  --dry-run             Print the discovered list; do not write WORKFLOW.md`)
	default:
		fmt.Fprintf(os.Stderr, "itervox models: unknown subcommand %q\n", sub)
		fatalExit(2)
	}
}

func runModelsList(args []string) {
	fs := flag.NewFlagSet("models list", flag.ExitOnError)
	workflowPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	_ = fs.Parse(args)

	models, err := readAvailableModels(*workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "itervox models list: %v\n", err)
		fatalExit(1)
	}
	out, _ := json.MarshalIndent(models, "", "  ")
	fmt.Println(string(out))
}

func runModelsRefresh(args []string) {
	fs := flag.NewFlagSet("models refresh", flag.ExitOnError)
	workflowPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	backend := fs.String("backend", "all", "claude | codex | all")
	dryRun := fs.Bool("dry-run", false, "print discovered models; do not write")
	_ = fs.Parse(args)

	discovered := map[string][]agent.ModelOption{}
	switch *backend {
	case "claude":
		discovered["claude"] = agent.ListClaudeModels()
	case "codex":
		discovered["codex"] = agent.ListCodexModels()
	case "all", "":
		discovered["claude"] = agent.ListClaudeModels()
		discovered["codex"] = agent.ListCodexModels()
	default:
		fmt.Fprintf(os.Stderr, "itervox models refresh: invalid --backend %q (claude|codex|all)\n", *backend)
		fatalExit(2)
	}

	for be, opts := range discovered {
		fmt.Printf("%s: %d model(s)\n", be, len(opts))
		for _, m := range opts {
			fmt.Printf("  - %s  %s\n", m.ID, m.Label)
		}
	}
	if *dryRun {
		return
	}

	merged, err := mergeAvailableModelsIntoWorkflow(*workflowPath, discovered)
	if err != nil {
		fmt.Fprintf(os.Stderr, "itervox models refresh: %v\n", err)
		fatalExit(1)
	}
	fmt.Printf("itervox models refresh: %s updated (claude=%d, codex=%d)\n",
		*workflowPath, len(merged["claude"]), len(merged["codex"]))
}

// readAvailableModels parses just the agent.available_models block from a
// WORKFLOW.md without invoking the full config loader. Returns the empty
// map when the field is absent.
func readAvailableModels(workflowPath string) (map[string][]agent.ModelOption, error) {
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", workflowPath, err)
	}
	front, _, ok := splitWorkflowFrontMatter(string(data))
	if !ok {
		return nil, fmt.Errorf("workflow %s: front matter not found", workflowPath)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return nil, fmt.Errorf("parse front matter: %w", err)
	}
	out := map[string][]agent.ModelOption{}
	a, _ := doc["agent"].(map[string]any)
	if a == nil {
		return out, nil
	}
	am, _ := a["available_models"].(map[string]any)
	if am == nil {
		return out, nil
	}
	for backend, raw := range am {
		list, _ := raw.([]any)
		if list == nil {
			continue
		}
		opts := make([]agent.ModelOption, 0, len(list))
		for _, entry := range list {
			m, _ := entry.(map[string]any)
			if m == nil {
				continue
			}
			id, _ := m["id"].(string)
			label, _ := m["label"].(string)
			if id == "" {
				continue
			}
			opts = append(opts, agent.ModelOption{ID: id, Label: label})
		}
		if len(opts) > 0 {
			out[backend] = opts
		}
	}
	return out, nil
}

// mergeAvailableModelsIntoWorkflow rewrites agent.available_models in
// WORKFLOW.md from the discovered map. For each backend present in
// `discovered`, the new list replaces the old; other backends keep their
// previous entries. Atomic write.
func mergeAvailableModelsIntoWorkflow(workflowPath string, discovered map[string][]agent.ModelOption) (map[string][]agent.ModelOption, error) {
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", workflowPath, err)
	}
	front, after, ok := splitWorkflowFrontMatter(string(raw))
	if !ok {
		return nil, fmt.Errorf("workflow %s: front matter not found", workflowPath)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return nil, fmt.Errorf("parse front matter: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}

	a, _ := doc["agent"].(map[string]any)
	if a == nil {
		a = map[string]any{}
	}
	merged := map[string][]agent.ModelOption{}
	existing, _ := a["available_models"].(map[string]any)
	for backend, raw := range existing {
		list, _ := raw.([]any)
		for _, entry := range list {
			m, _ := entry.(map[string]any)
			if m == nil {
				continue
			}
			id, _ := m["id"].(string)
			label, _ := m["label"].(string)
			if id == "" {
				continue
			}
			merged[backend] = append(merged[backend], agent.ModelOption{ID: id, Label: label})
		}
	}
	// Refreshed backends overwrite their slot entirely; untouched backends
	// keep their existing entries.
	for backend, opts := range discovered {
		merged[backend] = opts
	}

	yamlMap := map[string][]map[string]string{}
	for backend, opts := range merged {
		entries := make([]map[string]string, 0, len(opts))
		for _, m := range opts {
			entry := map[string]string{"id": m.ID}
			if m.Label != "" {
				entry["label"] = m.Label
			}
			entries = append(entries, entry)
		}
		yamlMap[backend] = entries
	}
	a["available_models"] = yamlMap
	doc["agent"] = a

	encoded, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode front matter: %w", err)
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(encoded)
	out.WriteString("---\n")
	out.WriteString(after)
	if err := atomicfs.WriteFile(workflowPath, out.Bytes(), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", workflowPath, err)
	}
	return merged, nil
}

// modelBackendsAccepted returns the set of backends `itervox models refresh`
// understands. Kept narrow so the HTTP handler and the CLI share the same
// validation surface.
func modelBackendsAccepted() []string {
	return []string{"claude", "codex", "all"}
}

// IsAcceptedModelBackend reports whether s is one of the recognised
// --backend values. Exposed for the HTTP refresh handler.
func IsAcceptedModelBackend(s string) bool {
	for _, v := range modelBackendsAccepted() {
		if v == s {
			return true
		}
	}
	return false
}
