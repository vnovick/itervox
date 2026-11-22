package referee

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuditTestPathIgnoredReadsConfigsWhole: `referee audit` reads a
// changed Jest config whole at both ends, so a test file pattern appended
// to a testPathIgnorePatterns array opened 30 lines above the change (out
// of the diff's context) exits 20 at that line, and removing one exits 0.
func TestAuditTestPathIgnoredReadsConfigsWhole(t *testing.T) {
	r := newTestRepo(t)
	var entries strings.Builder
	for i := range 30 {
		fmt.Fprintf(&entries, "    '/fixtures/case-%02d/',\n", i)
	}
	cfg := "module.exports = {\n  testPathIgnorePatterns: [\n" + entries.String() + "%s  ],\n};\n"
	r.commit("base", map[string]string{"jest.config.js": fmt.Sprintf(cfg, "    'src/old\\\\.test\\\\.ts',\n")})

	r.git("checkout", "-q", "-b", "ignore")
	r.commit("ignore", map[string]string{"jest.config.js": fmt.Sprintf(cfg, "    'src/old\\\\.test\\\\.ts',\n    'src/checkout\\\\.test\\\\.ts',\n")})
	code, out, errOut := referee(t, nil, "audit", "--dir", r.dir, "--base", "main", "--json")
	require.Equal(t, 20, code, out+errOut)
	var rep auditReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Findings, 1, "%+v", rep.Findings)
	assert.Equal(t, finding{Rule: "test_path_ignored", Path: "jest.config.js", Line: 34, Message: rep.Findings[0].Message, Text: `    'src/checkout\\.test\\.ts',`}, rep.Findings[0])

	r.git("checkout", "-q", "-b", "unignore", "main")
	r.commit("unignore", map[string]string{"jest.config.js": fmt.Sprintf(cfg, "")})
	code, out, errOut = referee(t, nil, "audit", "--dir", r.dir, "--base", "main")
	assert.Equal(t, 0, code, out+errOut)
}
