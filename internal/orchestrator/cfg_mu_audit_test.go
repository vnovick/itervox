// Gap §7.1 — auto-enforce the cfgMu doc-comment invariant via a meta-test.
//
// The cfgMu doc-comment in orchestrator.go enumerates the runtime-mutable
// cfg fields. Each new mutable field SHOULD be added to that comment AND
// always be accessed under cfgMu. A typed MutableConfig wrapper (deferred
// per gaps_010526 §7.1) would enforce the second invariant at the type
// level. Until that lands, this test enforces invariant #1: every
// `o.cfg.<Field> = <value>` assignment in the orchestrator package must
// reference a field named in the documented allowlist below.
//
// When a new runtime-mutable cfg field is added:
//   1. Add it to AllowedMutableCfgFields (this file).
//   2. Add it to the cfgMu doc-comment in orchestrator.go.
//   3. Always read it via the matching getter (e.g. SwitchWindowHoursCfg()).
//
// Direct reads outside this package (e.g. cmd/itervox snapshot builder)
// are intentionally not policed — that's a single boundary the contributor
// can audit by hand.

package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AllowedMutableCfgFields is the canonical list of cfg.X paths the
// orchestrator may mutate at runtime. Mirrored in the cfgMu doc-comment.
//
// Keep this map alphabetically sorted by full field path so diffs between
// this file and the CLAUDE.md / AGENTS.md `cfgMu` sections stay easy to
// review. v0.2.0 audit P2-4.
var AllowedMutableCfgFields = map[string]struct{}{
	"Agent.AutoReview":                   {},
	"Agent.DispatchStrategy":             {},
	"Agent.InlineInput":                  {},
	"Agent.MaxConcurrentAgents":          {},
	"Agent.MaxRetries":                   {},
	"Agent.MaxSwitchesPerIssuePerWindow": {},
	"Agent.Profiles":                     {},
	"Agent.RateLimitErrorPatterns":       {},
	"Agent.ReviewerProfile":              {},
	"Agent.SSHHostDescriptions":          {},
	"Agent.SSHHosts":                     {},
	"Agent.SwitchRevertHours":            {},
	"Agent.SwitchWindowHours":            {},
	"Automations":                        {},
	"Tracker.ActiveStates":               {},
	"Tracker.CompletionState":            {},
	"Tracker.FailedState":                {},
	"Tracker.TerminalStates":             {},
	"Workspace.AutoClearWorkspace":       {},
}

// TestCfgMuFieldAudit walks every .go file in this package and asserts
// every assignment targeting `o.cfg.<X>.<Y>` references a path in
// AllowedMutableCfgFields. Catches new orchestrator.cfg writes that
// forget the cfgMu doc / allowlist update.
func TestCfgMuFieldAudit(t *testing.T) {
	pkgRoot := "."
	files, err := filepath.Glob(filepath.Join(pkgRoot, "*.go"))
	require.NoError(t, err)

	fset := token.NewFileSet()
	var unknownAssignments []string

	for _, f := range files {
		// Skip test files — meta-tests can poke whatever they want.
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		require.NoErrorf(t, err, "read %s", f)
		af, err := parser.ParseFile(fset, f, data, 0)
		if err != nil {
			t.Logf("skipping unparseable file %s: %v", f, err)
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				path := flattenSelector(lhs)
				const prefix = "o.cfg."
				if !strings.HasPrefix(path, prefix) {
					continue
				}
				field := strings.TrimPrefix(path, prefix)
				if _, ok := AllowedMutableCfgFields[field]; !ok {
					unknownAssignments = append(unknownAssignments,
						fset.Position(as.Pos()).String()+": o.cfg."+field)
				}
			}
			return true
		})
	}

	assert.Empty(t, unknownAssignments,
		"every o.cfg.<X> assignment in the orchestrator package must reference a field in AllowedMutableCfgFields. "+
			"If this is a new runtime-mutable field, ALSO update the cfgMu doc-comment in orchestrator.go.")
}

// flattenSelector renders a selector expression as a dotted path, e.g.
// `o.cfg.Agent.MaxRetries`. Anything that's not a SelectorExpr/Ident is
// rendered as "?" so non-selector LHS (like map index, deref) is ignored
// by the prefix check.
func flattenSelector(n ast.Expr) string {
	switch v := n.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return flattenSelector(v.X) + "." + v.Sel.Name
	default:
		return "?"
	}
}
