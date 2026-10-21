package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/rules"
)

// RulesClient backs the Rules editor (#157). Rules are files, not runtime
// config: reads and writes go to the layer files, and the daemon reads them
// again for every run, so no reload or event-loop state is involved.
type RulesClient interface {
	// RulesLayers reads every rules layer from disk.
	RulesLayers() RulesView
	// SaveRulesLayer writes one layer through the comment-preserving JSONC
	// writer and returns it as now on disk. It returns rules.ErrStale when the
	// file changed since req.BaseVersion, rules.ErrInvalid (wrapped) when the
	// result is not a valid rules file, and ErrUnknownRulesLayer for a layer
	// that is not writable.
	SaveRulesLayer(layer string, req RulesSaveRequest) (RulesLayerView, error)
	// PreviewRules returns the rules a run of profile on the issue would get.
	// Both are optional: the issue supplies its profile and backend
	// overrides, and an empty profile is the default.
	PreviewRules(ctx context.Context, profile, identifier string) (RulesPreview, error)
}

// ErrUnknownRulesLayer is returned for a layer name the editor cannot write.
var ErrUnknownRulesLayer = errors.New("unknown or disabled rules layer")

// RulesView is GET /api/v1/rules.
type RulesView struct {
	SchemaURL string           `json:"schemaUrl"`
	Layers    []RulesLayerView `json:"layers"`
}

// RulesLayerView is one rules layer as the editor shows it.
type RulesLayerView struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
	Exists   bool   `json:"exists"`
	// Raw is the file's text, comments included ("" when it does not exist).
	Raw string `json:"raw"`
	// Version identifies Raw; a save sends it back as baseVersion.
	Version string       `json:"version"`
	Rules   []rules.Rule `json:"rules"`
	// Error is why the file could not be read or parsed; such a layer
	// contributes no rules to runs until it is fixed.
	Error string `json:"error,omitempty"`
}

// RulesSaveRequest is PUT /api/v1/rules/{layer}: exactly one of Raw (the JSON
// view's text, written as is) and Rules (the form view, written field by
// field so the file's comments stay).
type RulesSaveRequest struct {
	BaseVersion string        `json:"baseVersion"`
	Raw         *string       `json:"raw,omitempty"`
	Rules       *[]rules.Rule `json:"rules,omitempty"`
}

// RulesPreview is GET /api/v1/rules/preview: the effective rules for a run.
type RulesPreview struct {
	Profile    string             `json:"profile"`
	Backend    string             `json:"backend"`
	Identifier string             `json:"identifier,omitempty"`
	Projects   []string           `json:"projects"`
	Rules      []rules.Effective  `json:"rules"`
	Conflicts  []rules.Conflict   `json:"conflicts"`
	Errors     []rules.LayerError `json:"errors"`
	Block      string             `json:"block"`
	Hash       string             `json:"hash"`
	IDs        []string           `json:"ids"`
	// TemplatePlacesRules is set when the workflow prompt or the profile's
	// files print {{ rules }}: the run then gets no automatic block.
	TemplatePlacesRules bool `json:"templatePlacesRules"`
}

func (s *Server) handleGetRules(w http.ResponseWriter, _ *http.Request) {
	if s.rules == nil {
		writeError(w, http.StatusServiceUnavailable, "rules_unavailable", "rules are not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.rules.RulesLayers())
}

func (s *Server) handleSaveRules(w http.ResponseWriter, r *http.Request) {
	if s.rules == nil {
		writeError(w, http.StatusServiceUnavailable, "rules_unavailable", "rules are not configured")
		return
	}
	var req RulesSaveRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if (req.Raw == nil) == (req.Rules == nil) {
		writeError(w, http.StatusBadRequest, "invalid_body", `send exactly one of "raw" and "rules"`)
		return
	}
	layer, err := s.rules.SaveRulesLayer(chi.URLParam(r, "layer"), req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, layer)
	case errors.Is(err, ErrUnknownRulesLayer):
		writeError(w, http.StatusNotFound, "unknown_layer", err.Error())
	case errors.Is(err, rules.ErrStale):
		writeError(w, http.StatusConflict, "rules_stale", err.Error())
	case errors.Is(err, rules.ErrInvalid):
		writeError(w, http.StatusBadRequest, "rules_invalid", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "rules_write_failed", err.Error())
	}
}

func (s *Server) handlePreviewRules(w http.ResponseWriter, r *http.Request) {
	if s.rules == nil {
		writeError(w, http.StatusServiceUnavailable, "rules_unavailable", "rules are not configured")
		return
	}
	q := r.URL.Query()
	preview, err := s.rules.PreviewRules(r.Context(), q.Get("profile"), q.Get("issue"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "preview_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
