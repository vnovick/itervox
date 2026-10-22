package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/gitexec"
)

// diff.go — #215: a worktree's changes against the merge base with its
// base branch, for GET /api/v1/issues/{identifier}/diff and the run
// context's changed files. Every git call goes through gitexec; callers
// run these off the event loop under a timeout.

// Diff errors.
var (
	// ErrNotWorkTree: the path is not a git work tree.
	ErrNotWorkTree = errors.New("workspace: not a git work tree")
	// ErrDiffBaseNotFound: no candidate base ref has a merge base with HEAD.
	ErrDiffBaseNotFound = errors.New("workspace: no base branch to diff against")
	// ErrDiffFileNotChanged: the requested file is not in the diff.
	ErrDiffFileNotChanged = errors.New("workspace: file is not changed against the base")
)

// DiffMaxFiles caps the file list; DiffSummary.Truncated reports a cut.
const DiffMaxFiles = 2000

// untrackedSniffBytes is how much of an untracked file is read to tell
// text from binary (git's own heuristic: a NUL in the first 8000 bytes).
const untrackedSniffBytes = 8000

// untrackedCountMaxBytes bounds the untracked files whose lines are counted.
const untrackedCountMaxBytes = 1 << 20

// File statuses.
const (
	DiffAdded      = "added"
	DiffModified   = "modified"
	DiffDeleted    = "deleted"
	DiffTypeChange = "typechange"
	DiffUnmerged   = "unmerged"
	DiffUntracked  = "untracked"
)

// DiffFile is one changed path. Additions/Deletions are line counts (0 for
// a binary file).
type DiffFile struct {
	Path      string
	Status    string
	Additions int
	Deletions int
	Binary    bool
}

// DiffSummary is the worktree (committed and uncommitted changes, plus
// untracked files) against the merge base of HEAD with Base.
type DiffSummary struct {
	Base      string
	MergeBase string
	Files     []DiffFile
	Truncated bool
}

// FileDiff is one file's unified diff, cut at the caller's byte cap.
type FileDiff struct {
	DiffFile
	Patch     string
	Truncated bool
	// Bytes is the size of the whole diff before the cap.
	Bytes int
}

// DiffAgainstBase lists the worktree's changes against the merge base of
// HEAD with the first of bases that has one.
func DiffAgainstBase(ctx context.Context, wsPath string, bases []string) (DiffSummary, error) {
	if out, err := gitexec.Command(ctx, wsPath, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DiffSummary{}, ctxErr
		}
		return DiffSummary{}, ErrNotWorkTree
	}
	var sum DiffSummary
	for _, b := range bases {
		if b == "" || strings.HasPrefix(b, "-") {
			continue
		}
		out, err := gitexec.Command(ctx, wsPath, "merge-base", b, "HEAD").Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			sum.Base, sum.MergeBase = b, strings.TrimSpace(string(out))
			break
		}
		if ctx.Err() != nil {
			return DiffSummary{}, ctx.Err()
		}
	}
	if sum.MergeBase == "" {
		return DiffSummary{}, ErrDiffBaseNotFound
	}
	numstat, err := gitexec.Command(ctx, wsPath, "diff", "--no-renames", "--no-ext-diff", "--numstat", "-z", sum.MergeBase, "--").Output()
	if err != nil {
		return DiffSummary{}, fmt.Errorf("workspace: diff --numstat: %w", err)
	}
	nameStatus, err := gitexec.Command(ctx, wsPath, "diff", "--no-renames", "--no-ext-diff", "--name-status", "-z", sum.MergeBase, "--").Output()
	if err != nil {
		return DiffSummary{}, fmt.Errorf("workspace: diff --name-status: %w", err)
	}
	statuses := parseNameStatusZ(nameStatus)
	for _, f := range parseNumstatZ(numstat) {
		f.Status = statuses[f.Path]
		if f.Status == "" {
			f.Status = DiffModified
		}
		sum.Files = append(sum.Files, f)
	}
	untracked, err := gitexec.Command(ctx, wsPath, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return DiffSummary{}, fmt.Errorf("workspace: ls-files --others: %w", err)
	}
	for p := range strings.SplitSeq(string(untracked), "\x00") {
		if p != "" {
			sum.Files = append(sum.Files, untrackedFile(wsPath, p))
		}
	}
	if len(sum.Files) > DiffMaxFiles {
		sum.Files, sum.Truncated = sum.Files[:DiffMaxFiles], true
	}
	return sum, nil
}

