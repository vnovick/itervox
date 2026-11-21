// Package adapters describes the deploy and build tools referee checks:
// the `tools:` section of .referee.yml (read from the base ref, never the
// working tree). An adapter knows, for one tool, which installed files to
// scan for the environment variables it mentions, which of those variables
// choose where it deploys ("deploy-target variables"), and which commands
// are safe to run to compare two installed versions (probes).
//
// A tool is either a built-in adapter (Builtins) or a generic one, described
// entirely in the policy (env_scan, env_pattern, deploy_vars, probes).
//
// This package imports only the standard library (gitexec is the one
// internal package it may add). It is used by internal/referee alone; the
// daemon never imports it (TestAdaptersImportGuard).
package adapters

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Adapter is one tool referee knows how to inspect.
type Adapter interface {
	// Name is the tool's key under tools: ("convex", "vercel", "mycli").
	Name() string
	// Detect reports whether the repository at repo shows the tool is used
	// (a manifest dependency or a config file). It only reads files.
	Detect(repo string) bool
	// Probes are safe commands whose output a behaviour diff compares
	// between two installed versions. Parsed and validated, not run here.
	Probes() []Probe
	// ProbeSetup says what the probes need installed beyond the package's
	// own files (a native binary an install script downloads), or "" when
	// an install without scripts is enough.
	ProbeSetup() string
	// InstallRoot is the repository-relative directory the tool is
	// installed in (e.g. node_modules/convex).
	InstallRoot() string
	// DeployVar reports whether an environment variable name is one that
	// chooses where (or as whom) the tool deploys.
	DeployVar(name string) bool
	// EnvReads scans the tool installed at installRoot (a directory on disk)
	// and returns the deploy-target variables it mentions, one entry per
	// name (its first location), sorted by name. An unreadable install, or
	// one with no file to scan, is an error: never an empty result.
	EnvReads(installRoot string) ([]EnvRead, error)
}

// EnvRead is a deploy-target variable an installed tool mentions.
type EnvRead struct {
	Name string `json:"variable"`
	// Path is the file, slash-separated and relative to the install root.
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Count int    `json:"count"` // mentions across all scanned files
}

// Probe is one safe command. Argv[0] is the tool's command (resolved by
// whoever runs it, e.g. node_modules/.bin/<command>); there is no shell.
type Probe struct {
	Text string   `json:"text"` // as written in the policy
	Argv []string `json:"argv"`
}

// Config is one tools: entry as written in .referee.yml. Pointer lists tell
// an absent key (the default) from an explicit [] (refused where it would
// turn the check off).
type Config struct {
	// Guard is the repository file that classifies deploy-target variables
	// (e.g. a deploy guard script). A variable is classified when the guard
	// mentions its name. Required.
	Guard string `yaml:"guard"`
	// Install overrides the install root (built-in default
	// node_modules/<package>; generic default: env_scan's directory part).
	Install string `yaml:"install"`
	// EnvScan (generic only, required) is a repository-relative glob of the
	// installed files to scan; `**` spans directories.
	EnvScan string `yaml:"env_scan"`
	// EnvPattern (generic only) is a regular expression whose first group is
	// a variable name. Without it every word that matches deploy_vars is a
	// mention (the built-in adapters' mode).
	EnvPattern string `yaml:"env_pattern"`
	// DeployVars are globs over variable names (`*`, `?`). Generic: required;
	// built-in: replaces the adapter's own list.
	DeployVars *[]string `yaml:"deploy_vars"`
	// Probes replace the built-in adapter's probes (generic: its probes).
	Probes *[]string `yaml:"probes"`
	// Command (generic only) is the tool's executable name; probes must
	// start with it. Default: the tool's key.
	Command string `yaml:"command"`
}

// Tool is a configured tools: entry: its adapter and its guard file.
type Tool struct {
	Adapter
	Guard string
}

