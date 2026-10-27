package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/logging"
)

// #250 — workspace.copy_files: gitignored files the project needs but does
// not commit (.env, .env.local, a local config) copied from the main
// checkout into each worktree.
//
// Rules, each enforced here (the config load already refused entries that
// are absolute or contain ".."):
//   - Only files git ignores are copied, checked with `git check-ignore`
//     through gitexec in BOTH the main checkout and the worktree. A tracked
//     or unignored match is skipped with a reason, so a copied file can never
//     be committed from the worktree.
//   - Symlinks are never followed: a match that is a symlink, a match reached
//     through a symlinked directory, and a symlink inside a copied directory
//     are skipped.
//   - Directories are copied recursively; the bytes copied per pass are
//     capped (workspace.copy_files_max_bytes) and a cap hit is reported.
//   - File modes and modification times are kept. A file is (re)copied only
//     when the worktree has no copy or the main checkout's is newer, so an
//     edit made in the worktree survives later runs.
//   - The values of a copied dotenv file are registered with the log
//     redactor (logging.RegisterSecret).

// CopyFilesReport is what one copy pass did. Paths are relative to the
// repository root, with forward slashes.
type CopyFilesReport struct {
	// Copied were written to the worktree in this pass.
	Copied []string
	// Kept were already in the worktree and at least as new as the source.
	Kept []string
	// Skipped are "path: reason" for matches that were not copied.
	Skipped []string
	// Unmatched are patterns that matched nothing in the main checkout.
	Unmatched []string
	// Bytes is the size of the files copied in this pass.
	Bytes int64
	// CapHit is set when the size cap stopped the copy.
	CapHit bool
	// Secrets counts the dotenv values registered with the log redactor.
	Secrets int
}

// copyCandidate is one regular file a pattern matched.
type copyCandidate struct {
	rel  string // slash-separated, relative to the source root
	info fs.FileInfo
}

// CopyFiles copies the files patterns match under srcRoot into dstRoot (see
// the rules above). maxBytes <= 0 means no cap. It returns an error only
// when nothing can be checked (no source repository); per-file problems are
// reported in the CopyFilesReport.
func CopyFiles(ctx context.Context, srcRoot, dstRoot string, patterns []string, maxBytes int64) (CopyFilesReport, error) {
	var rep CopyFilesReport
	if len(patterns) == 0 {
		return rep, nil
	}
	if !filepath.IsAbs(srcRoot) || !filepath.IsAbs(dstRoot) {
		return rep, errors.New("workspace: copy_files needs absolute source and worktree paths")
	}
	cands := collectCopyCandidates(srcRoot, patterns, &rep)
	if len(cands) == 0 {
		return rep, nil
	}
	rels := make([]string, len(cands))
	for i, c := range cands {
		rels[i] = c.rel
	}
	srcIgnored, err := gitIgnored(ctx, srcRoot, rels)
	if err != nil {
		return rep, fmt.Errorf("workspace: copy_files: check the main checkout's gitignore: %w", err)
	}
	dstIgnored, err := gitIgnored(ctx, dstRoot, rels)
	if err != nil {
		return rep, fmt.Errorf("workspace: copy_files: check the worktree's gitignore: %w", err)
	}
	for _, c := range cands {
		switch {
		case !srcIgnored[c.rel]:
			rep.Skipped = append(rep.Skipped, c.rel+": not gitignored (tracked or unignored) in the main checkout")
			continue
		case !dstIgnored[c.rel]:
			rep.Skipped = append(rep.Skipped, c.rel+": not gitignored (tracked or unignored) in the worktree")
			continue
		case rep.CapHit:
			rep.Skipped = append(rep.Skipped, c.rel+": size cap reached")
			continue
		}
		copied, kept, err := copyOneFile(srcRoot, dstRoot, c, maxBytes, &rep)
		if err != nil {
			rep.Skipped = append(rep.Skipped, c.rel+": "+err.Error())
			continue
		}
		if copied {
			rep.Copied = append(rep.Copied, c.rel)
		} else if kept {
			rep.Kept = append(rep.Kept, c.rel)
		} else {
			continue // the cap stopped it; already reported
		}
		if isDotenvFile(c.rel) {
			rep.Secrets += registerDotenvSecrets(filepath.Join(dstRoot, filepath.FromSlash(c.rel)))
		}
	}
	return rep, nil
}

