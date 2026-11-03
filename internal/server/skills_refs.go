package server

import (
	"net/http"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/skills"
)

// Backend-aware skills in the dashboard (#123): the prompt editor's
// reference check and the per-profile backend map the pickers use.

// maxPromptRefCheckBytes caps the prompt a single check accepts.
const maxPromptRefCheckBytes = 256 << 10

// PromptRefCheckRequest is the body of POST /api/v1/skills/check-refs: one
// draft prompt (SOUL.md or INSTRUCTIONS.md) and the draft profile's runner
// settings. Profile is used only in messages.
type PromptRefCheckRequest struct {
	Profile string `json:"profile"`
	Command string `json:"command"`
	Backend string `json:"backend"`
	Text    string `json:"text"`
}

// PromptRefCheck is what the validator reports for one prompt. The
// adapter fills Findings with byte offsets; the handler converts them to
// UTF-16 code-unit offsets (what the browser editor counts) before writing.
type PromptRefCheck struct {
	Backend  string              `json:"backend"`
	SSHHosts []string            `json:"sshHosts"`
	Findings []skills.RefFinding `json:"-"`
}

// promptRefRow is one reference occurrence on the wire.
type promptRefRow struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	From     int    `json:"from"`
	To       int    `json:"to"`
	Status   string `json:"status"`
	Severity string `json:"severity,omitempty"`
	IssueID  string `json:"issueId,omitempty"`
	Message  string `json:"message,omitempty"`
}

type promptRefCheckResponse struct {
	Backend  string         `json:"backend"`
	SSHHosts []string       `json:"sshHosts"`
	Refs     []promptRefRow `json:"refs"`
}

// handleSkillsCheckRefs validates one draft prompt against the inventory
// with the same code doctor runs, so the editor underlines exactly what
// doctor reports. 503 before the first scan.
func (s *Server) handleSkillsCheckRefs(w http.ResponseWriter, r *http.Request) {
	var body PromptRefCheckRequest
	if err := decodeJSONBody(w, r, &body); err != nil {
		return
	}
	if len(body.Text) > maxPromptRefCheckBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "prompt_too_large",
			"prompt is too large to check")
		return
	}
	result, ok := s.skills.CheckPromptRefs(body)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "inventory_unavailable",
			"skills inventory has not been scanned yet")
		return
	}
	resp := promptRefCheckResponse{
		Backend:  result.Backend,
		SSHHosts: result.SSHHosts,
		Refs:     make([]promptRefRow, 0, len(result.Findings)),
	}
	if resp.SSHHosts == nil {
		resp.SSHHosts = []string{}
	}
	offsets := newUTF16Offsets(body.Text)
	for _, f := range result.Findings {
		row := promptRefRow{
			Kind:   f.Kind,
			Name:   f.Name,
			From:   offsets.at(f.Start),
			To:     offsets.at(f.End),
			Status: f.Status,
		}
		if f.Issue != nil {
			row.Severity = f.Issue.Severity
			row.IssueID = f.Issue.ID
			row.Message = f.Issue.Description
		}
		resp.Refs = append(resp.Refs, row)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSkillsProfileBackends returns {"default": ..., "profiles": {...}}.
func (s *Server) handleSkillsProfileBackends(w http.ResponseWriter, _ *http.Request) {
	m := s.skills.ProfileBackends()
	profiles := make(map[string]string, len(m))
	def := "claude"
	for name, backend := range m {
		if name == "" {
			def = backend
			continue
		}
		profiles[name] = backend
	}
	writeJSON(w, http.StatusOK, map[string]any{"default": def, "profiles": profiles})
}

// utf16Offsets converts increasing byte offsets of a string into UTF-16
// code-unit offsets in one forward pass.
type utf16Offsets struct {
	text     string
	byteOff  int
	utf16Off int
}

func newUTF16Offsets(text string) *utf16Offsets { return &utf16Offsets{text: text} }

// at returns the UTF-16 offset of byte offset b. Offsets may go backwards
// (the pass restarts) but are normally increasing.
func (u *utf16Offsets) at(b int) int {
	if b < u.byteOff {
		u.byteOff, u.utf16Off = 0, 0
	}
	b = min(b, len(u.text))
	for u.byteOff < b {
		r, size := utf8.DecodeRuneInString(u.text[u.byteOff:])
		u.byteOff += size
		if r >= 0x10000 {
			u.utf16Off += 2
		} else {
			u.utf16Off++
		}
	}
	return u.utf16Off
}
