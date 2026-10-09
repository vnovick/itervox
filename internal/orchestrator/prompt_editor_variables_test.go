package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/prompt"
)

// TestPromptEditorVariablesAreBound (#87): every Liquid variable the
// dashboard's prompt editor suggests is one the daemon binds — PROFILE_VARIABLES
// for profile prompts, TRIGGER_VARIABLES for automation instructions — so the
// autocomplete list cannot drift from the renderer.
func TestPromptEditorVariablesAreBound(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "Settings", "profiles", "promptCompletions.ts"))
	require.NoError(t, err)
	list := func(name string) []string {
		start := strings.Index(string(src), "export const "+name)
		require.GreaterOrEqual(t, start, 0, name)
		block := string(src)[start:]
		block = block[:strings.Index(block, "];")]
		var out []string
		for _, m := range regexp.MustCompile(`v\('([^']+)'`).FindAllStringSubmatch(block, -1) {
			out = append(out, m[1])
		}
		require.NotEmpty(t, out, name)
		return out
	}

	s, n := "x", 1
	now := time.Now()
	issue := domain.Issue{ID: s, Identifier: s, Title: s, Description: &s, Priority: &n, State: s, BranchName: &s,
		URL: &s, Labels: []string{s}, BlockedBy: []domain.BlockerRef{{ID: &s}},
		Comments: []domain.Comment{{Body: s}}, CreatedAt: &now, UpdatedAt: &now}
	bindings := runBindings("t", "h", "b", "e", nil)
	for k, v := range automationTriggerBindings(&AutomationDispatch{Trigger: AutomationTriggerContext{
		FiredAt: now, ResolvedBlockers: []domain.BlockerRef{{ID: &s}}}}) {
		bindings[k] = v
	}
	check := func(vars []string) {
		var tpl strings.Builder
		for _, v := range vars {
			tpl.WriteString("{% if " + v + " == nil %}MISSING:" + v + " {% endif %}")
		}
		out := prompt.RenderPromptOverlay(tpl.String(), issue, &n, bindings)
		require.NotContains(t, out, "{%", "the probe template must render")
		require.Empty(t, strings.TrimSpace(out), "suggested but not bound: %s", out)
	}
	check(list("PROFILE_VARIABLES"))
	check(list("TRIGGER_VARIABLES"))
}