// collectCopyCandidates expands patterns into the regular files to consider,
// sorted and de-duplicated. Matches that cannot be copied are reported in
// rep.Skipped.
func collectCopyCandidates(srcRoot string, patterns []string, rep *CopyFilesReport) []copyCandidate {
	seen := map[string]bool{}
	var out []copyCandidate
	add := func(rel string, info fs.FileInfo) {
		if !plainName(rel) {
			rep.Skipped = append(rep.Skipped, strconv.Quote(rel)+": unusual file name (control character, quote or backslash)")
			return
		}
		if !seen[rel] {
			seen[rel] = true
			out = append(out, copyCandidate{rel: rel, info: info})
		}
	}
	for _, pat := range patterns {
		if !safeRelPattern(pat) {
			rep.Skipped = append(rep.Skipped, pat+": leaves the repository root")
			continue
		}
		matches, err := filepath.Glob(filepath.Join(srcRoot, filepath.FromSlash(pat)))
		if err != nil || len(matches) == 0 {
			rep.Unmatched = append(rep.Unmatched, pat)
			continue
		}
		slices.Sort(matches)
		for _, m := range matches {
			rel, ok := relInside(srcRoot, m)
			if !ok {
				rep.Skipped = append(rep.Skipped, pat+": leaves the repository root")
				continue
			}
			if reason := unsafeSourcePath(srcRoot, rel); reason != "" {
				rep.Skipped = append(rep.Skipped, rel+": "+reason)
				continue
			}
			info, err := os.Lstat(m)
			if err != nil {
				rep.Skipped = append(rep.Skipped, rel+": "+err.Error())
				continue
			}
			switch {
			case info.Mode().IsRegular():
				add(rel, info)
			case info.IsDir():
				walkCopyDir(srcRoot, m, rep, add)
			default:
				rep.Skipped = append(rep.Skipped, rel+": not a regular file")
			}
		}
	}
	slices.SortFunc(out, func(a, b copyCandidate) int { return strings.Compare(a.rel, b.rel) })
	return out
}

// walkCopyDir adds the regular files under dir. WalkDir does not follow
// symlinks; a symlink (or any other non-regular entry) is skipped and
// reported, and a .git entry is never descended into.
func walkCopyDir(srcRoot, dir string, rep *CopyFilesReport, add func(string, fs.FileInfo)) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		rel, ok := relInside(srcRoot, p)
		if !ok {
			return filepath.SkipDir
		}
		if err != nil {
			rep.Skipped = append(rep.Skipped, rel+": "+err.Error())
			return nil
		}
		if d.Name() == ".git" {
			rep.Skipped = append(rep.Skipped, rel+": git metadata is never copied")
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			rep.Skipped = append(rep.Skipped, rel+": symlink (not followed)")
		case d.Type().IsRegular():
			if info, ierr := d.Info(); ierr == nil {
				add(rel, info)
			}
		default:
			rep.Skipped = append(rep.Skipped, rel+": not a regular file")
		}
		return nil
	})
}

// copyOneFile copies c into dstRoot unless the worktree's copy is at least
// as new. It returns copied=true when it wrote the file, kept=true when the
// worktree copy was left as it is, and neither when the size cap stopped it
// (rep.CapHit is then set).
func copyOneFile(srcRoot, dstRoot string, c copyCandidate, maxBytes int64, rep *CopyFilesReport) (copied, kept bool, err error) {
	dst := filepath.Join(dstRoot, filepath.FromSlash(c.rel))
	if reason := unsafeDestPath(dstRoot, c.rel); reason != "" {
		return false, false, errors.New(reason)
	}
	if di, err := os.Lstat(dst); err == nil {
		if !di.Mode().IsRegular() {
			return false, false, errors.New("the worktree has something other than a file there")
		}
		if !c.info.ModTime().After(di.ModTime()) {
			return false, true, nil
		}
	}
	size := c.info.Size()
	if maxBytes > 0 && rep.Bytes+size > maxBytes {
		rep.CapHit = true
		rep.Skipped = append(rep.Skipped, c.rel+": size cap reached")
		return false, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, false, err
	}
	if err := copyFileContents(filepath.Join(srcRoot, filepath.FromSlash(c.rel)), dst, c.info); err != nil {
		return false, false, err
	}
	rep.Bytes += size
	return true, false, nil
}

