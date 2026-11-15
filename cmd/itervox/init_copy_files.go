package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/gitexec"
)

// initCopyFilesCandidates are the files `itervox init` suggests for
// workspace.copy_files (#250) when they exist and git ignores them.
var initCopyFilesCandidates = []string{".env", ".env.local"}

// detectCopyFiles returns the initCopyFilesCandidates that are regular files
// (not symlinks) in dir and that git ignores there (`git check-ignore`,
// which also reports a tracked file as not ignored). Outside a git checkout
// it returns nothing: copy_files only ever copies ignored files.
func detectCopyFiles(dir string) []string {
	var out []string
	for _, name := range initCopyFilesCandidates {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if gitexec.Command(context.Background(), dir, "-C", dir, "check-ignore", "-q", "--", name).Run() == nil {
			out = append(out, name)
		}
	}
	return out
}

// writeCopyFilesLine writes workspace.copy_files for the detected files,
// with the warning that the agent can read them. Nothing when none.
func writeCopyFilesLine(b *strings.Builder, files []string) {
	if len(files) == 0 {
		return
	}
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = strconv.Quote(f)
	}
	b.WriteString("  # Gitignored files copied into each new worktree (the agent can read them).\n")
	b.WriteString("  copy_files: [" + strings.Join(quoted, ", ") + "]\n")
}
