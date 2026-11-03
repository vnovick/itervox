package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/server"
)

// The verification readiness check (#218, pstack's
// /create-verification-skill): does the repository tell an agent how to
// prove a change works? It passes when any of these exists at the
// repository root (the WORKFLOW.md directory):
//
//   - a project skill whose directory name contains "verif"
//     (.claude/skills/<…verif…>/SKILL.md, case-insensitive);
//   - a scripts/verify* file;
//   - a `verify` target in Makefile, makefile or GNUmakefile;
//   - a `verify` script in package.json.
//
// Otherwise it warns, and the Repo Analysis page offers "Generate verify
// skill", which creates an issue asking an agent to write one.

// readinessVerifySkill is the check's stable ID.
const readinessVerifySkill = "verify_skill"

// verifyScanBytes bounds how much of a Makefile or package.json is read.
const verifyScanBytes = 256 * 1024

// makeVerifyTarget matches a rule line that defines a `verify` target
// (alone or among several targets), not a variable assignment.
var makeVerifyTarget = regexp.MustCompile(`^(?:[A-Za-z0-9_./-]+\s+)*verify(?:\s+[A-Za-z0-9_./-]+)*\s*::?(?:[^=]|$)`)

// verifySources lists what the repository at dir has that proves a change.
func verifySources(dir string) []string {
	var found []string
	if entries, err := os.ReadDir(filepath.Join(dir, ".claude", "skills")); err == nil {
		for _, e := range entries {
			if !e.IsDir() || !strings.Contains(strings.ToLower(e.Name()), "verif") {
				continue
			}
			if st, err := os.Stat(filepath.Join(dir, ".claude", "skills", e.Name(), "SKILL.md")); err == nil && st.Mode().IsRegular() {
				found = append(found, ".claude/skills/"+e.Name()+"/SKILL.md")
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "scripts")); err == nil {
		for _, e := range entries {
			if e.Type().IsRegular() && strings.HasPrefix(strings.ToLower(e.Name()), "verify") {
				found = append(found, "scripts/"+e.Name())
			}
		}
	}
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		if makefileHasVerify(filepath.Join(dir, name)) {
			found = append(found, "`make verify` ("+name+")")
			break
		}
	}
	if packageJSONHasVerify(filepath.Join(dir, "package.json")) {
		found = append(found, "package.json `verify` script")
	}
	slices.Sort(found)
	return slices.Compact(found)
}

func makefileHasVerify(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, verifyScanBytes))
	sc.Buffer(make([]byte, 0, 4096), verifyScanBytes)
	for sc.Scan() {
		if makeVerifyTarget.MatchString(sc.Text()) {
			return true
		}
	}
	return false
}

func packageJSONHasVerify(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var pkg struct {
		Scripts map[string]any `json:"scripts"`
	}
	if err := json.NewDecoder(io.LimitReader(f, verifyScanBytes)).Decode(&pkg); err != nil {
		return false
	}
	s, ok := pkg.Scripts["verify"].(string)
	return ok && strings.TrimSpace(s) != ""
}

func verifySkillReadiness(workflowPath string) server.RepoReadinessCheck {
	const title = "Project verification skill or script"
	found := verifySources(filepath.Dir(workflowPath))
	if len(found) > 0 {
		return readinessRow(readinessVerifySkill, title, server.RepoReadinessPass, "found "+strings.Join(found, ", "), "")
	}
	return readinessRow(readinessVerifySkill, title, server.RepoReadinessWarn,
		"no project verification skill or script — agents guess how to prove a change works",
		"add .claude/skills/verify/SKILL.md (or scripts/verify.sh, a `make verify` target, or a package.json `verify` script) listing the commands that prove a change; Repo Analysis → Generate verify skill creates an issue for an agent to write it")
}
