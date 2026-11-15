package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/prwatch"
)

// change_review_diff.go — #238: the PR source of GET .../diff. Once the
// issue has a GitHub pull request, the diff defaults to the PR's files as
// GitHub compares them (base...head), so the operator reviews what a merge
// would apply. The live worktree diff stays selectable while a run is live
// (it may be ahead of the pushed head).

// prFileDiffMaxBytes caps one PR file's patch, like the worktree's.
const prFileDiffMaxBytes = 256 * 1024

// diffSource is the resolved source of one diff request.
type diffSource struct {
	use      string
	explicit bool
	prURL    string
	ref      prwatch.PRRef
	sources  []string
	prError  string
}

// resolveDiffSource picks the default source for identifier: the PR when
// it has one GitHub can be asked about, else the worktree.
func (s *Server) resolveDiffSource(identifier string) diffSource {
	src := diffSource{use: DiffSourceWorktree}
	if u, ref, ok := s.issuePRRef(identifier); ok {
		src.use, src.prURL, src.ref = DiffSourcePR, u, ref
		src.sources = append(src.sources, DiffSourcePR)
	}
	if src.prURL == "" || s.issueRunID(identifier) != "" {
		src.sources = append(src.sources, DiffSourceWorktree)
	}
	return src
}

// decorate stamps the source fields on a diff list.
func (d diffSource) decorate(diff *DiffResponse) {
	diff.Source, diff.Sources, diff.PRURL, diff.PRError = d.use, d.sources, d.prURL, d.prError
}

// prStatus maps GitHub's file status to the worktree diff's vocabulary.
func prStatus(status string) string {
	switch status {
	case "removed":
		return "deleted"
	case "changed", "unchanged", "":
		return "modified"
	default: // added, modified, renamed, copied
		return status
	}
}

func prFileRow(f prwatch.PRFile) DiffFileRow {
	return DiffFileRow{
		Path: f.Filename, Status: prStatus(f.Status), Additions: f.Additions, Deletions: f.Deletions,
		// GitHub omits the patch of a binary file (and of a huge one,
		// which then still has line counts).
		Binary: f.Patch == "" && f.Additions == 0 && f.Deletions == 0 && f.Status != "removed" && f.Status != "renamed",
	}
}

// prFilePatch is f's hunks with git's file header, so the dashboard parses
// it like a worktree patch.
func prFilePatch(f prwatch.PRFile) string {
	if f.Patch == "" {
		return ""
	}
	old := f.Filename
	if f.PreviousFilename != "" {
		old = f.PreviousFilename
	}
	return "diff --git a/" + old + " b/" + f.Filename + "\n--- a/" + old + "\n+++ b/" + f.Filename + "\n" +
		strings.TrimRight(f.Patch, "\n") + "\n"
}

// servePRDiff answers a PR-source diff request. It returns false, having
// written nothing, when GitHub could not be read and the PR source was the
// default: the caller then serves the worktree, reporting prError.
func (s *Server) servePRDiff(w http.ResponseWriter, r *http.Request, id, file string, src diffSource) (handled bool, prError string) {
	pr, err := s.github.PR(r.Context(), src.ref)
	var files []prwatch.PRFile
	if err == nil {
		files, err = s.github.PRFiles(r.Context(), src.ref)
	}
	if err != nil {
		if src.explicit {
			writeGitHubError(w, err)
			return true, ""
		}
		if errors.Is(err, prwatch.ErrNotFound) {
			return false, "pr_not_found"
		}
		return false, "github_failed"
	}
	scope := callerScope(r)
	if file == "" {
		resp := DiffResponse{
			Identifier: id, Base: pr.Base.Ref, MergeBase: pr.Base.SHA, HeadSHA: pr.Head.SHA,
			Files: make([]DiffFileRow, 0, len(files)), Truncated: len(files) >= maxPRFiles,
			HunksAllowed: scope.Allows(apitokens.ScopeOperator),
		}
		for _, f := range files {
			resp.Files = append(resp.Files, prFileRow(f))
		}
		src.decorate(&resp)
		if s.changeReview != nil {
			resp.WorktreePath = s.changeReview.WorktreePath(id)
		}
		writeJSON(w, http.StatusOK, resp)
		return true, ""
	}
	for _, f := range files {
		if f.Filename != file {
			continue
		}
		patch := prFilePatch(f)
		fd := FileDiffResponse{Identifier: id, Base: pr.Base.Ref, File: prFileRow(f), Bytes: len(patch),
			MaxBytes: prFileDiffMaxBytes, Source: DiffSourcePR}
		if len(patch) > prFileDiffMaxBytes {
			patch, fd.Truncated = patch[:prFileDiffMaxBytes], true
		}
		fd.Patch = patch
		if scope != apitokens.ScopeAdmin {
			fd.Patch = logging.RedactString(fd.Patch)
		}
		writeJSON(w, http.StatusOK, fd)
		return true, ""
	}
	writeErrorWithField(w, http.StatusNotFound, "file_not_changed", "the file is not changed in the pull request", "file")
	return true, ""
}

// maxPRFiles mirrors GitHub's cap on a pull request's file listing.
const maxPRFiles = 3000
