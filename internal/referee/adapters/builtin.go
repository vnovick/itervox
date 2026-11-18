package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// builtinSpec is a built-in adapter: an npm-installed deploy CLI. Each is
// deliberately small: the package, its command, the prefix its
// deploy-target variables share, a few read-only probes and the config
// files that show it is used.
type builtinSpec struct {
	pkg, command string
	deployVars   []string
	probes       []string
	configFiles  []string
}

var builtins = map[string]builtinSpec{
	"convex": {
		pkg: "convex", command: "convex",
		deployVars:  []string{"CONVEX_*"},
		probes:      []string{"convex --version", "convex deploy --help"},
		configFiles: []string{"convex.json"},
	},
	"vercel": {
		pkg: "vercel", command: "vercel",
		deployVars:  []string{"VERCEL_*"},
		probes:      []string{"vercel --version", "vercel deploy --help"},
		configFiles: []string{"vercel.json", ".vercel/project.json"},
	},
}

// BuiltinNames lists the built-in adapters, sorted.
func BuiltinNames() []string {
	names := make([]string, 0, len(builtins))
	for n := range builtins {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Builtins returns each built-in adapter with its default settings, sorted
// by name (for Detect: a tool the repository uses that tools: leaves out).
func Builtins() []Adapter {
	var out []Adapter
	for _, n := range BuiltinNames() {
		a, err := loadBuiltin(n, builtins[n], Config{})
		if err != nil { // the built-in table itself is invalid
			panic(fmt.Sprintf("adapters: built-in %s: %v", n, err))
		}
		out = append(out, a)
	}
	return out
}

func loadBuiltin(name string, spec builtinSpec, c Config) (*adapter, error) {
	switch {
	case c.EnvScan != "", c.EnvPattern != "", c.Command != "":
		return nil, fmt.Errorf("%s is a built-in adapter: env_scan, env_pattern and command are for a generic tool (use another name)", name)
	}
	a := &adapter{name: name, command: spec.command, install: "node_modules/" + spec.pkg, include: isJS}
	if c.Install != "" {
		p, err := cleanRel(c.Install)
		if err != nil {
			return nil, fmt.Errorf("install: %w", err)
		}
		a.install = p
	}
	vars := spec.deployVars
	if c.DeployVars != nil {
		vars = *c.DeployVars
	}
	var err error
	if a.deployVars, err = parseDeployVars(vars); err != nil {
		return nil, err
	}
	probes := spec.probes
	if c.Probes != nil {
		probes = *c.Probes
	}
	if a.probes, err = parseProbes(a.command, probes); err != nil {
		return nil, err
	}
	a.detect = func(repo string) bool {
		if dependsOn(repo, spec.pkg) {
			return true
		}
		for _, f := range spec.configFiles {
			if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(f))); err == nil {
				return true
			}
		}
		return false
	}
	return a, nil
}

func isJS(rel string) bool {
	for _, ext := range []string{".js", ".cjs", ".mjs"} {
		if strings.HasSuffix(rel, ext) {
			return true
		}
	}
	return false
}

// dependsOn reports whether repo's package.json lists pkg in any of its
// dependency maps.
func dependsOn(repo, pkg string) bool {
	raw, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		var deps map[string]json.RawMessage
		if json.Unmarshal(m[key], &deps) != nil {
			continue
		}
		if _, ok := deps[pkg]; ok {
			return true
		}
	}
	return false
}
