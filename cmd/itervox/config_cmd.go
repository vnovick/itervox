package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
)

// `itervox config` (#129) shows the effective settings with the layer each
// came from, and edits the JSONC settings files with their comments kept.
//
//	itervox config show [--workflow WORKFLOW.md] [--json]
//	itervox config set KEY VALUE [--user] [--workflow WORKFLOW.md]
//	itervox config unset KEY [--user] [--workflow WORKFLOW.md]

const configUsage = `usage: itervox config show [--workflow WORKFLOW.md] [--json]
       itervox config set KEY VALUE [--user] [--workflow WORKFLOW.md]
       itervox config unset KEY [--user] [--workflow WORKFLOW.md]`

func runConfig(args []string) {
	fatalExit(configCmd(args, os.Stdout, os.Stderr))
}

// configCmd runs `itervox config` and returns the exit code.
func configCmd(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || (args[0] != "show" && args[0] != "set" && args[0] != "unset") {
		_, _ = fmt.Fprintln(errOut, configUsage)
		return 2
	}
	fs := flag.NewFlagSet("config "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	workflow := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	asJSON := fs.Bool("json", false, "(show) print JSON")
	user := fs.Bool("user", false, "(set, unset) edit the user file instead of the project's .itervox/settings.jsonc")
	// Flags may come before or after the positional arguments.
	var positional []string
	rest := args[1:]
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

	switch args[0] {
	case "show":
		if len(positional) > 0 {
			_, _ = fmt.Fprintln(errOut, configUsage)
			return 2
		}
		return configShow(*workflow, *asJSON, out, errOut)
	case "set":
		if len(positional) != 2 {
			_, _ = fmt.Fprintln(errOut, configUsage)
			return 2
		}
		return configSet(*workflow, *user, positional[0], parseSettingValue(positional[1]), out, errOut)
	default:
		if len(positional) != 1 {
			_, _ = fmt.Fprintln(errOut, configUsage)
			return 2
		}
		return configSet(*workflow, *user, positional[0], nil, out, errOut)
	}
}

func configShow(workflow string, asJSON bool, out, errOut io.Writer) int {
	settings, err := config.Explain(workflow)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox config show: %v\n", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(settings); err != nil {
			_, _ = fmt.Fprintf(errOut, "itervox config show: %v\n", err)
			return 1
		}
		return 0
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KEY\tVALUE\tLAYER")
	for _, s := range settings {
		layer := s.Layer
		if s.Path != "" {
			layer += " (" + s.Path + ")"
		}
		if s.Note != "" {
			layer += " — " + s.Note
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Key, truncateSettingValue(formatSettingValue(s.Value)), layer)
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

// configSet sets (value != nil) or removes a key in a settings file, then
// loads the workflow with it; a change the workflow does not load with is
// undone.
func configSet(workflow string, user bool, key string, value any, out, errOut io.Writer) int {
	verb := "set"
	if value == nil {
		verb = "unset"
	}
	path := config.ProjectSettingsPath(workflow)
	if user {
		if path = config.UserSettingsPath(); path == "" {
			_, _ = fmt.Fprintf(errOut, "itervox config %s: the user settings file is off (%s=off) or there is no home directory\n", verb, config.UserSettingsEnv)
			return 1
		}
	}
	before, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		_, _ = fmt.Fprintf(errOut, "itervox config %s: %v\n", verb, readErr)
		return 1
	}
	if err := config.SetSetting(path, key, value); err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox config %s: %v\n", verb, err)
		return 1
	}
	var effective *config.Setting
	if _, statErr := os.Stat(workflow); statErr == nil {
		if _, err := config.Load(workflow); err != nil {
			restoreSettingsFile(path, before, readErr == nil)
			_, _ = fmt.Fprintf(errOut, "itervox config %s: %s does not load with this change, so it was not made: %v\n", verb, workflow, err)
			return 1
		}
		settings, err := config.Explain(workflow)
		if err == nil {
			for i := range settings {
				if settings[i].Key == key {
					effective = &settings[i]
				}
			}
		}
		if effective != nil && !effective.Unread && value != nil && config.ValueKind(value) != config.ValueKind(effective.Value) {
			restoreSettingsFile(path, before, readErr == nil)
			_, _ = fmt.Fprintf(errOut, "itervox config set: %s takes a %s, not %s; nothing was changed\n",
				key, config.ValueKind(effective.Value), formatSettingValue(value))
			return 1
		}
	}
	if value == nil {
		_, _ = fmt.Fprintf(out, "Removed %s from %s.\n", key, path)
	} else {
		_, _ = fmt.Fprintf(out, "Set %s = %s in %s.\n", key, formatSettingValue(value), path)
	}
	layer := config.LayerProject
	if user {
		layer = config.LayerUser
	}
	switch {
	case value != nil && (effective == nil || effective.Unread):
		_, _ = fmt.Fprintf(errOut, "warning: Itervox does not read %s; check the name (itervox config show lists the settings it reads)\n", key)
	case effective == nil:
	case value == nil:
		_, _ = fmt.Fprintf(out, "%s is now %s (%s layer).\n", key, formatSettingValue(effective.Value), effective.Layer)
	case layerRank(effective.Layer) > layerRank(layer):
		_, _ = fmt.Fprintf(errOut, "warning: the %s layer also sets %s (%s), and it wins over this file\n",
			effective.Layer, key, formatSettingValue(effective.Value))
	}
	_, _ = fmt.Fprintln(out, "A running daemon picks it up when it next loads WORKFLOW.md: restart it, or save WORKFLOW.md.")
	return 0
}

// layerRank orders the layers, lowest first.
func layerRank(layer string) int {
	switch layer {
	case config.LayerUser:
		return 1
	case config.LayerProject:
		return 2
	case config.LayerWorkflow:
		return 3
	}
	return 0
}

// truncateSettingValue keeps the table readable; --json prints values whole.
func truncateSettingValue(s string) string {
	const maxRunes = 60
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes-1]) + "…"
	}
	return s
}

func restoreSettingsFile(path string, before []byte, existed bool) {
	if !existed {
		_ = os.Remove(path)
		return
	}
	_ = atomicfs.WriteFile(path, before, 0o644)
}

// parseSettingValue reads a command-line value as JSON (numbers, true/false,
// lists, objects, quoted strings) and anything else as a plain string.
func parseSettingValue(s string) any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return s
	}
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
		f, _ := n.Float64()
		return f
	}
	if v == nil {
		return s // "null" is not a way to remove a key; use unset
	}
	return v
}

func formatSettingValue(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(buf.String())
}