// copyFileContents writes src to dst through a temporary file in dst's
// directory, with src's permission bits and modification time.
func copyFileContents(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".itervox-copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(tmpName, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// checkIgnoreBatch bounds how many paths one `git check-ignore` call is
// given as arguments (the paths go on the command line rather than stdin:
// itervox writes stdin only to the agent processes it starts, #178).
const checkIgnoreBatch = 200

// gitIgnored returns which of rels git ignores in the repository at dir
// (`git check-ignore -- <paths>`, in batches, one path per output line with
// core.quotePath off; plainName keeps out the names git would still quote).
// A tracked file is not ignored. Exit status 1 means none of a batch is
// ignored; anything else non-zero is an error.
func gitIgnored(ctx context.Context, dir string, rels []string) (map[string]bool, error) {
	ignored := map[string]bool{}
	for batch := range slices.Chunk(rels, checkIgnoreBatch) {
		args := append([]string{"-c", "core.quotePath=false", "-C", dir, "check-ignore", "--"}, batch...)
		cmd := gitexec.Command(ctx, dir, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == 1 {
				continue
			}
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("%w: %s", err, msg)
			}
			return nil, err
		}
		for _, p := range strings.Split(string(out), "\n") {
			if p != "" {
				ignored[p] = true
			}
		}
	}
	return ignored, nil
}

// plainName reports whether rel has no character git quotes in a path even
// with core.quotePath off (control characters, '"' and '\\'), so its
// check-ignore output line is the path itself.
func plainName(rel string) bool {
	return !strings.ContainsFunc(rel, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '"' || r == '\\' })
}

// safeRelPattern re-checks what the config load enforces: relative, and no
// ".." element.
func safeRelPattern(pat string) bool {
	if pat == "" || filepath.IsAbs(pat) || strings.HasPrefix(pat, "/") || strings.HasPrefix(pat, `\`) {
		return false
	}
	for _, part := range strings.FieldsFunc(pat, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return false
		}
	}
	return true
}

// relInside returns p relative to root (slash-separated) when p is strictly
// inside root.
func relInside(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// unsafeSourcePath reports why rel (under srcRoot) must not be read: one of
// its parent directories is a symlink (the match would be read through it),
// or it is inside .git.
func unsafeSourcePath(srcRoot, rel string) string {
	parts := strings.Split(rel, "/")
	cur := srcRoot
	for i, part := range parts {
		if part == ".git" {
			return "git metadata is never copied"
		}
		cur = filepath.Join(cur, part)
		if i == len(parts)-1 {
			break
		}
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return "reached through a symlink (not followed)"
		}
	}
	if fi, err := os.Lstat(filepath.Join(srcRoot, filepath.FromSlash(rel))); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return "symlink (not followed)"
	}
	return ""
}

// unsafeDestPath reports why rel must not be written under dstRoot: one of
// its parent directories in the worktree is a symlink (the write could land
// outside the worktree).
func unsafeDestPath(dstRoot, rel string) string {
	parts := strings.Split(rel, "/")
	cur := dstRoot
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return "" // missing from here down: MkdirAll creates real directories
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return "the worktree path goes through a symlink or a file"
		}
	}
	return ""
}

// isDotenvFile reports whether rel names a dotenv file: .env, .env.<name>,
// or <name>.env.
func isDotenvFile(rel string) bool {
	base := filepath.Base(filepath.FromSlash(rel))
	return base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env")
}

// registerDotenvSecrets registers every value in the dotenv file at path
// with the log redactor and returns how many it handed over (values shorter
// than the redactor's minimum are ignored by it). A file that does not
// parse registers nothing.
func registerDotenvSecrets(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	vals, err := godotenv.Parse(f)
	if err != nil {
		return 0
	}
	n := 0
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			logging.RegisterSecret(v)
			n++
		}
	}
	return n
}
