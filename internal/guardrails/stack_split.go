package guardrails

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Stack split (guardrails.lanes.split): the daemon's push of a patches-only
// run (push: daemon) whose changes touch both human-lane files and other
// files is offered as two stacked pull requests, the code and the pipeline
// change. The orchestrator builds and checks the two commits; this file
// only answers which files the run changed and which side each one is on,
// with the classifier the pre-push hook uses.

// ChangedSinceBase lists the files commit sha changes against its merge
// base with the first of baseRefs that has one, exactly as the pre-push
// hook lists a new branch's files (renames as a deletion plus an
// addition), and returns that merge base.
func ChangedSinceBase(dir, sha string, baseRefs []string) (base string, paths []string, err error) {
	repo := gitRepo{dir: dir}
	for _, ref := range baseRefs {
		if mb, ok := repo.MergeBase(ref, sha); ok {
			base = mb
			break
		}
	}
	if base == "" {
		return "", nil, fmt.Errorf("guardrails: no base branch to compare %s with (tried %s)", shortSHA(sha), strings.Join(baseRefs, ", "))
	}
	paths, err = repo.ChangedPaths(base, sha)
	return base, paths, err
}

// lockfileNames are the dependency lockfiles a stack split always keeps on
// the code side: they move with the manifests the code changes, whatever
// lane a pattern puts them in.
var lockfileNames = []string{
	"go.sum", "go.work.sum", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock",
	"bun.lock", "bun.lockb", "Cargo.lock", "Gemfile.lock", "poetry.lock", "Pipfile.lock", "uv.lock",
	"pdm.lock", "composer.lock", "mix.lock", "pubspec.lock", "Podfile.lock", "packages.lock.json",
	"gradle.lockfile", "flake.lock", "deno.lock",
}

// IsLockfile reports whether the repository-relative path p is a
// dependency lockfile (by its base name).
func IsLockfile(p string) bool {
	return slices.Contains(lockfileNames, path.Base(strings.ReplaceAll(p, `\`, "/")))
}

// SplitPaths divides a run's changed paths into the code side and the
// pipeline side of a stack split: the pipeline side is every human-lane
// path (Lanes.Classify) except lockfiles and the keep paths (Itervox's own
// bookkeeping), which stay with the code; everything else is code. A file
// is never divided. Both sides keep paths' order.
func (l Lanes) SplitPaths(paths []string, keep func(string) bool) (code, pipeline []string) {
	for _, p := range paths {
		if l.Lane(p) == LaneHuman && !IsLockfile(p) && (keep == nil || !keep(p)) {
			pipeline = append(pipeline, p)
			continue
		}
		code = append(code, p)
	}
	return code, pipeline
}
