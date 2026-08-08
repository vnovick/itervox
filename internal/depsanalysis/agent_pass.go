package depsanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// analyzerDescriptionByteCap bounds each issue's rendered Description at
// prompt-build time (#46-2). Chunking already bounds the prompt by issue
// COUNT (deps_analyzer_chunk_size, default 75), but Description itself had
// no size limit of its own — 75 issues with unbounded descriptions was the
// residual unbounded dimension the count-based cap did not cover. 4KB per
// issue keeps the worst-case prompt at the default chunk size around 300KB
// (75 * 4KB), which the analyzer profile has already been proven to handle.
// This caps rendering only: FetchIssues/the sidecar/the fingerprint used by
// PlanIncremental all still see the full, untruncated Description.
const analyzerDescriptionByteCap = 4096

// truncatedSuffix marks a Description cut by analyzerDescriptionByteCap so
// the analyzer (and anyone reading the prompt) can tell a short description
// from one that was cut off mid-thought.
const truncatedSuffix = "…[truncated]"

// truncateDescriptionForPrompt caps s at analyzerDescriptionByteCap bytes,
// cutting at a UTF-8-safe boundary (never splitting a multi-byte rune) and
// appending truncatedSuffix when a cut happened. A no-op when s is already
// within the cap.
func truncateDescriptionForPrompt(s string) string {
	if len(s) <= analyzerDescriptionByteCap {
		return s
	}
	cut := analyzerDescriptionByteCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedSuffix
}

// ErrAnalyzerOutputInvalid is returned when the agent's stdout is not valid
// strict JSON matching the analyzer output contract. Callers should log the
// raw output (or a truncated form) and proceed without an inferred layer.
var ErrAnalyzerOutputInvalid = errors.New("depsanalysis: analyzer output is not valid JSON matching the contract")

// ErrAnalyzerFailed wraps the agent's own failure text (rate-limited, crashed,
// stalled, etc.). Surfaced as the job's error message.
var ErrAnalyzerFailed = errors.New("depsanalysis: analyzer agent failed")

// AgentPassInput carries everything needed to run one analyzer pass via the
// existing agent.Runner.
type AgentPassInput struct {
	Runner        agent.Runner
	Profile       config.AgentProfile
	ProfileName   string
	Issues        []AnalyzerIssue
	TrackerEdges  []TrackerEdge
	WorkspacePath string
	LogDir        string
	Logger        agent.Logger
	// OnProgress, when non-nil, is invoked after each assistant event. Used to
	// prove the job is still moving inside a long chunk — ChunksDone alone
	// cannot distinguish "working" from "wedged".
	OnProgress    func(agent.TurnResult)
	ReadTimeoutMs int
	TurnTimeoutMs int
}

// RunAgentPass invokes the analyzer profile against the supplied issue set
// and returns the inferred edges. The JSON issue list and the tracker-edge
// list are embedded directly into the prompt as fenced JSON blocks (advisor
// Approach A), so the call goes through the same agent.Runner abstraction
// used by every worker — preserving timeout, SSE-stream, and log-dir
// plumbing.
func RunAgentPass(ctx context.Context, input AgentPassInput) ([]InferredEdge, error) {
	if input.Runner == nil {
		return nil, fmt.Errorf("depsanalysis: runner is nil")
	}
	if input.Profile.Command == "" {
		return nil, fmt.Errorf("depsanalysis: profile %q has no command", input.ProfileName)
	}

	prompt := buildAnalyzerPrompt(input.Profile, input.ProfileName, input.Issues, input.TrackerEdges)
	var sessionID string
	res, err := input.Runner.RunTurn(
		ctx,
		input.Logger,
		input.OnProgress,
		&sessionID,
		prompt,
		input.WorkspacePath,
		input.Profile.Command,
		"", // workerHost — analyzer runs locally
		input.LogDir,
		input.ReadTimeoutMs,
		input.TurnTimeoutMs,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAnalyzerFailed, err)
	}
	if res.Failed {
		failureText := res.FailureText
		if failureText == "" {
			failureText = res.ResultText
		}
		return nil, fmt.Errorf("%w: %s", ErrAnalyzerFailed, failureText)
	}
	combined := combineAnalyzerOutput(res)
	edges, parseErr := parseAnalyzerOutput(combined)
	if parseErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrAnalyzerOutputInvalid, parseErr)
	}
	now := time.Now().UTC()
	for i := range edges {
		if edges[i].InferredAt.IsZero() {
			edges[i].InferredAt = now
		}
	}
	return edges, nil
}

