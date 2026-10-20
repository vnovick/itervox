package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/config"
)

// SettingsLayersClient backs the schema-driven Settings page (#158): it reads
// the JSONC settings layers and saves one of them through the generic
// comment-preserving writer, applying live settings to the running daemon.
type SettingsLayersClient interface {
	// SettingsLayers reads every settings layer and the effective value of
	// each layered setting.
	SettingsLayers() (SettingsLayersView, error)
	// SaveSettingsLayer writes one layer. It returns an error wrapping
	// ErrUnknownSettingsLayer for a layer it cannot write, ErrSettingsStale
	// when the file changed since req.BaseVersion, ErrSettingsInvalid when a
	// value or the resulting config is not valid, and ErrSettingsReloading
	// when a reload superseded the save.
	SaveSettingsLayer(layer string, req SettingsLayerSaveRequest) (SettingsLayerSaveResult, error)
}

// Settings layer save errors, mapped to 404, 409 and 400.
var (
	ErrUnknownSettingsLayer = errors.New("unknown or disabled settings layer")
	ErrSettingsStale        = errors.New("the settings file changed since it was loaded")
	ErrSettingsInvalid      = errors.New("invalid settings")
)

// SettingsLayerView is one settings layer as the Settings page shows it.
type SettingsLayerView struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
	Exists   bool   `json:"exists"`
	// Raw is the file's text, comments included ("" when it does not exist).
	Raw string `json:"raw"`
	// Version identifies Raw; a save sends it back as baseVersion.
	Version string `json:"version"`
	// Values maps each registered setting the file sets ("agent.max_turns")
	// to its value.
	Values map[string]any `json:"values"`
	// Unknown lists keys the file sets that Itervox does not read.
	Unknown []string `json:"unknown"`
	// Error is why the file could not be read or parsed.
	Error string `json:"error,omitempty"`
}

// SettingsEffective is one layered setting's value in effect and the layer
// it came from ("default", "user", "project", "local", "workflow" or
// "environment").
type SettingsEffective struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	Layer string `json:"layer"`
	Path  string `json:"path,omitempty"`
}

// SettingsLayersView is GET /api/v1/settings/layers.
type SettingsLayersView struct {
	SchemaURL string              `json:"schemaUrl"`
	Layers    []SettingsLayerView `json:"layers"`
	Effective []SettingsEffective `json:"effective"`
}

// SettingsLayerSaveRequest is PUT /api/v1/settings/layers/{layer}: exactly
// one of Values (the form: keys to set, a null value removes the key) and Raw
// (the JSON view's text, written as typed).
type SettingsLayerSaveRequest struct {
	BaseVersion string         `json:"baseVersion"`
	Values      map[string]any `json:"values,omitempty"`
	Raw         *string        `json:"raw,omitempty"`
}

// SettingsLayerSaveResult is the answer to a save: the layers as now on
// disk, plus what the save did.
type SettingsLayerSaveResult struct {
	SettingsLayersView
	// Layer is the layer written.
	Layer string `json:"layer"`
	// Applied lists the changed settings the running daemon applied at once.
	Applied []string `json:"applied"`
	// RestartRequired lists the changed settings that take effect at the
	// next restart or WORKFLOW.md reload.
	RestartRequired []string `json:"restartRequired"`
	// Shadowed maps a key the save set to the higher layer that still wins
	// over it.
	Shadowed map[string]string `json:"shadowed"`
}

func (s *Server) handleSettingsSchema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, config.SettingsSchema())
}

func (s *Server) handleGetSettingsLayers(w http.ResponseWriter, _ *http.Request) {
	if s.settingsLayers == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings layers are not configured")
		return
	}
	view, err := s.settingsLayers.SettingsLayers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleSaveSettingsLayer(w http.ResponseWriter, r *http.Request) {
	if s.settingsLayers == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings layers are not configured")
		return
	}
	var req SettingsLayerSaveRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if (req.Raw == nil) == (req.Values == nil) {
		writeError(w, http.StatusBadRequest, "invalid_body", `send exactly one of "values" and "raw"`)
		return
	}
	res, err := s.settingsLayers.SaveSettingsLayer(chi.URLParam(r, "layer"), req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case errors.Is(err, ErrUnknownSettingsLayer):
		writeError(w, http.StatusNotFound, "unknown_layer", err.Error())
	case errors.Is(err, ErrSettingsStale):
		writeError(w, http.StatusConflict, "settings_stale", err.Error())
	case errors.Is(err, ErrSettingsInvalid):
		writeError(w, http.StatusBadRequest, "settings_invalid", err.Error())
	default:
		writeClientError(w, "settings_write_failed", err)
	}
}
