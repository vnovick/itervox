package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker/github"
)

// GitHub state-label check for `itervox doctor` (#75).
//
// The GitHub tracker maps workflow states to issue labels. A configured state
// whose label does not exist on the repository matches nothing: no issue is
// ever dispatched and nothing reports why. Doctor lists every missing label
// with the exact `gh label create` command, and `doctor --fix` creates them.

// labelCheckTimeout bounds the whole label probe (list + optional creates).
var labelCheckTimeout = 15 * time.Second // var: tests shorten it

// defaultLabelColors are the colours `itervox init`'s GitHub template
// suggests; any other state label gets defaultLabelColor.
var defaultLabelColors = map[string]string{
	"todo":        "0075ca",
	"in-progress": "e4e669",
	"in progress": "e4e669",
	"in-review":   "d93f0b",
	"in review":   "d93f0b",
	"done":        "0e8a16",
	"cancelled":   "cccccc",
	"canceled":    "cccccc",
	"backlog":     "f9f9f9",
	"failed":      "b60205",
}

const defaultLabelColor = "ededed"

func labelColor(name string) string {
	if c, ok := defaultLabelColors[strings.ToLower(name)]; ok {
		return c
	}
	return defaultLabelColor
}

// githubStateLabels returns every state name the GitHub tracker reads or
// writes as a label, de-duplicated case-insensitively in config order. Only
// a terminal state of "closed" is GitHub's native issue state (the adapter
// lists closed issues for it); every other field is read or written as a
// literal label, "closed" included, so it is checked like any other.
func githubStateLabels(t config.TrackerConfig) []string {
	var out []string
	seen := map[string]bool{}
	add := func(native bool, names ...string) {
		for _, n := range names {
			n = strings.TrimSpace(n)
			key := strings.ToLower(n)
			if n == "" || (native && key == "closed") || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, n)
		}
	}
	add(false, t.ActiveStates...)
	add(false, t.WorkingState, t.CompletionState)
	add(true, t.TerminalStates...)
	add(false, t.BacklogStates...)
	add(false, t.FailedState)
	return out
}

// withoutLabels returns names minus drop, compared case-insensitively.
func withoutLabels(names, drop []string) []string {
	var out []string
	for _, n := range names {
		if !slices.ContainsFunc(drop, func(d string) bool { return strings.EqualFold(d, n) }) {
			out = append(out, n)
		}
	}
	return out
}

// labelChecker is the slice of the GitHub client the label check uses.
type labelChecker interface {
	ListLabels(ctx context.Context) ([]string, error)
	CreateLabel(ctx context.Context, name, color string) error
}

// newLabelChecker builds the client from the loaded workflow. A package
// variable so tests could substitute it; the shipped tests use a fake GitHub
// via tracker.endpoint instead.
var newLabelChecker = func(cfg *config.Config) labelChecker {
	return github.NewClient(github.ClientConfig{
		APIKey:      cfg.Tracker.APIKey,
		ProjectSlug: cfg.Tracker.ProjectSlug,
		Endpoint:    cfg.Tracker.Endpoint,
	})
}

// LabelCheck is the outcome of the state-label check.
type LabelCheck struct {
	// Ran is false when the check does not apply (not a GitHub tracker) or
	// could not run; SkipReason then says why when it is worth telling.
	Ran        bool
	SkipReason string
	Repo       string
	Checked    []string
	Missing    []string
	// APIError is a redacted error from listing labels. Reported, never fatal.
	APIError string
}

// checkGitHubLabels lists the repository's labels and diffs them against the
// configured state labels. GitHub label names are case-insensitive.
func checkGitHubLabels(ctx context.Context, cfg *config.Config) LabelCheck {
	if cfg == nil || cfg.Tracker.Kind != "github" {
		return LabelCheck{}
	}
	lc := LabelCheck{Repo: cfg.Tracker.ProjectSlug, Checked: githubStateLabels(cfg.Tracker)}
	if len(lc.Checked) == 0 {
		return lc
	}
	if strings.TrimSpace(cfg.Tracker.APIKey) == "" {
		lc.SkipReason = "tracker.api_key is empty (set GITHUB_TOKEN, or run `itervox doctor --deploy`, which loads .itervox/.env)"
		return lc
	}
	have, err := newLabelChecker(cfg).ListLabels(ctx)
	if err != nil {
		lc.APIError = logging.RedactString(err.Error())
		return lc
	}
	lc.Ran = true
	existing := make(map[string]bool, len(have))
	for _, n := range have {
		existing[strings.ToLower(n)] = true
	}
	for _, n := range lc.Checked {
		if !existing[strings.ToLower(n)] {
			lc.Missing = append(lc.Missing, n)
		}
	}
	return lc
}

func ghLabelCreateCommand(repo, name string) string {
	return fmt.Sprintf("gh label create %q --color %q --repo %s", name, labelColor(name), repo)
}

func renderLabelCheck(b *strings.Builder, lc LabelCheck) {
	switch {
	case lc.APIError != "":
		fmt.Fprintf(b, "github labels: could not check %s — %s\n", lc.Repo, lc.APIError)
	case lc.SkipReason != "":
		fmt.Fprintf(b, "github labels: not checked — %s\n", lc.SkipReason)
	case !lc.Ran:
		// Not a GitHub tracker, or no state labels configured: nothing to say.
	case len(lc.Missing) == 0:
		fmt.Fprintf(b, "github labels: OK (%d state labels present on %s)\n", len(lc.Checked), lc.Repo)
	default:
		fmt.Fprintf(b, "ERROR: %d state label(s) missing on %s — issues in those states are never dispatched or moved. Create them with `itervox doctor --fix`, or:\n",
			len(lc.Missing), lc.Repo)
		for _, n := range lc.Missing {
			fmt.Fprintf(b, "  %s\n", ghLabelCreateCommand(lc.Repo, n))
		}
	}
}

// fixMissingLabels creates lc.Missing on the repository. Unless assumeYes is
// set it asks for confirmation on in and creates nothing on any answer other
// than y/yes (including EOF, so a non-interactive run without --yes is safe).
// It returns the labels it created and the first creation error.
func fixMissingLabels(cfg *config.Config, lc LabelCheck, assumeYes bool, in io.Reader, out io.Writer) ([]string, error) {
	if !lc.Ran || len(lc.Missing) == 0 {
		return nil, nil
	}
	if !assumeYes {
		_, _ = fmt.Fprintf(out, "Create %d label(s) on %s: %s? [y/N] ", len(lc.Missing), lc.Repo, strings.Join(lc.Missing, ", "))
		answer, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			_, _ = fmt.Fprintln(out, "no labels created (pass --yes to skip this prompt)")
			return nil, nil
		}
	}
	// The deadline starts after the answer: a person may take any time to
	// read the prompt, and that must not eat the API calls' budget.
	ctx, cancel := context.WithTimeout(context.Background(), labelCheckTimeout)
	defer cancel()
	client := newLabelChecker(cfg)
	var created []string
	for _, n := range lc.Missing {
		if err := client.CreateLabel(ctx, n, labelColor(n)); err != nil {
			return created, fmt.Errorf("create label %q: %s", n, logging.RedactString(err.Error()))
		}
		created = append(created, n)
		_, _ = fmt.Fprintf(out, "created label %q on %s\n", n, lc.Repo)
	}
	return created, nil
}