// buildAnalyzerPrompt concatenates the profile SOUL + INSTRUCTIONS with the
// issue list + tracker edges + output reminder. The SOUL/INSTRUCTIONS content
// has already been loaded by config.parseAgentProfiles; we trust it here.
func buildAnalyzerPrompt(profile config.AgentProfile, profileName string, issues []AnalyzerIssue, trackerEdges []TrackerEdge) string {
	var b strings.Builder
	if profile.Soul != "" {
		b.WriteString(profile.Soul)
		b.WriteString("\n\n")
	}
	if profile.Instructions != "" {
		b.WriteString(profile.Instructions)
		b.WriteString("\n\n")
	}
	b.WriteString("## Issues\n\n")
	b.WriteString("The following JSON array lists every candidate issue. Read each entry's title, description, and state, then surface any non-tracker-declared dependency relations as strict JSON on stdout.\n\n")
	b.WriteString("```json\n")
	// Cap each issue's Description for rendering only (#46-2) — build a copy
	// so the caller's issues slice (and anything else holding a reference to
	// it, e.g. PlanIncremental's fingerprinting) never sees the truncated
	// text.
	promptIssues := make([]AnalyzerIssue, len(issues))
	for i, iss := range issues {
		iss.Description = truncateDescriptionForPrompt(iss.Description)
		promptIssues[i] = iss
	}
	issueJSON, _ := json.MarshalIndent(promptIssues, "", "  ")
	b.Write(issueJSON)
	b.WriteString("\n```\n\n")
	b.WriteString("## Existing Tracker Edges\n\n")
	b.WriteString("The tracker already declares these relations. Skip them — only emit edges the tracker has not surfaced.\n\n")
	b.WriteString("```json\n")
	trackerJSON, _ := json.MarshalIndent(trackerEdges, "", "  ")
	b.Write(trackerJSON)
	b.WriteString("\n```\n\n")
	b.WriteString("## Output\n\n")
	b.WriteString("Emit exactly one JSON object on stdout: {\"edges\":[{\"source\":\"FOO-12\",\"target\":\"FOO-34\",\"evidence\":\"...\",\"confidence\":0.0}]}. No surrounding prose, no markdown code fence. If no non-tracker relations exist, output {\"edges\":[]}.\n\n")
	b.WriteString("Each edge must include a `confidence` number between 0 and 1 rating how certain you are of the relation:\n")
	b.WriteString("- ~0.9 when the issue text explicitly states the dependency (e.g. \"blocked by\", \"depends on\", \"requires\").\n")
	b.WriteString("- ~0.6 when the relation is inferred from shared files or components mentioned across the issues.\n")
	b.WriteString("- ~0.3 when the relation is only a thematic or topical similarity.\n")
	_ = profileName
	return b.String()
}

func combineAnalyzerOutput(res agent.TurnResult) string {
	parts := make([]string, 0, len(res.AllTextBlocks)+2)
	parts = append(parts, res.AllTextBlocks...)
	if res.LastText != "" {
		parts = append(parts, res.LastText)
	}
	if res.ResultText != "" {
		parts = append(parts, res.ResultText)
	}
	return strings.Join(parts, "\n")
}

// jsonObjectRE finds the first balanced top-level JSON object containing the
// `"edges"` key. We anchor on the key to skip prose paragraphs the analyzer
// might prepend despite the instructions.
var jsonObjectRE = regexp.MustCompile(`(?s)\{[^{}]*"edges"\s*:\s*\[.*?\][^{}]*\}`)

// parsedAnalyzerEdge is the wire shape the analyzer outputs. Kept private
// since the public InferredEdge wraps these with an InferredAt timestamp.
//
// Confidence is captured as json.RawMessage (not float64) so a missing or
// malformed confidence value on one edge never fails decoding of the whole
// output — parseConfidence tolerantly defaults it to 0 instead.
type parsedAnalyzerEdge struct {
	Source     string          `json:"source"`
	Target     string          `json:"target"`
	Evidence   string          `json:"evidence"`
	Confidence json.RawMessage `json:"confidence"`
}

type parsedAnalyzerOutput struct {
	Edges []parsedAnalyzerEdge `json:"edges"`
}

// parseAnalyzerOutput extracts the first JSON object containing an "edges"
// key from the agent's stdout. Operates on the entire combined text to be
// resilient to leading/trailing prose.
func parseAnalyzerOutput(text string) ([]InferredEdge, error) {
	candidate := strings.TrimSpace(text)
	if candidate == "" {
		return nil, errors.New("empty output")
	}
	var out parsedAnalyzerOutput
	if err := json.Unmarshal([]byte(candidate), &out); err == nil {
		return convertParsedEdges(out.Edges), nil
	}
	match := jsonObjectRE.FindString(candidate)
	if match == "" {
		return nil, errors.New("no JSON object containing an edges key found")
	}
	if err := json.Unmarshal([]byte(match), &out); err != nil {
		return nil, fmt.Errorf("parse edges json: %w", err)
	}
	return convertParsedEdges(out.Edges), nil
}

func convertParsedEdges(edges []parsedAnalyzerEdge) []InferredEdge {
	out := make([]InferredEdge, 0, len(edges))
	for _, e := range edges {
		src := strings.TrimSpace(e.Source)
		dst := strings.TrimSpace(e.Target)
		if src == "" || dst == "" {
			continue
		}
		out = append(out, InferredEdge{
			Source:     src,
			Target:     dst,
			Evidence:   strings.TrimSpace(e.Evidence),
			Confidence: parseConfidence(e.Confidence),
		})
	}
	return out
}

// parseConfidence decodes a per-edge confidence value tolerantly: a missing
// field or one that isn't a JSON number defaults to 0 rather than failing
// the analyzer pass. Values are clamped to [0, 1] via clampConfidence (the
// same helper LoadSidecar applies), so an over/under-range value from the
// agent never leaks out of range.
func parseConfidence(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var c float64
	if err := json.Unmarshal(raw, &c); err != nil {
		return 0
	}
	return clampConfidence(c)
}
