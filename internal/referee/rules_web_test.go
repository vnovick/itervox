package referee

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuditWebRulesOnRepo: on a real repository, a TypeScript change that
// adds @ts-ignore, turns off strict and an ESLint hooks rule exits 20 with
// those findings; the same files changed harmlessly exit 0.
func TestAuditWebRulesOnRepo(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{
		"tsconfig.json":    "{\n  \"compilerOptions\": {\n    \"strict\": true\n  }\n}\n",
		"eslint.config.js": "export default [{\n  rules: {\n    'react-hooks/rules-of-hooks': 'error',\n  },\n}];\n",
		"src/App.tsx":      "export const App = () => null;\n",
	})
	r.git("checkout", "-q", "-b", "clean")
	r.commit("clean", map[string]string{"src/App.tsx": "// The app.\nexport const App = () => null;\n"})
	code, out, errOut := referee(t, nil, "audit", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 0, code, out+errOut)

	r.git("checkout", "-q", "-b", "weaken", "main")
	r.commit("weaken", map[string]string{
		"tsconfig.json":    "{\n  \"compilerOptions\": {\n    \"strict\": false\n  }\n}\n",
		"eslint.config.js": "export default [{\n  rules: {\n    'react-hooks/rules-of-hooks': 'off',\n  },\n}];\n",
		"src/App.tsx":      "// @ts-ignore\nexport const App = () => null;\n",
	})
	code, out, errOut = referee(t, nil, "audit", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep auditReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	got := map[string]finding{}
	for _, f := range rep.Findings {
		got[f.Rule] = f
	}
	require.Len(t, rep.Findings, 3, "%+v", rep.Findings)
	assert.Equal(t, finding{Rule: "eslint_rule_off", Path: "eslint.config.js", Line: 3, Message: got["eslint_rule_off"].Message, Text: "    'react-hooks/rules-of-hooks': 'off',"}, got["eslint_rule_off"])
	assert.Equal(t, "src/App.tsx", got["ts_ignore"].Path)
	assert.Equal(t, 1, got["ts_ignore"].Line)
	assert.Equal(t, "tsconfig.json", got["ts_strict_off"].Path)
	assert.Equal(t, 3, got["ts_strict_off"].Line)
}

// TestAuditMaxWarningsCommaChangeOnRepo: on a real repository, adding a
// "lint:fix" script after the lint script (only a comma added to the lint
// line) and an eslint plugin dependency has no finding and exits 0;
// raising the flag exits 20 at the lint line.
func TestAuditMaxWarningsCommaChangeOnRepo(t *testing.T) {
	r := newTestRepo(t)
	r.commit("base", map[string]string{
		"package.json": "{\n  \"scripts\": {\n    \"lint\": \"eslint . --max-warnings 0\"\n  },\n  \"devDependencies\": {\n    \"eslint\": \"^9.0.0\"\n  }\n}\n",
	})
	r.git("checkout", "-q", "-b", "fix-script")
	r.commit("lint:fix", map[string]string{
		"package.json": "{\n  \"scripts\": {\n    \"lint\": \"eslint . --max-warnings 0\",\n    \"lint:fix\": \"eslint . --fix\"\n  },\n  \"devDependencies\": {\n    \"eslint\": \"^9.0.0\",\n    \"eslint-plugin-react\": \"^7.37.0\"\n  }\n}\n",
	})
	code, out, errOut := referee(t, nil, "audit", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 0, code, out+errOut)

	r.git("checkout", "-q", "-b", "raise", "main")
	r.commit("raise", map[string]string{
		"package.json": "{\n  \"scripts\": {\n    \"lint\": \"eslint . --max-warnings 40\",\n    \"lint:fix\": \"eslint . --fix\"\n  },\n  \"devDependencies\": {\n    \"eslint\": \"^9.0.0\"\n  }\n}\n",
	})
	code, out, errOut = referee(t, nil, "audit", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep auditReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Findings, 1, "%+v", rep.Findings)
	assert.Equal(t, "eslint_max_warnings", rep.Findings[0].Rule)
	assert.Equal(t, 3, rep.Findings[0].Line)
}

// TestAuditTestPathIgnoredOnRepo: one test file added to Vitest's
// multi-line test exclude on a real repository (git's context lines carry
// the opener) exits 20 at that line; one added to coverage.exclude exits 0.
func TestAuditTestPathIgnoredOnRepo(t *testing.T) {
	r := newTestRepo(t)
	base := "import { defineConfig } from 'vitest/config';\n\nexport default defineConfig({\n  test: {\n    exclude: [\n      'node_modules',\n%s    ],\n    coverage: {\n      exclude: [\n        'src/main.tsx',\n%s      ],\n    },\n  },\n});\n"
	r.commit("base", map[string]string{"vitest.config.ts": fmt.Sprintf(base, "", "")})
	r.git("checkout", "-q", "-b", "coverage-only")
	r.commit("coverage", map[string]string{"vitest.config.ts": fmt.Sprintf(base, "", "        'src/main.test.tsx',\n")})
	code, out, errOut := referee(t, nil, "audit", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 0, code, out+errOut)

	r.git("checkout", "-q", "-b", "exclude", "main")
	r.commit("exclude", map[string]string{"vitest.config.ts": fmt.Sprintf(base, "      'src/flaky.test.ts',\n", "")})
	code, out, errOut = referee(t, nil, "audit", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep auditReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Findings, 1, "%+v", rep.Findings)
	assert.Equal(t, finding{Rule: "test_path_ignored", Path: "vitest.config.ts", Line: 7, Message: rep.Findings[0].Message, Text: "      'src/flaky.test.ts',"}, rep.Findings[0])
}
