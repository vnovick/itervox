//go:build linux

package sysmon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Tracepoint field layouts from tracefs, so precise mode's programs follow
// the running kernel.

// tracefsRoots are where tracefs is looked for, in order.
var tracefsRoots = []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing"}

// errNoTracefs: tracefs is not mounted (or not readable).
var errNoTracefs = errors.New("tracefs is not mounted at /sys/kernel/tracing")

type tpField struct{ offset, size int }

// parseTracepointFormat reads the field offsets of a tracefs format file.
func parseTracepointFormat(b []byte) map[string]tpField {
	out := map[string]tpField{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "field:") {
			continue
		}
		var decl string
		f := tpField{offset: -1}
		for _, part := range strings.Split(line, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), ":")
			if !ok {
				continue
			}
			switch k {
			case "field":
				decl = v
			case "offset":
				f.offset, _ = strconv.Atoi(v)
			case "size":
				f.size, _ = strconv.Atoi(v)
			}
		}
		decl = strings.TrimSpace(decl)
		if i := strings.IndexByte(decl, '['); i >= 0 && !strings.HasPrefix(decl, "__data_loc") {
			decl = decl[:i]
		}
		fs := strings.Fields(decl)
		if len(fs) == 0 || f.offset < 0 {
			continue
		}
		name := strings.TrimLeft(fs[len(fs)-1], "*")
		if i := strings.IndexByte(name, '['); i >= 0 {
			name = name[:i]
		}
		out[name] = f
	}
	return out
}

func tracefsRoot() (string, error) {
	for _, r := range tracefsRoots {
		if _, err := os.Stat(filepath.Join(r, "events", "sched")); err == nil {
			return r, nil
		}
	}
	return "", errNoTracefs
}

func tracepointFields(root, group, name string, want ...string) (map[string]tpField, error) {
	b, err := os.ReadFile(filepath.Join(root, "events", group, name, "format"))
	if err != nil {
		return nil, fmt.Errorf("tracepoint %s/%s: %w", group, name, err)
	}
	fields := parseTracepointFormat(b)
	for _, w := range want {
		if _, ok := fields[w]; !ok {
			return nil, fmt.Errorf("tracepoint %s/%s has no field %q", group, name, w)
		}
	}
	return fields, nil
}
