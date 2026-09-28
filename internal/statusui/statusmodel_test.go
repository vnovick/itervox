package statusui

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// webStatusModelPath is the web half of CORE-076. The parity test reads the
// TypeScript source directly instead of a generated fixture: the web module is
// the canonical copy, so parsing it leaves exactly two copies (TS and Go) and
// no third file that could itself drift or need regenerating.
//
// ITERVOX_STATUS_MODEL_TS overrides the path (test hook): pointing it at a
// scratch copy with a malformed entry is how the parser's strictness is
// demonstrated end to end, e.g.
//
//	ITERVOX_STATUS_MODEL_TS=/tmp/statusModel.ts go test -run '^TestStatusLabelParity$' ./internal/statusui/
const defaultWebStatusModelPath = "../../web/src/lib/statusModel.ts"

// webSchemasPath holds InputRequiredEntrySchema, whose `state` default decides
// how a row without a state is counted on the web.
const webSchemasPath = "../../web/src/types/schemas.ts"

func webStatusModelPath() string {
	if p := os.Getenv("ITERVOX_STATUS_MODEL_TS"); p != "" {
		return p
	}
	return defaultWebStatusModelPath
}

var (
	// `running: { label: 'Running', noun: 'running', tone: 'success' },`
	webStatusMetaEntryRe = regexp.MustCompile(
		`^(\w+)\s*:\s*\{\s*label\s*:\s*'([^'\n]*)'\s*,\s*noun\s*:\s*'([^'\n]*)'\s*,\s*tone\s*:\s*'([^'\n]*)'\s*,?\s*\}\s*(?:,|$)`)
	// `active: 'Active',`
	webActivityEntryRe = regexp.MustCompile(`^(\w+)\s*:\s*'([^'\n]*)'\s*(?:,|$)`)
	// Blank space and line comments between entries.
	webBlockFillerRe = regexp.MustCompile(`^(?:\s+|//[^\n]*)+`)
	// `state: tolerantEnum('inputRequired.state', [...], 'input_required'),`
	webInputStateDefaultRe = regexp.MustCompile(
		`tolerantEnum\(\s*'inputRequired\.state'\s*,\s*\[[^\]]*\]\s*,\s*'([^']*)'\s*,?\s*\)`)
)

// webBlock returns the `{ ... }` body that follows `export const <name>`.
func webBlock(src, name string) (string, error) {
	re := regexp.MustCompile(`(?s)export const ` + name + `\b[^=]*=\s*\{(.*?)\n\};`)
	m := re.FindStringSubmatch(src)
	if len(m) != 2 {
		return "", fmt.Errorf("could not find `export const %s = { ... };` — update the parser if the file was reformatted", name)
	}
	return m[1], nil
}

// parseWebEntries consumes a whole object-literal body with entryRe. It is
// strict: any text that is not blank space, a line comment or one complete
// entry (a double-quoted string, an extra field, a spread, a computed key…)
// is an error, and so is a duplicate key. A lenient FindAll would silently
// skip such an entry and let the parity check pass without comparing it.
func parseWebEntries(block string, entryRe *regexp.Regexp) (map[string][]string, error) {
	out := map[string][]string{}
	rest := block
	for {
		if loc := webBlockFillerRe.FindStringIndex(rest); loc != nil {
			rest = rest[loc[1]:]
		}
		if rest == "" {
			return out, nil
		}
		m := entryRe.FindStringSubmatch(rest)
		if m == nil {
			line, _, _ := strings.Cut(rest, "\n")
			return nil, fmt.Errorf("unparseable entry %q", strings.TrimSpace(line))
		}
		if _, dup := out[m[1]]; dup {
			return nil, fmt.Errorf("duplicate key %q", m[1])
		}
		out[m[1]] = m[2:]
		rest = rest[len(m[0]):]
	}
}

