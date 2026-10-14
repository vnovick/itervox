package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/rules"
	"github.com/vnovick/itervox/internal/server"
)

// TestRulesFilesGitignorePolicy (#157): `itervox init` keeps the project
// rules layer committable and the project-local layer ignored, with a root
// .gitignore that hides .itervox/ and with one that does not; `init
// --update` (which patches only the root .gitignore) ignores the local layer
// too.
func TestRulesFilesGitignorePolicy(t *testing.T) {
	for name, root := range map[string]string{"root hides .itervox": ".itervox/\n", "plain root": "node_modules/\n"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(root), 0o644))
			require.NoError(t, finalizeItervoxGitignore(filepath.Join(dir, ".itervox")))
			for _, f := range []string{"rules.json", "rules.local.json", "settings.jsonc", "tracker_store.json"} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", f), []byte("{}\n"), 0o644))
			}
			assert.False(t, gitPathIgnored(t, dir, ".itervox/rules.json"), "the project rules layer is committable")
			assert.True(t, gitPathIgnored(t, dir, ".itervox/rules.local.json"), "the project-local rules layer is ignored")
			assert.False(t, gitPathIgnored(t, dir, ".itervox/settings.jsonc"))
			assert.True(t, gitPathIgnored(t, dir, ".itervox/tracker_store.json"), "runtime JSON stays ignored")
		})
	}

	t.Run("init --update root patch", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".itervox/\n"), 0o644))
		require.NoError(t, patchRootGitignoreForAgents(dir))
		require.NoError(t, patchRootGitignoreForAgents(dir)) // idempotent
		body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(string(body), rulesLocalGitignoreEntry))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox"), 0o755))
		assert.True(t, gitPathIgnored(t, dir, ".itervox/rules.local.json"))
		assert.False(t, gitPathIgnored(t, dir, ".itervox/rules.json"))
	})
}

func rulesTestServer(t *testing.T, wf string) (*server.Server, *config.Config) {
	t.Helper()
	cfg := &config.Config{WorkflowPath: wf, PromptTemplate: "Work on {{ issue.identifier }}"}
	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		Rules:        &rulesAdapter{cfg: cfg},
		AllowedHosts: []string{"example.com"},
	})
	return srv, cfg
}

