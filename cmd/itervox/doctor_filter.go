package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker"
)

// filterCheckTimeout bounds the doctor's tracker filter check (#198).
const filterCheckTimeout = 30 * time.Second

// filterCheckSample is how many matching identifiers doctor prints.
const filterCheckSample = 5

// FilterCheck is the outcome of the tracker.filter check (#198): the filter
// validated with the rest of the workflow (an invalid one fails the schema
// check); here its assignees resolve and the tracker reports how many
// issues in the active states it matches.
type FilterCheck struct {
	// Ran is false when the check does not apply (no tracker.filter, or the
	// workflow did not validate) or could not run; SkipReason then says why
	// when it is worth telling.
	Ran        bool
	SkipReason string
	Label      string
	// Unresolvable: an assignee the filter names matches no user — the
	// daemon's polls fail until it is fixed.
	Unresolvable bool
	// ReadError is a redacted error from the count.
	ReadError string
	Matches   int
	Sample    []string
}

// checkTrackerFilter counts the issues tracker.filter matches through the
// same tracker client the daemon builds.
func checkTrackerFilter(ctx context.Context, cfg *config.Config, schemaPassed bool) FilterCheck {
	if cfg == nil || len(cfg.Tracker.Filter) == 0 {
		return FilterCheck{}
	}
	fc := FilterCheck{Label: cfg.Tracker.Filter.Label()}
	switch {
	case !schemaPassed:
		return fc // the schema error already says what is wrong
	case strings.TrimSpace(cfg.Tracker.APIKey) == "":
		fc.SkipReason = "tracker.api_key is empty (set it, or run `itervox doctor --deploy`, which loads .itervox/.env)"
		return fc
	}
	tr, err := buildTracker(cfg)
	if err != nil {
		fc.SkipReason = err.Error()
		return fc
	}
	fm, ok := tr.(tracker.FilterManager)
	if !ok {
		return fc
	}
	fc.Ran = true
	if err := fm.ResolveFilter(ctx, cfg.Tracker.Filter); err != nil {
		if errors.Is(err, tracker.ErrAssigneeNotFound) {
			fc.Unresolvable = true
		}
		fc.ReadError = logging.RedactString(err.Error())
		return fc
	}
	p, err := fm.PreviewFilter(ctx, cfg.Tracker.Filter, filterCheckSample)
	if err != nil {
		fc.ReadError = logging.RedactString(err.Error())
		return fc
	}
	fc.Matches, fc.Sample = p.Count, p.Identifiers
	return fc
}

// filterCheckFailed reports a broken filter (exit 1).
func filterCheckFailed(fc FilterCheck) bool { return fc.Unresolvable }

// renderFilterCheck writes the check's lines into the doctor report.
func renderFilterCheck(b *strings.Builder, fc FilterCheck) {
	switch {
	case fc.Label == "":
		return
	case !fc.Ran:
		if fc.SkipReason != "" {
			fmt.Fprintf(b, "tracker filter: not checked — %s\n", fc.SkipReason)
		}
		return
	case fc.Unresolvable:
		fmt.Fprintf(b, "ERROR: tracker.filter names an assignee this API key cannot see: %s\n", fc.ReadError)
		return
	case fc.ReadError != "":
		fmt.Fprintf(b, "WARNING: could not count the issues tracker.filter matches: %s\n", fc.ReadError)
		return
	}
	sample := ""
	if len(fc.Sample) > 0 {
		sample = " (e.g. " + strings.Join(fc.Sample, ", ") + ")"
	}
	fmt.Fprintf(b, "tracker filter: OK — %s — %d issue(s) in the active states match%s\n", fc.Label, fc.Matches, sample)
	if fc.Matches == 0 {
		fmt.Fprintf(b, "WARNING: tracker.filter matches no issue in the active states; Itervox will dispatch nothing until one matches\n")
	}
}