var toolName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Load validates a tools: entry and returns its adapter. A name that is not
// a built-in adapter is a generic tool and must say what to scan.
func Load(name string, c Config) (Tool, error) {
	if !toolName.MatchString(name) {
		return Tool{}, fmt.Errorf("tool name %q: use lowercase letters, digits, - and _", name)
	}
	guard, err := cleanRel(c.Guard)
	if err != nil {
		return Tool{}, fmt.Errorf("guard: %w (the file that classifies the tool's deploy-target variables)", err)
	}
	var a *adapter
	if spec, ok := builtins[name]; ok {
		a, err = loadBuiltin(name, spec, c)
	} else {
		a, err = loadGeneric(name, c)
	}
	if err != nil {
		return Tool{}, err
	}
	return Tool{Adapter: a, Guard: guard}, nil
}

// adapter implements Adapter for built-in and generic tools alike: they
// differ only in where their settings come from.
type adapter struct {
	name, command, install string
	deployVars             []string
	probes                 []Probe
	// include reports whether a file (slash path relative to the install
	// root) is scanned.
	include func(rel string) bool
	// probeSetup is ProbeSetup (built-ins that run a native binary).
	probeSetup string
	// pattern is nil in word mode; else group 1 is a variable name.
	pattern *regexp.Regexp
	detect  func(repo string) bool
}

func (a *adapter) Name() string            { return a.name }
func (a *adapter) Detect(repo string) bool { return a.detect(repo) }
func (a *adapter) Probes() []Probe         { return slices.Clone(a.probes) }
func (a *adapter) InstallRoot() string     { return a.install }
func (a *adapter) ProbeSetup() string      { return a.probeSetup }

func (a *adapter) DeployVar(name string) bool {
	for _, g := range a.deployVars {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

func (a *adapter) EnvReads(installRoot string) ([]EnvRead, error) {
	return scanInstall(installRoot, a.include, a.pattern, a.DeployVar)
}

var (
	varGlob    = regexp.MustCompile(`^[A-Za-z0-9_*?]{1,128}$`)
	command    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	probeChars = regexp.MustCompile(`^[A-Za-z0-9 _./=:,@+%-]+$`)
)

const maxProbes = 32

func parseDeployVars(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("deploy_vars is empty: nothing would be checked")
	}
	var out []string
	for _, g := range raw {
		g = strings.TrimSpace(g)
		if !varGlob.MatchString(g) {
			return nil, fmt.Errorf("deploy_vars: %q is not a variable-name glob (letters, digits, _, * and ?)", g)
		}
		if !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out, nil
}

// ParseProbe parses one probe. It is split on spaces and runs without a
// shell, so quotes, $, ;, |, &, redirections and globs are refused rather
// than passed through; it must start with the tool's command.
func ParseProbe(cmd, text string) (Probe, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 512 {
		return Probe{}, fmt.Errorf("probe %q: empty or longer than 512 bytes", text)
	}
	if !probeChars.MatchString(text) {
		return Probe{}, fmt.Errorf("probe %q: only letters, digits, spaces and _ . / = : , @ + %% - are allowed (it runs without a shell)", text)
	}
	argv := strings.Fields(text)
	if argv[0] != cmd {
		return Probe{}, fmt.Errorf("probe %q: must start with the tool's command %q", text, cmd)
	}
	return Probe{Text: text, Argv: argv}, nil
}

func parseProbes(cmd string, raw []string) ([]Probe, error) {
	if len(raw) > maxProbes {
		return nil, fmt.Errorf("probes: at most %d", maxProbes)
	}
	out := []Probe{}
	for _, t := range raw {
		p, err := ParseProbe(cmd, t)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// cleanRel validates a repository-relative path: not empty, not absolute,
// no `..`, no backslashes, already clean.
func cleanRel(p string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", errors.New("missing")
	case strings.ContainsAny(p, "\\\x00"), strings.HasPrefix(p, "/"), path.Clean(p) != p, p == ".":
		return "", fmt.Errorf("%q must be a clean repository-relative path", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%q must stay inside the repository", p)
		}
	}
	return p, nil
}
