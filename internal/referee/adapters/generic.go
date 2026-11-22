package adapters

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// loadGeneric builds an adapter described entirely by its tools: entry.
func loadGeneric(name string, c Config) (*adapter, error) {
	if c.EnvScan == "" {
		return nil, fmt.Errorf("%s is not a built-in adapter (%s): a generic tool needs env_scan and deploy_vars", name, strings.Join(BuiltinNames(), ", "))
	}
	if c.DeployVars == nil {
		return nil, fmt.Errorf("%s: a generic tool needs deploy_vars (the variable names that choose where it deploys)", name)
	}
	scan, err := cleanRel(c.EnvScan)
	if err != nil {
		return nil, fmt.Errorf("env_scan: %w", err)
	}
	if err := validateGlob(scan); err != nil {
		return nil, fmt.Errorf("env_scan: %w", err)
	}
	install, rest := splitGlob(scan)
	if c.Install != "" {
		if install, err = cleanRel(c.Install); err != nil {
			return nil, fmt.Errorf("install: %w", err)
		}
		r, ok := strings.CutPrefix(scan, install+"/")
		if !ok {
			return nil, fmt.Errorf("env_scan %q is not under install %q", scan, install)
		}
		rest = r
	}
	if install == "" {
		return nil, fmt.Errorf("env_scan %q: start with the directory the tool is installed in (e.g. node_modules/%s/…)", scan, name)
	}
	cmd := name
	if c.Command != "" {
		cmd = c.Command
	}
	if !command.MatchString(cmd) {
		return nil, fmt.Errorf("command %q: use lowercase letters, digits, ., - and _", cmd)
	}
	a := &adapter{name: name, command: cmd, install: install}
	if a.deployVars, err = parseDeployVars(*c.DeployVars); err != nil {
		return nil, err
	}
	var probes []string
	if c.Probes != nil {
		probes = *c.Probes
	}
	if a.probes, err = parseProbes(cmd, probes); err != nil {
		return nil, err
	}
	if c.EnvPattern != "" {
		if len(c.EnvPattern) > 1024 {
			return nil, errors.New("env_pattern: longer than 1024 bytes")
		}
		re, err := regexp.Compile(c.EnvPattern)
		if err != nil {
			return nil, fmt.Errorf("env_pattern: %w", err)
		}
		if re.NumSubexp() < 1 {
			return nil, fmt.Errorf("env_pattern %q: needs a group that captures the variable name", c.EnvPattern)
		}
		a.pattern = re
	}
	segs := strings.Split(rest, "/")
	a.include = func(rel string) bool { return matchSegments(segs, strings.Split(rel, "/")) }
	a.detect = func(repo string) bool {
		_, err := os.Stat(filepath.Join(repo, filepath.FromSlash(install)))
		return err == nil
	}
	return a, nil
}

func hasMeta(seg string) bool { return strings.ContainsAny(seg, "*?[") }

// splitGlob splits a glob into its literal directory prefix and the rest:
// node_modules/mycli/dist/**/*.js → node_modules/mycli/dist, **/*.js. A
// glob with no wildcard splits at its last slash.
func splitGlob(p string) (dir, rest string) {
	segs := strings.Split(p, "/")
	i := 0
	for i < len(segs)-1 && !hasMeta(segs[i]) {
		i++
	}
	return strings.Join(segs[:i], "/"), strings.Join(segs[i:], "/")
}

func validateGlob(p string) error {
	for _, seg := range strings.Split(p, "/") {
		if seg == "**" {
			continue
		}
		if strings.Contains(seg, "**") {
			return fmt.Errorf("%q: ** must be a whole path segment", p)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("%q: %w", p, err)
		}
	}
	return nil
}

// matchSegments matches path segments against glob segments; a `**`
// segment matches any number of segments, including none.
func matchSegments(glob, segs []string) bool {
	if len(glob) == 0 {
		return len(segs) == 0
	}
	if glob[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegments(glob[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, _ := path.Match(glob[0], segs[0]); !ok {
		return false
	}
	return matchSegments(glob[1:], segs[1:])
}
