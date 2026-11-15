package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// sandboxExecPath is macOS's sandbox-exec.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// sandboxExec confines an agent with macOS sandbox-exec and a generated
// profile (#258): everything allowed but file writes outside the writable
// paths, and file reads and writes on the deny list. sandbox-exec is
// deprecated by Apple but still ships and is what the platform's own tools
// use; the profile is passed inline (-p), nothing is written to disk.
type sandboxExec struct{}

// newSandboxExec returns the sandbox-exec mechanism.
func newSandboxExec() IsolationMechanism { return sandboxExec{} }

func (sandboxExec) Name() string { return "sandbox-exec" }

func (sandboxExec) Available() error {
	if _, err := exec.LookPath(sandboxExecPath); err != nil {
		return errors.New("sandbox-exec is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, sandboxExecPath, "-p", sandboxExecProfile(IsolationPolicy{}), "/usr/bin/true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox-exec cannot apply a profile here (%v): %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (sandboxExec) Wrap(argv []string, p IsolationPolicy) ([]string, error) {
	return append([]string{sandboxExecPath, "-p", sandboxExecProfile(p)}, argv...), nil
}

// sandboxExecProfile returns the SBPL profile enforcing p. sandbox-exec
// applies the LAST matching rule, so the rules follow the policy order
// (shallowest first, see IsolationPolicy.orderedRules) and a deeper rule
// wins. A writable file also allows its siblings that extend its name
// (~/.claude.json.tmp, ~/.claude.json.lock), which CLIs use to replace a
// file atomically. A path re-exposed inside a denied directory keeps its
// ancestors' metadata readable so lookups through them work.
func sandboxExecProfile(p IsolationPolicy) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n")
	b.WriteString("(allow file-write* (subpath \"/dev\"))\n")
	rules := p.orderedRules()
	var denied []string
	for _, r := range rules {
		filter := fmt.Sprintf("(subpath %s)", sbplString(r.path))
		if !r.dir {
			filter = fmt.Sprintf("(literal %s)", sbplString(r.path))
		}
		switch r.kind {
		case ruleWrite:
			if !r.dir {
				filter = fmt.Sprintf("(regex %s)", sbplString("^"+regexQuote(r.path)))
			}
			fmt.Fprintf(&b, "(allow file-read* file-write* %s)\n", filter)
		case ruleRead:
			fmt.Fprintf(&b, "(allow file-read* %s)\n", filter)
		case ruleDeny:
			fmt.Fprintf(&b, "(deny file-read* file-write* %s)\n", filter)
			if r.dir {
				denied = append(denied, r.path)
			}
		}
	}
	var ancestors []string
	for _, r := range rules {
		if r.kind == ruleDeny {
			continue
		}
		for _, d := range denied {
			if r.path == d || !within(r.path, d) {
				continue
			}
			for a := filepath.Dir(r.path); within(a, d); a = filepath.Dir(a) {
				ancestors = append(ancestors, a)
				if a == d {
					break
				}
			}
		}
	}
	for _, a := range dedupe(ancestors) {
		fmt.Fprintf(&b, "(allow file-read-metadata (literal %s))\n", sbplString(a))
	}
	return b.String()
}

// sbplString quotes s as an SBPL string literal.
func sbplString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// regexQuote escapes the regular-expression metacharacters in s.
func regexQuote(s string) string {
	var b strings.Builder
	for _, c := range s {
		if strings.ContainsRune(`.[]()*+?{}|^$\`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}
