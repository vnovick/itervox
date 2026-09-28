package server

import (
	"errors"
	"net/http"
)

// ErrSettingsReloading is returned (wrapped) by a settings save that belongs
// to a daemon generation which has already been superseded by a WORKFLOW.md
// reload (M1-close C2). The save was refused before it wrote anything: had
// it written, the new generation — which loaded the file before the write —
// would never see the value, and because the write is the daemon's own it
// would not trigger another reload either. Handlers answer 503 with
// Retry-After so the client re-sends the save to the new generation.
var ErrSettingsReloading = errors.New("server: WORKFLOW.md was reloaded while this save was in flight; nothing was written, retry the save")

// writeClientError maps an OrchestratorClient error from a settings or
// mutation handler: ErrSettingsReloading becomes 503 settings_reloading with
// Retry-After, anything else 500 with the handler's code.
func writeClientError(w http.ResponseWriter, code string, err error) {
	if errors.Is(err, ErrSettingsReloading) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "settings_reloading", err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, code, err.Error())
}
