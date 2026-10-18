package server

import (
	"cmp"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Run history by prompt version (#186). History rows come from the same
// snapshot as GET /api/v1/state; these endpoints only filter and group them.

// promptDigestPrefix is a valid ?prompt= value: 1–64 hex characters.
var promptDigestPrefix = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// HistoryResponse is GET /api/v1/history.
type HistoryResponse struct {
	History []HistoryRow `json:"history"`
}

// PromptVersionRow is one prompt version in GET /api/v1/history/prompt-versions.
type PromptVersionRow struct {
	PromptDigest string `json:"promptDigest"`
	Runs         int    `json:"runs"`
	// Statuses counts the runs per status ("succeeded", "failed", …).
	Statuses map[string]int `json:"statuses"`
	// SuccessRate is the share of the runs that succeeded, 0–1.
	SuccessRate float64   `json:"successRate"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	// Profiles are the profiles that ran this version, sorted.
	Profiles []string `json:"profiles"`
}

// PromptVersionsResponse is GET /api/v1/history/prompt-versions.
type PromptVersionsResponse struct {
	Versions []PromptVersionRow `json:"versions"`
	// UnversionedRuns counts history rows recorded without a digest
	// (written before #186).
	UnversionedRuns int `json:"unversionedRuns"`
}

// promptFilter reads ?prompt=; ok is false (and a 400 written) when it is
// not a hex prefix.
func promptFilter(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("prompt")))
	if p != "" && !promptDigestPrefix.MatchString(p) {
		writeErrorWithField(w, http.StatusBadRequest, "invalid_prompt_digest",
			"prompt must be 1-64 hex characters of a prompt digest", "prompt")
		return "", false
	}
	return p, true
}

// FilterHistoryByPrompt returns the rows whose prompt digest starts with
// prefix; every row for an empty prefix.
func FilterHistoryByPrompt(rows []HistoryRow, prefix string) []HistoryRow {
	out := make([]HistoryRow, 0, len(rows))
	for _, row := range rows {
		if prefix == "" || (row.PromptDigest != "" && strings.HasPrefix(row.PromptDigest, prefix)) {
			out = append(out, row)
		}
	}
	return out
}

// GroupHistoryByPrompt groups rows by prompt digest, newest version first.
func GroupHistoryByPrompt(rows []HistoryRow) PromptVersionsResponse {
	byDigest := map[string]*PromptVersionRow{}
	profiles := map[string]map[string]bool{}
	resp := PromptVersionsResponse{Versions: []PromptVersionRow{}}
	for _, row := range rows {
		if row.PromptDigest == "" {
			resp.UnversionedRuns++
			continue
		}
		v := byDigest[row.PromptDigest]
		if v == nil {
			v = &PromptVersionRow{PromptDigest: row.PromptDigest, Statuses: map[string]int{},
				FirstSeenAt: row.StartedAt, LastSeenAt: row.StartedAt}
			byDigest[row.PromptDigest] = v
			profiles[row.PromptDigest] = map[string]bool{}
		}
		v.Runs++
		v.Statuses[row.Status]++
		if row.StartedAt.Before(v.FirstSeenAt) {
			v.FirstSeenAt = row.StartedAt
		}
		if row.StartedAt.After(v.LastSeenAt) {
			v.LastSeenAt = row.StartedAt
		}
		if row.ProfileName != "" {
			profiles[row.PromptDigest][row.ProfileName] = true
		}
	}
	for d, v := range byDigest {
		v.SuccessRate = float64(v.Statuses["succeeded"]) / float64(v.Runs)
		v.Profiles = make([]string, 0, len(profiles[d]))
		for p := range profiles[d] {
			v.Profiles = append(v.Profiles, p)
		}
		slices.Sort(v.Profiles)
		resp.Versions = append(resp.Versions, *v)
	}
	slices.SortFunc(resp.Versions, func(a, b PromptVersionRow) int {
		return cmp.Or(b.LastSeenAt.Compare(a.LastSeenAt), cmp.Compare(a.PromptDigest, b.PromptDigest))
	})
	return resp
}

// handleHistory is GET /api/v1/history[?prompt=<digest prefix>]: the run
// history, oldest first, optionally only the runs of one prompt version.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	prefix, ok := promptFilter(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, HistoryResponse{History: FilterHistoryByPrompt(s.snapshot().History, prefix)})
}

// handleHistoryPromptVersions is GET /api/v1/history/prompt-versions: run
// counts and outcome rates per prompt version.
func (s *Server) handleHistoryPromptVersions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, GroupHistoryByPrompt(s.snapshot().History))
}
