package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/logging"
)

// attestations.go — signed checks (agent.profiles.<name>.verify).
//
//	GET /api/v1/issues/{identifier}/attestations   viewer   the issue's signed check records
//
// The records are read from the daemon's attestation store and verified on
// the HTTP goroutine (never the event loop): a record is `verified` when
// its ed25519 signature is valid for the daemon's key and its tree is the
// run's final tree (the tree the run's seal records). Commands and output
// tails were redacted when the record was made.

// AttestationRecordView is one signed record.
type AttestationRecordView struct {
	Kind         string    `json:"kind"`
	Name         string    `json:"name,omitempty"`
	Command      string    `json:"command,omitempty"`
	Pipefail     bool      `json:"pipefail,omitempty"`
	ExitCode     int       `json:"exitCode"`
	TimedOut     bool      `json:"timedOut,omitempty"`
	Error        string    `json:"error,omitempty"`
	StartedAt    time.Time `json:"startedAt"`
	DurationMs   int64     `json:"durationMs"`
	OutputSHA256 string    `json:"outputSha256,omitempty"`
	OutputTail   string    `json:"outputTail,omitempty"`
	TreeHash     string    `json:"treeHash,omitempty"`
	Head         string    `json:"head,omitempty"`
	KeyID        string    `json:"keyId,omitempty"`
	// Verified: signature valid and tree hash equal to the run's final
	// tree. Reason says why not.
	Verified bool   `json:"verified"`
	Reason   string `json:"reason,omitempty"`
}

// AttestationRunView is one run's records, oldest first, ending with the
// run's seal.
type AttestationRunView struct {
	RunID     string                  `json:"runId"`
	Profile   string                  `json:"profile,omitempty"`
	FinalTree string                  `json:"finalTree,omitempty"`
	Records   []AttestationRecordView `json:"records"`
}

// AttestationsResponse is GET /api/v1/issues/{identifier}/attestations.
type AttestationsResponse struct {
	Identifier string `json:"identifier"`
	// KeyID names the daemon's signing key.
	KeyID string               `json:"keyId,omitempty"`
	Runs  []AttestationRunView `json:"runs"`
}

// AttestationsClient reads and verifies an issue's signed check records.
type AttestationsClient interface {
	Attestations(identifier string) (AttestationsResponse, error)
}

func (s *Server) handleIssueAttestations(w http.ResponseWriter, r *http.Request) {
	if s.attestations == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "signed checks are not available")
		return
	}
	id := chi.URLParam(r, "identifier")
	resp, err := s.attestations.Attestations(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "attestations_unreadable", "the signed check records could not be read: "+logging.RedactString(err.Error()))
		return
	}
	resp.Identifier = id
	if resp.Runs == nil {
		resp.Runs = []AttestationRunView{}
	}
	writeJSON(w, http.StatusOK, resp)
}
