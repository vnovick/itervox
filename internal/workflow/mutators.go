package workflow

import (
	"fmt"
	"strconv"
	"strings"
)

// This file holds in-memory front-matter manipulation as composable Mutator
// values. Every writer goes through ApplyAndWriteFrontMatter (CORE-006), so
// it is serialized by editMu. The agent:/workspace: block Mutate* helpers and
// the Doc builder moved to patchers_testonly_test.go and
// doc_testonly_test.go (#129): layered settings are saved to
// .itervox/settings.jsonc, and production only removes them from WORKFLOW.md
// (RemoveFrontMatterKeys).

// MutateIntField rewrites the first occurrence of `key: <int>` inside the
// front matter, preserving any inline comment. Errors when the key is
// missing — this matches PatchIntField's strict-key semantics.
func MutateIntField(key string, n int) Mutator {
	return func(frontLines []string) ([]string, error) {
		for i, line := range frontLines {
			m := keyLineRE.FindStringSubmatch(line)
			if m == nil || m[2] != key {
				continue
			}
			oldVal := m[4]
			comment := ""
			if ci := strings.Index(oldVal, " #"); ci >= 0 {
				comment = " " + strings.TrimSpace(oldVal[ci+1:])
				comment = " #" + comment[2:]
			}
			frontLines[i] = m[1] + m[2] + m[3] + strconv.Itoa(n) + comment
			return frontLines, nil
		}
		return nil, fmt.Errorf("workflow mutate: key %q not found in front matter", key)
	}
}
