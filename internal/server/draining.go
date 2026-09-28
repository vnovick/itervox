package server

import (
	"errors"
	"net/http"
)

// writeDrainingConflict writes 409 {"code":"draining"} when err is
// ErrDraining (CORE-057) and reports whether it did. The request was refused,
// not queued: the daemon is shutting down or reloading and admits no new work.
func writeDrainingConflict(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, ErrDraining) {
		return false
	}
	writeError(w, http.StatusConflict, "draining",
		"daemon is draining for shutdown or reload; not admitting new work — retry once it is back")
	return true
}