func parseWebStatusMeta(src string) (map[statusKey]statusMetaEntry, error) {
	block, err := webBlock(src, "STATUS_META")
	if err != nil {
		return nil, err
	}
	raw, err := parseWebEntries(block, webStatusMetaEntryRe)
	if err != nil {
		return nil, fmt.Errorf("STATUS_META: %w", err)
	}
	out := make(map[statusKey]statusMetaEntry, len(raw))
	for k, v := range raw {
		out[statusKey(k)] = statusMetaEntry{Label: v[0], Noun: v[1], Tone: statusTone(v[2])}
	}
	return out, nil
}

func parseWebActivityLabel(src string) (map[statusActivity]string, error) {
	block, err := webBlock(src, "ACTIVITY_LABEL")
	if err != nil {
		return nil, err
	}
	raw, err := parseWebEntries(block, webActivityEntryRe)
	if err != nil {
		return nil, fmt.Errorf("ACTIVITY_LABEL: %w", err)
	}
	out := make(map[statusActivity]string, len(raw))
	for k, v := range raw {
		out[statusActivity(k)] = v[0]
	}
	return out, nil
}

func TestStatusLabelParity(t *testing.T) {
	path := webStatusModelPath()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the web status model must exist; the TUI labels are checked against it")
	src := string(raw)

	// STATUS_META: every entry must parse, and the key sets must match exactly.
	web, err := parseWebStatusMeta(src)
	require.NoErrorf(t, err, "%s", path)
	require.Lenf(t, web, len(statusMeta), "parsed %d STATUS_META entries from %s, TUI has %d", len(web), path, len(statusMeta))
	assert.Equal(t, sortedKeys(web), sortedKeys(statusMeta), "STATUS_META keys differ between web and TUI")
	for k, w := range web {
		assert.Equalf(t, w, statusMeta[k], "statusMeta[%q] differs from web STATUS_META", k)
	}

	// ACTIVITY_LABEL, same rules.
	webActivity, err := parseWebActivityLabel(src)
	require.NoErrorf(t, err, "%s", path)
	require.Lenf(t, webActivity, len(activityLabel), "parsed %d ACTIVITY_LABEL entries from %s, TUI has %d", len(webActivity), path, len(activityLabel))
	assert.Equal(t, webActivity, activityLabel, "activityLabel differs from web ACTIVITY_LABEL")

	// A row without `state` is counted by the schema default (see inputRowState).
	schemas, err := os.ReadFile(webSchemasPath)
	require.NoError(t, err)
	dm := webInputStateDefaultRe.FindStringSubmatch(string(schemas))
	require.Lenf(t, dm, 2, "could not find the inputRequired.state tolerantEnum default in %s", webSchemasPath)
	assert.Equal(t, string(statusInputRequired), dm[1],
		"web defaults a stateless input row to %q; inputRowState must use the same default", dm[1])
	assert.Equal(t, statusInputRequired, inputRowState(server.InputRequiredRow{}))
}

// TestParseWebStatusMetaIsStrict pins the non-vacuous parser (M5-close): each
// malformed shape the lenient FindAll parser used to skip must now be an error.
func TestParseWebStatusMetaIsStrict(t *testing.T) {
	wrap := func(body string) string {
		return "export const STATUS_META: Record<StatusKey, StatusMeta> = {\n" + body + "\n};\n"
	}
	good := "  running: { label: 'Running', noun: 'running', tone: 'success' },\n" +
		"  // a comment line\n" +
		"  idle: { label: 'Idle', noun: 'idle', tone: 'neutral' },"
	got, err := parseWebStatusMeta(wrap(good))
	require.NoError(t, err)
	assert.Len(t, got, 2)

	bad := map[string]string{
		"double_quoted": `  queued: { label: "Queued", noun: "queued", tone: "info" },`,
		"extra_field":   `  queued: { label: 'Queued', noun: 'queued', tone: 'info', hint: 'x' },`,
		"missing_field": `  queued: { label: 'Queued', tone: 'info' },`,
		"spread":        `  ...EXTRA,`,
		"duplicate":     `  running: { label: 'Again', noun: 'again', tone: 'info' },`,
	}
	for name, entry := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := parseWebStatusMeta(wrap(good + "\n" + entry))
			assert.Error(t, err)
		})
	}
}

