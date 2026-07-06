package agent

import (
	"context"
	"strings"
)

// Event type constants emitted by a supported agent CLI stream.
const (
	EventSystem    = "system"
	EventAssistant = "assistant"
	EventResult    = "result"
)

// TurnResult holds the outcome of a single agent subprocess turn.
type TurnResult struct {
	SessionID         string
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	TotalTokens       int
	LastText          string   // most recent assistant text block
	AllTextBlocks     []string // all assistant text blocks across the turn, for tracker comments
	Failed            bool
	InputRequired     bool
	FailureText       string // result field from the error event, or stderr output
	ResultText        string // result field from a successful result event
}

// Logger is a minimal structured logging interface, satisfied by *slog.Logger.
type Logger interface {
	Info(msg string, args ...any)
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

// Runner is the interface for executing a single agent turn.
// Real implementations spawn an agent subprocess; FakeRunner is used in tests.
// log should be pre-seeded with issue context (e.g. issue_identifier) so that
// Claude's live output appears in the log stream with filterable attributes.
// workerHost: if non-empty, the command is executed on that SSH host.
// logDir: if non-empty, the CLAUDE_CODE_LOG_DIR env var is set to this path so
// the agent writes full session logs (including sub-agents) to disk.
// onProgress, if non-nil, is called after each assistant event with the partial
// TurnResult so callers can stream live token/message updates to the dashboard.
type Runner interface {
	RunTurn(ctx context.Context, log Logger, onProgress func(TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (TurnResult, error)
}

// FinalizeResult performs end-of-turn checks on an accumulated TurnResult.
// It scans the assistant text blocks for the input-required sentinel token
// and sets r.InputRequired accordingly.
//
// The project's WORKFLOW.md instructs agents to emit the
// <!-- itervox:needs-input --> sentinel on its own line when they need
// human input before continuing. This is the deterministic fast path shared by
// all backend parsers. The orchestrator may run an additional deterministic
// fallback for successful turns that did not emit an explicit signal, but
// FinalizeResult itself stays literal and backend-agnostic.
//
// Detection order:
//  1. Parser already set InputRequired (e.g. codex turn.failed "human turn") — keep it
//  2. Sentinel token in assistant text or result text
//
// This unifies detection across all backends — Claude and Codex both
// finalize their TurnResults the same way.
func FinalizeResult(r TurnResult) TurnResult {
	if r.InputRequired {
		return r
	}
	if len(r.AllTextBlocks) == 0 && r.ResultText == "" {
		return r
	}
	// Build the check text from text blocks (where the sentinel appears)
	// plus the result text (some agents put the sentinel in the summary).
	combined := strings.Join(r.AllTextBlocks, "\n")
	if r.ResultText != "" {
		combined += "\n" + r.ResultText
	}
	if IsSentinelInputRequired(combined) {
		r.InputRequired = true
	}
	return r
}

// ApplyEvent merges a StreamEvent into the accumulated TurnResult.
func ApplyEvent(r TurnResult, ev StreamEvent) TurnResult {
	switch ev.Type {
	case EventSystem:
		if r.SessionID == "" {
			r.SessionID = ev.SessionID
		}
	case EventAssistant:
		// InProgress events (item.started) carry no token counts or text; skip
		// accumulation to avoid polluting AllTextBlocks if the parser ever adds text.
		if ev.InProgress {
			break
		}
		r.InputTokens += ev.Usage.InputTokens
		r.CachedInputTokens += ev.Usage.CachedInputTokens
		r.OutputTokens += ev.Usage.OutputTokens
		r.TotalTokens = r.InputTokens + r.OutputTokens
		if len(ev.TextBlocks) > 0 {
			r.LastText = ev.TextBlocks[len(ev.TextBlocks)-1]
			r.AllTextBlocks = append(r.AllTextBlocks, ev.TextBlocks...)
		}
	case EventResult:
		r.InputTokens += ev.Usage.InputTokens
		r.CachedInputTokens += ev.Usage.CachedInputTokens
		r.OutputTokens += ev.Usage.OutputTokens
		r.TotalTokens = r.InputTokens + r.OutputTokens
		if ev.SessionID != "" {
			r.SessionID = ev.SessionID
		}
		if ev.IsError {
			r.Failed = true
			r.FailureText = ev.ResultText
		} else {
			r.ResultText = ev.ResultText
		}
		if ev.IsInputRequired {
			r.InputRequired = true
		}
	}
	return r
}