// parseNumstatZ parses `git diff --numstat -z` without renames: records of
// "added\tdeleted\tpath" separated by NUL, "-\t-" for a binary file.
func parseNumstatZ(out []byte) []DiffFile {
	var files []DiffFile
	for rec := range strings.SplitSeq(string(out), "\x00") {
		add, rest, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		del, path, ok := strings.Cut(rest, "\t")
		if !ok || path == "" {
			continue
		}
		f := DiffFile{Path: path}
		if add == "-" && del == "-" {
			f.Binary = true
		} else {
			f.Additions, _ = strconv.Atoi(add)
			f.Deletions, _ = strconv.Atoi(del)
		}
		files = append(files, f)
	}
	return files
}

// parseNameStatusZ parses `git diff --name-status -z` without renames:
// alternating status letter and path, NUL separated.
func parseNameStatusZ(out []byte) map[string]string {
	fields := strings.Split(string(out), "\x00")
	m := make(map[string]string, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "" {
			continue
		}
		var st string
		switch fields[i][0] {
		case 'A':
			st = DiffAdded
		case 'D':
			st = DiffDeleted
		case 'T':
			st = DiffTypeChange
		case 'U':
			st = DiffUnmerged
		default:
			st = DiffModified
		}
		m[fields[i+1]] = st
	}
	return m
}

// untrackedFile describes an untracked path: binary when its head holds a
// NUL, else its line count (left 0 above untrackedCountMaxBytes).
func untrackedFile(wsPath, rel string) DiffFile {
	f := DiffFile{Path: rel, Status: DiffUntracked}
	full := filepath.Join(wsPath, rel)
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return f
	}
	if info.Size() > untrackedCountMaxBytes {
		f.Binary = sniffBinary(full)
		return f
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return f
	}
	if bytes.IndexByte(data[:min(len(data), untrackedSniffBytes)], 0) >= 0 {
		f.Binary = true
		return f
	}
	f.Additions = bytes.Count(data, []byte("\n"))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		f.Additions++
	}
	return f
}

func sniffBinary(path string) bool {
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = fh.Close() }()
	buf := make([]byte, untrackedSniffBytes)
	n, _ := fh.Read(buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// cappedBuffer keeps the first limit bytes written and counts the rest.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	total int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.total += len(p)
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// FileDiffAgainst returns file's unified diff against sum's merge base, cut
// at maxBytes (on a line boundary). file must be one of sum.Files: no other
// path reaches git. A binary file has no patch.
func FileDiffAgainst(ctx context.Context, wsPath string, sum DiffSummary, file string, maxBytes int) (FileDiff, error) {
	var f *DiffFile
	for i := range sum.Files {
		if sum.Files[i].Path == file {
			f = &sum.Files[i]
			break
		}
	}
	if f == nil {
		return FileDiff{}, ErrDiffFileNotChanged
	}
	out := FileDiff{DiffFile: *f}
	if f.Binary {
		return out, nil
	}
	var cmdArgs []string
	if f.Status == DiffUntracked {
		cmdArgs = []string{"diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", "--", os.DevNull, f.Path}
	} else {
		cmdArgs = []string{"diff", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv", sum.MergeBase, "--", f.Path}
	}
	capped := &cappedBuffer{limit: maxBytes}
	cmd := gitexec.Command(ctx, wsPath, cmdArgs...)
	cmd.Stdout = capped
	if err := cmd.Run(); err != nil {
		// --no-index exits 1 when the files differ, which they always do.
		var exitErr interface{ ExitCode() int }
		differs := f.Status == DiffUntracked && errors.As(err, &exitErr) && exitErr.ExitCode() == 1
		if !differs {
			if ctx.Err() != nil {
				return FileDiff{}, ctx.Err()
			}
			return FileDiff{}, fmt.Errorf("workspace: diff %s: %w", f.Path, err)
		}
	}
	patch := capped.buf.String()
	out.Bytes = capped.total
	if isBinaryPatch(patch) {
		out.Binary = true
		return out, nil
	}
	if capped.total > maxBytes {
		out.Truncated = true
		if i := strings.LastIndexByte(patch, '\n'); i >= 0 {
			patch = patch[:i+1]
		}
	}
	out.Patch = patch
	return out, nil
}

// isBinaryPatch reports git's "Binary files … differ" patch.
func isBinaryPatch(patch string) bool {
	for line := range strings.SplitSeq(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			return false
		}
		if strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch" {
			return true
		}
	}
	return false
}
