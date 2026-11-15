package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// TestReadinessFlagsMissingVerifySkill (#218): Repo Analysis readiness and
// `itervox doctor` warn "no project verification skill or script" until the
// repository has one of: a .claude/skills/<…verif…>/SKILL.md, a
// scripts/verify* file, a `verify` Makefile target, or a package.json
// `verify` script — each of which alone makes the check pass. Look-alikes
// (a verify variable in the Makefile, a skill dir without SKILL.md, an
// empty package.json script) do not count.
func TestReadinessFlagsMissingVerifySkill(t *testing.T) {
	check := func(dir string) server.RepoReadinessCheck {
		t.Helper()
		return verifySkillReadiness(filepath.Join(dir, "WORKFLOW.md"))
	}
	write := func(dir, rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	empty := t.TempDir()
	write(empty, "Makefile", "VERIFY := yes\nverify-docs:\n\techo\nbuild: verify.o\n")
	write(empty, ".claude/skills/verify/README.md", "no SKILL.md here\n")
	write(empty, "package.json", `{"scripts": {"verify": "  ", "test": "vitest"}}`)
	c := check(empty)
	assert.Equal(t, readinessVerifySkill, c.ID)
	assert.Equal(t, server.RepoReadinessWarn, c.Status)
	assert.Contains(t, c.Detail, "no project verification skill or script")
	assert.Contains(t, c.Fix, "Generate verify skill")

	for name, setup := range map[string]func(dir string){
		"skill":        func(d string) { write(d, ".claude/skills/Verify-Before-Done/SKILL.md", "---\nname: v\n---\n") },
		"script":       func(d string) { write(d, "scripts/verify.sh", "#!/bin/sh\n") },
		"make target":  func(d string) { write(d, "Makefile", "all: build\n\nlint verify: deps\n\tgo test ./...\n") },
		"package.json": func(d string) { write(d, "package.json", `{"scripts": {"verify": "pnpm lint && pnpm test"}}`) },
	} {
		dir := t.TempDir()
		setup(dir)
		c := check(dir)
		assert.Equal(t, server.RepoReadinessPass, c.Status, name)
		assert.Contains(t, c.Detail, "found ", name)
	}

	// The row is part of the readiness response doctor and the endpoint share.
	wf := readinessFixture(t)
	resp := collectRepoReadiness(context.Background(), wf, stubTrackerProbe(deploySkipped, "skipped"))
	var found bool
	for _, row := range resp.Checks {
		if row.ID == readinessVerifySkill {
			found = true
			assert.Equal(t, server.RepoReadinessWarn, row.Status)
		}
	}
	assert.True(t, found, "readiness lists the verify_skill check")
}