func sortedKeys[K ~string, V any](m map[K]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

func TestSummarizeStatus_NeedsInputExcludesPendingResume(t *testing.T) {
	s := summarizeStatus(server.StateSnapshot{
		InputRequired: []server.InputRequiredRow{
			{Identifier: "ENG-1", State: "input_required"},
			{Identifier: "ENG-2", State: "pending_input_resume"},
		},
	})
	assert.Equal(t, 1, s.NeedsInput)
	assert.Equal(t, 1, s.Resuming)
}

// Mirrors web statusModel "mixed snapshot" — counts come from the row lists
// (not server Counts), exactly as summarizeStatus does on the web.
func TestSummarizeStatus_MixedSnapshotCountsRows(t *testing.T) {
	s := summarizeStatus(server.StateSnapshot{
		Counts:   server.Counts{Running: 0, Retrying: 0, Paused: 0},
		Running:  []server.RunningRow{{Identifier: "ENG-1"}, {Identifier: "ENG-2"}},
		Retrying: []server.RetryRow{{Identifier: "ENG-3"}},
		Paused:   []string{"ENG-4"},
		InputRequired: []server.InputRequiredRow{
			{Identifier: "ENG-5", State: "input_required"},
			{Identifier: "ENG-6", State: "pending_input_resume"},
			{Identifier: "ENG-7", Context: "Reply received, waiting to resume.\n\nOriginal request:\nx"},
			{Identifier: "ENG-8", Context: "Which database?"},
		},
	})
	// Stateless rows count as needs input, as the web schema default does.
	assert.Equal(t, statusSummary{
		Running: 2, Paused: 1, Retrying: 1, NeedsInput: 3, Resuming: 1,
		Activity: activityActive,
	}, s)
}

func TestSummarizeStatus_ActivityIsWaitingWithoutRunning(t *testing.T) {
	s := summarizeStatus(server.StateSnapshot{Paused: []string{"A"}, Retrying: []server.RetryRow{{Identifier: "B"}}})
	assert.Equal(t, activityWaiting, s.Activity)
	assert.Equal(t, "Waiting", activityLabel[s.Activity])
}

func TestView_HeaderUsesStatusLabels(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running:  []server.RunningRow{{Identifier: "ENG-1", State: "Running"}},
		Retrying: []server.RetryRow{{Identifier: "ENG-3"}},
		Paused:   []string{"ENG-4"},
		InputRequired: []server.InputRequiredRow{
			{Identifier: "ENG-5", State: "input_required"},
			{Identifier: "ENG-6", State: "pending_input_resume"},
		},
	})
	out := stripANSI(readyModel(snap).View())
	assert.Contains(t, out, "AGENTS ▸ 1/5 Active") // len(Running), not Counts.Running
	assert.Contains(t, out, "RETRYING ▸ 1")
	assert.Contains(t, out, "PAUSED ▸ 1")
	assert.Contains(t, out, "1 needs input")
	assert.Contains(t, out, "1 resuming")
	assert.Contains(t, out, "◆─[ NEEDS INPUT ]")
	assert.Contains(t, out, "◆─[ RESUMING ]")
}

// The web parses every input row through InputRequiredEntrySchema, whose
// `state` is tolerantEnum(..., 'input_required'): a missing or unknown state
// becomes input_required before any context-prefix fallback can run. The TUI
// reads the same wire rows and must reach the same count (M5-close status).
func TestSummarizeStatus_StatelessRowsAreNeedsInput(t *testing.T) {
	rows := []server.InputRequiredRow{
		{Identifier: "ENG-1", Context: "Reply received, waiting to resume.\n\nOriginal request:\nx"},
		{Identifier: "ENG-2", Context: "Which database?"},
		{Identifier: "ENG-3", State: "some_future_state"},
		{Identifier: "ENG-4", State: "pending_input_resume"},
	}
	for _, r := range rows[:3] {
		assert.Equalf(t, statusInputRequired, inputRowState(r), "row %s", r.Identifier)
	}
	s := summarizeStatus(server.StateSnapshot{InputRequired: rows})
	assert.Equal(t, 3, s.NeedsInput)
	assert.Equal(t, 1, s.Resuming)
}
