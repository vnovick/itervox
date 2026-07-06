package depsanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

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
		nil, // onProgress
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
	issueJSON, _ := json.MarshalIndent(issues, "", "  ")
	b.Write(issueJSON)
	b.WriteString("\n```\n\n")
	b.WriteString("## Existing Tracker Edges\n\n")
	b.WriteString("The tracker already declares these relations. Skip them — only emit edges the tracker has not surfaced.\n\n")
	b.WriteString("```json\n")
	trackerJSON, _ := json.MarshalIndent(trackerEdges, "", "  ")
	b.Write(trackerJSON)
	b.WriteString("\n```\n\n")
	b.WriteString("## Output\n\n")
	b.WriteString("Emit exactly one JSON object on stdout: {\"edges\":[{\"source\":\"FOO-12\",\"target\":\"FOO-34\",\"evidence\":\"...\"}]}. No surrounding prose, no markdown code fence. If no non-tracker relations exist, output {\"edges\":[]}.\n")
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
type parsedAnalyzerEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Evidence string `json:"evidence"`
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
			Source:   src,
			Target:   dst,
			Evidence: strings.TrimSpace(e.Evidence),
		})
	}
	return out
}