func rulesRequest(t *testing.T, srv *server.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestRulesEditorEndpointsEditEachLayerKeepCommentsAndPreview (#157): the
// editor's endpoints list every layer, write each writable layer through the
// comment-preserving writer (form view) or as typed (JSON view), refuse a
// stale or invalid save, and preview the effective rules.
func TestRulesEditorEndpointsEditEachLayerKeepCommentsAndPreview(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "acme", "WORKFLOW.md")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "acme", ".itervox"), 0o755))
	require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))
	t.Setenv(rules.UserRulesEnv, filepath.Join(root, "home", ".itervox", "rules.json"))
	project := rules.ProjectPath(wf)
	require.NoError(t, os.WriteFile(project, []byte(`// Team rules: reviewed in PRs.
{
  "rules": [
    // Lockfile is pnpm.
    {"id": "pnpm", "severity": "must", "text": "Use pnpm."},
  ],
}
`), 0o644))
	srv, _ := rulesTestServer(t, wf)

	rec := rulesRequest(t, srv, http.MethodGet, "/api/v1/rules", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var view server.RulesView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	require.Len(t, view.Layers, 3)
	assert.Equal(t, rules.SchemaURL, view.SchemaURL)
	byName := map[string]server.RulesLayerView{}
	for _, l := range view.Layers {
		byName[l.Name] = l
		assert.True(t, l.Writable, l.Name)
	}
	require.True(t, byName["project"].Exists)
	assert.Equal(t, "pnpm", byName["project"].Rules[0].ID)
	assert.False(t, byName["user"].Exists)

	// Form view on the project layer: comments survive.
	newRules := []rules.Rule{
		{ID: "pnpm", Severity: rules.SeverityMust, Text: "Use pnpm 9."},
		{ID: "small-prs", Severity: rules.SeverityShould, Text: "Keep PRs small."},
	}
	rec = rulesRequest(t, srv, http.MethodPut, "/api/v1/rules/project",
		map[string]any{"baseVersion": byName["project"].Version, "rules": newRules})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	onDisk, err := os.ReadFile(project)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "// Team rules: reviewed in PRs.")
	assert.Contains(t, string(onDisk), "// Lockfile is pnpm.")
	assert.Contains(t, string(onDisk), "Use pnpm 9.")

	// The old version is now stale.
	rec = rulesRequest(t, srv, http.MethodPut, "/api/v1/rules/project",
		map[string]any{"baseVersion": byName["project"].Version, "rules": newRules})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	// JSON view on the user and local layers (created; text kept as typed).
	userRaw := "// mine\n{\"rules\": [{\"id\": \"pnpm\", \"severity\": \"must\", \"text\": \"Always pnpm.\"}]}\n"
	rec = rulesRequest(t, srv, http.MethodPut, "/api/v1/rules/user", map[string]any{"baseVersion": "", "raw": userRaw})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := os.ReadFile(rules.UserPath())
	require.NoError(t, err)
	assert.Equal(t, userRaw, string(got))
	rec = rulesRequest(t, srv, http.MethodPut, "/api/v1/rules/local",
		map[string]any{"baseVersion": "", "rules": []rules.Rule{{ID: "port", Severity: rules.SeverityMust, Text: "Port 5174."}}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Invalid JSON view and unknown layer are refused.
	rec = rulesRequest(t, srv, http.MethodPut, "/api/v1/rules/local",
		map[string]any{"baseVersion": "x", "raw": `{"rules":[{"id":"a"}]}`})
	assert.Equal(t, http.StatusConflict, rec.Code, "the version is checked first")
	var local server.RulesLayerView
	rec = rulesRequest(t, srv, http.MethodGet, "/api/v1/rules", nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	for _, l := range view.Layers {
		if l.Name == "local" {
			local = l
		}
	}
	rec = rulesRequest(t, srv, http.MethodPut, "/api/v1/rules/local",
		map[string]any{"baseVersion": local.Version, "raw": `{"rules":[{"id":"a"}]}`})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "rules_invalid")
	rec = rulesRequest(t, srv, http.MethodPut, "/api/v1/rules/org", map[string]any{"raw": "{}"})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Preview: the effective rules, the user must rule kept over the
	// project's, the conflict reported.
	rec = rulesRequest(t, srv, http.MethodGet, "/api/v1/rules/preview?profile=implementer", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var preview server.RulesPreview
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &preview))
	assert.Equal(t, []string{"pnpm", "port", "small-prs"}, preview.IDs)
	assert.Equal(t, "user", preview.Rules[0].Layer)
	require.Len(t, preview.Conflicts, 1)
	assert.Equal(t, "project", preview.Conflicts[0].Layer)
	assert.Contains(t, preview.Block, "## Rules")
	assert.Equal(t, "claude", preview.Backend)
	assert.Equal(t, []string{"acme"}, preview.Projects)
	assert.False(t, preview.TemplatePlacesRules)
}

// TestAnalyzerRulesBlockReadsLayersForAnalyzerProfile (#157): the dependency
// analyzer's pass gets the rules in scope for its profile.
func TestAnalyzerRulesBlockReadsLayersForAnalyzerProfile(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, "acme", "WORKFLOW.md")
	t.Setenv(rules.UserRulesEnv, "off")
	require.NoError(t, os.MkdirAll(filepath.Dir(rules.ProjectPath(wf)), 0o755))
	require.NoError(t, os.WriteFile(rules.ProjectPath(wf), []byte(`{"rules":[
		{"id":"all","severity":"must","text":"EVERYONE."},
		{"id":"analyzer","severity":"should","text":"ANALYZER-ONLY.","scope":{"profiles":["deps-analyzer"]}},
		{"id":"impl","severity":"should","text":"IMPL-ONLY.","scope":{"profiles":["implementer"]}}
	]}`), 0o644))
	cfg := &config.Config{WorkflowPath: wf}
	block := analyzerRulesBlock(cfg, "deps-analyzer", config.AgentProfile{Command: "claude"}, slog.Default())
	assert.Contains(t, block, "EVERYONE.")
	assert.Contains(t, block, "ANALYZER-ONLY.")
	assert.NotContains(t, block, "IMPL-ONLY.")
}
