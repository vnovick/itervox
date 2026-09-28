package orchestrator

// CORE-030 — the default rate-limit classifier reads the exhausted-retry
// exit cause in two fields: the agent-reported failure and the CLI's own
// stderr (the "stderr: " segment the runners' formatFailureText appends).
// Bare "429"/"quota" substrings no longer match anywhere; a standalone 429
// token matches only on the agent side; anchored 429/quota forms and the
// vendor phrases match on both sides, because pre-stream auth/credit/usage
// failures print the vendor phrase only on stderr.
//
// The messages below are shaped exactly as the worker's exit cause reaches
// the classifier: "turn N: " + FailureText, where FailureText is
// "<agent failure> | stderr: <stderr>" or "stderr: <stderr>" when the agent
// reported nothing (formatFailureText, internal/agent/failure_text.go;
// TestSplitFailureText_RoundTripsFormatFailureText pins that layout).

import "testing"

// exitCause renders the worker's exit-cause text for a failed turn whose
// FailureText has the given agent-side and stderr-side parts.
func exitCause(agentFailure, stderr string) string {
	switch {
	case agentFailure != "" && stderr != "":
		return "turn 1: " + agentFailure + " | stderr: " + stderr
	case stderr != "":
		return "turn 1: stderr: " + stderr
	default:
		return "turn 1: " + agentFailure
	}
}

func TestRateLimitClassifier_IgnoresStderr429(t *testing.T) {
	for _, stderr := range []string{"pr-1429.json", "TestQuotaHandler", "disk quota exceeded"} {
		t.Run(stderr, func(t *testing.T) {
			if msg := exitCause("", stderr); IsRateLimitFailure(msg) {
				t.Fatalf("IsRateLimitFailure(%q) = true; stderr-only %q must not classify", msg, stderr)
			}
			// Read-timeout shape: the agent side is the runner's own read
			// error text (resolveFailureText's fallback), stderr follows.
			if msg := exitCause("agent: read timeout after 30000ms idle", stderr); IsRateLimitFailure(msg) {
				t.Fatalf("IsRateLimitFailure(%q) = true; stderr %q must not classify", msg, stderr)
			}
		})
	}
}

func TestRateLimitClassifier_VendorPhraseInStderrStillMatches(t *testing.T) {
	for _, stderr := range []string{
		"You've hit your usage limit",
		"You’ve hit your usage limit. Try again at 3:00 PM.",
		"Credit balance is too low",
		"Error: HTTP 429 Too Many Requests",
	} {
		t.Run(stderr, func(t *testing.T) {
			if msg := exitCause("", stderr); !IsRateLimitFailure(msg) {
				t.Fatalf("IsRateLimitFailure(%q) = false; the vendor phrase on stderr alone must still classify", msg)
			}
		})
	}
}

func TestRateLimitClassifier_FieldSplitNegatives(t *testing.T) {
	cases := []struct {
		name         string
		agentFailure string
		stderr       string
		want         bool
	}{
		{"agent disk quota exceeded", "disk quota exceeded", "", false},
		{"agent pr-1429.json", "pr-1429.json", "", false},
		{"agent TestQuotaHandler", "TestQuotaHandler", "", false},
		{"stderr pr-1429.json", "", "pr-1429.json", false},
		{"stderr bare-token 429 is agent-only", "", "API Error: 429 {", false},
		{"agent bare-token 429", "API Error: 429 {", "", true},
		{"agent insufficient_quota", "insufficient_quota", "", true},
		{"stderr HTTP 429 Too Many Requests", "", "HTTP 429 Too Many Requests", true},
		// Extra rows beyond the spec table.
		{"agent bare-token 429 with unrelated stderr", `API Error: 429 {"type":"error"}`, "warning: telemetry disabled", true},
		{"stderr status: 429", "", "request failed, status: 429", true},
		{"stderr (429)", "", "upstream answered (429)", true},
		{"stderr quota exceeded for", "", "Quota exceeded for quota metric 'Requests'", true},
		{"stderr usage quota", "", "monthly usage quota reached", true},
		{"openai current quota stays matched", "RateLimitError: You exceeded your current quota", "", true},
		{"agent issue-4290 number", "see issue-4290 for details", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := exitCause(tc.agentFailure, tc.stderr)
			if got := IsRateLimitFailure(msg); got != tc.want {
				t.Fatalf("IsRateLimitFailure(%q) = %v, want %v", msg, got, tc.want)
			}
		})
	}

	// Operator-supplied patterns stay verbatim substrings over both fields.
	t.Run("operator pattern still matches stderr", func(t *testing.T) {
		if !IsRateLimitFailureWithPatterns(exitCause("", "pr-1429.json"), []string{"1429"}) {
			t.Fatal("an operator pattern must still match stderr text verbatim")
		}
	})
}
