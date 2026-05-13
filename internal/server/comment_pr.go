package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker"
)

// handleAgentCommentPR (gap D) is the structured-findings sibling of
// handleAgentComment. It accepts a CommentPRRequest, validates it, renders
// the findings into a deterministic Markdown body, and posts it via the
// existing CommentOnIssue path.
//
// v1 caveat (gap §6.4): the rendered comment lands on the tracker ISSUE,
// not on the GitHub Pull Request. For Linear-tracked work this is correct
// (the issue IS the workflow surface). For GitHub-tracked work where the
// issue and the PR are distinct objects, the structured findings appear
// on the issue, not the PR. True GitHub PR API integration is deferred to
// a future gap; the action name retains "_pr" because the *intent* is
// review feedback (vs. freeform comment).
//
// The action grant must include AgentActionCommentPR — which is a separate
// scope from AgentActionComment so operators can authorise reviewer
// profiles to post structured findings without granting freeform comment
// access (or vice-versa).
func (s *Server) handleAgentCommentPR(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.validateAgentActionRequest(w, r, config.AgentActionCommentPR); !ok {
		return
	}
	identifier := chi.URLParam(r, "identifier")
	var req CommentPRRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if err := ValidateCommentPRRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	body := tracker.MarkManagedComment(RenderCommentPRMarkdown(req))
	if err := s.client.CommentOnIssue(r.Context(), identifier, body); err != nil {
		writeError(w, http.StatusInternalServerError, "comment_failed", err.Error())
		return
	}
	s.client.BumpCommentCount(identifier)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "findings": len(req.Findings)})
}

// CommentPRFinding is one structured review finding submitted by an agent
// via the `comment_pr` action. Path + Line + Severity + Body are required.
// Severity is normalised to one of {info, warning, error}; anything else is
// rejected by ValidateCommentPRRequest.
type CommentPRFinding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
}

// CommentPRRequest is the JSON body accepted by POST /agent-actions/{identifier}/comment_pr.
// Either Summary or Findings (or both) must be non-empty — an entirely empty
// review comment is rejected.
type CommentPRRequest struct {
	Summary  string             `json:"summary"`
	Findings []CommentPRFinding `json:"findings"`
}

// ValidateCommentPRRequest enforces input invariants before we render Markdown
// or hit the tracker. Returns a human-readable reason on failure.
func ValidateCommentPRRequest(req CommentPRRequest) error {
	if strings.TrimSpace(req.Summary) == "" && len(req.Findings) == 0 {
		return fmt.Errorf("comment_pr requires summary or at least one finding")
	}
	for i, f := range req.Findings {
		if strings.TrimSpace(f.Path) == "" {
			return fmt.Errorf("finding %d: path is required", i)
		}
		if f.Line < 0 {
			return fmt.Errorf("finding %d: line must be >= 0 (use 0 for file-level)", i)
		}
		if strings.TrimSpace(f.Body) == "" {
			return fmt.Errorf("finding %d: body is required", i)
		}
		switch strings.ToLower(strings.TrimSpace(f.Severity)) {
		case "info", "warning", "error":
		case "":
			return fmt.Errorf("finding %d: severity is required (info|warning|error)", i)
		default:
			return fmt.Errorf("finding %d: severity must be info, warning, or error (got %q)", i, f.Severity)
		}
	}
	return nil
}

// RenderCommentPRMarkdown deterministically renders a CommentPRRequest into a
// review comment body. Findings are sorted by severity (error first), then
// path, then line so the output is stable across calls. The header makes it
// easy to grep "🤖 Itervox review" downstream.
//
// Output shape (Markdown):
//
//	## 🤖 Itervox review
//
//	<summary, if any>
//
//	### Findings (3)
//
//	- ❌ **error** · `path/to/file.go:42` — body
//	- ⚠️ **warning** · `path/to/other.go:7` — body
//	- ℹ️ **info** · `path/to/file.go` — body
//
// File-level findings (line=0) omit the line suffix.
func RenderCommentPRMarkdown(req CommentPRRequest) string {
	findings := append([]CommentPRFinding(nil), req.Findings...)
	severityRank := func(s string) int {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "error":
			return 0
		case "warning":
			return 1
		case "info":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if severityRank(findings[i].Severity) != severityRank(findings[j].Severity) {
			return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
		}
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Line < findings[j].Line
	})

	var b strings.Builder
	b.WriteString("## 🤖 Itervox review\n\n")
	if s := strings.TrimSpace(req.Summary); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	if len(findings) > 0 {
		fmt.Fprintf(&b, "### Findings (%d)\n\n", len(findings))
		for _, f := range findings {
			icon := severityIcon(f.Severity)
			loc := f.Path
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
			}
			fmt.Fprintf(&b, "- %s **%s** · `%s` — %s\n",
				icon,
				strings.ToLower(strings.TrimSpace(f.Severity)),
				loc,
				strings.TrimSpace(f.Body),
			)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// severityIcon maps a severity string to its rendered glyph. The default
// "•" branch is defense-in-depth — `ValidateCommentPRRequest` already
// rejects unknown severities, so a validated request can never reach this
// fallback. Kept for safety in case a caller renders an un-validated
// CommentPRRequest in the future.
func severityIcon(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "error":
		return "❌"
	case "warning":
		return "⚠️"
	case "info":
		return "ℹ️"
	default:
		return "•"
	}
}
