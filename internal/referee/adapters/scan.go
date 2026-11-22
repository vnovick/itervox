package adapters

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// maxScanFile bounds one scanned file. A larger file is an error, not a
// skip: skipping it would hide whatever it reads.
const maxScanFile = 64 << 20

// scanInstall walks installRoot (a symlinked root, as pnpm installs, is
// followed; symlinks below it are not) and collects the deploy-target
// variables the included files mention.
func scanInstall(installRoot string, include func(string) bool, pattern *regexp.Regexp, deployVar func(string) bool) ([]EnvRead, error) {
	info, err := os.Stat(installRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("install root %s not found (install dependencies before this check)", installRoot)
		}
		return nil, fmt.Errorf("install root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("install root %s is not a directory", installRoot)
	}
	root, err := filepath.EvalSymlinks(installRoot)
	if err != nil {
		return nil, fmt.Errorf("install root: %w", err)
	}
	found := map[string]*EnvRead{}
	scanned := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil // directories are walked; symlinks and devices are not read
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !include(rel) {
			return nil
		}
		raw, err := readBounded(p)
		if err != nil {
			return err
		}
		scanned++
		scanFile(rel, raw, pattern, deployVar, found)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", installRoot, err)
	}
	if scanned == 0 {
		return nil, fmt.Errorf("no file under %s matches the scan (is the tool installed there?)", installRoot)
	}
	out := make([]EnvRead, 0, len(found))
	for _, r := range found {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b EnvRead) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func readBounded(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxScanFile+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxScanFile {
		return nil, fmt.Errorf("%s is larger than %d MiB", p, maxScanFile>>20)
	}
	return raw, nil
}

// scanFile records each deploy-target name in raw, line by line.
func scanFile(rel string, raw []byte, pattern *regexp.Regexp, deployVar func(string) bool, found map[string]*EnvRead) {
	note := func(name string, line int) {
		// A name is recorded without a shell's leading $ (an env_pattern
		// may capture "$NAME"), so it matches deploy_vars and the guard.
		name = strings.TrimLeft(name, "$")
		if name == "" || !deployVar(name) {
			return
		}
		if r, ok := found[name]; ok {
			r.Count++
			return
		}
		found[name] = &EnvRead{Name: name, Path: rel, Line: line, Count: 1}
	}
	for lineNo := 1; len(raw) > 0; lineNo++ {
		line := raw
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			line, raw = raw[:i], raw[i+1:]
		} else {
			raw = nil
		}
		if pattern != nil {
			for _, m := range pattern.FindAllSubmatchIndex(line, -1) {
				if m[2] >= 0 {
					note(string(line[m[2]:m[3]]), lineNo)
				}
			}
			continue
		}
		for _, w := range words(line) {
			note(w, lineNo)
		}
	}
}

// isWordByte reports whether c is part of a variable name. $ never is: a
// shell reads $NAME and ${NAME}, and both must yield NAME, or a shell
// install file's reads are missed (envcheck would fail open) and a shell
// guard's [ -z "$NAME" ] would classify nothing.
func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// words splits b into identifier-like words ([A-Za-z0-9_]+), so a name is
// matched whole: CONVEX_DEPLOY is not a mention inside CONVEX_DEPLOY_KEY,
// and $NAME / ${NAME} are the word NAME.
func words(b []byte) []string {
	var out []string
	for i := 0; i < len(b); {
		if !isWordByte(b[i]) {
			i++
			continue
		}
		j := i
		for j < len(b) && isWordByte(b[j]) {
			j++
		}
		out = append(out, string(b[i:j]))
		i = j
	}
	return out
}

// Mentions is the set of whole words in text: what a guard file
// classifies. A variable is classified by a guard that mentions its name
// anywhere (code, a list, or a comment a person wrote).
func Mentions(text []byte) map[string]bool {
	out := map[string]bool{}
	for _, w := range words(text) {
		out[w] = true
	}
	return out
}
