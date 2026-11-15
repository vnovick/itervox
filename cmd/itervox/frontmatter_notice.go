package main

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/config"
)

// reportedFrontMatter holds the front-matter notices already logged by this
// process (#239), so a WORKFLOW.md reload does not repeat them: each
// deprecation list and each conflict is reported once.
var reportedFrontMatter sync.Map

// logFrontMatterReport logs, once per process, the layered keys WORKFLOW.md's
// front matter still sets (deprecated: they still apply, and move to the
// settings file) and each key a settings file also sets (the file wins).
func logFrontMatterReport(workflowPath string, r config.FrontMatterReport) {
	if len(r.Deprecated) > 0 {
		id := "deprecated\x00" + workflowPath + "\x00" + strings.Join(r.Deprecated, ",")
		if _, seen := reportedFrontMatter.LoadOrStore(id, struct{}{}); !seen {
			slog.Warn("config: WORKFLOW.md front-matter settings are deprecated; they still apply. Run `itervox init --update` to move them to the settings file (itervox doctor lists each)",
				"workflow", filepath.Base(workflowPath), "keys", r.Deprecated, "moves_to", config.FrontMatterMovesTo)
		}
	}
	for _, c := range r.Conflicts {
		id := "conflict\x00" + workflowPath + "\x00" + c.Key + "\x00" + c.Path
		if _, seen := reportedFrontMatter.LoadOrStore(id, struct{}{}); !seen {
			slog.Warn("config: "+c.Message(), "key", c.Key, "layer", c.Layer, "settings_file", c.Path)
		}
	}
}
