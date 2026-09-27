package orchestrator

import (
	"strings"
	"testing"
)

// TestRateLimitClassifier_VendorStrings pins the default rate-limit
// classifier (IsRateLimitFailure → defaultRateLimitErrorPatterns) against
// the vendors' current quota wording, verbatim, including the U+2019
// apostrophe that upstream Codex uses ("You’ve"), plus negative and
// near-miss rows so the broader phrases ("try again at", "you've hit your",
// "usage limit") cannot silently over-match unrelated failures (CORE-009).
//
// Each vendor row records where it was copied from and when. The bare
// "429"/"quota" defaults are deliberately NOT exercised or narrowed here —
// that is CORE-030's scope.
func TestRateLimitClassifier_VendorStrings(t *testing.T) {
	const (
		claudeErrorsDoc = "code.claude.com/docs/en/errors (copied 2026-09-25)"
		codexErrorRS    = "github.com/openai/codex codex-rs/protocol/src/error.rs@main (copied 2026-09-25)"
		// No public URL exists for this fixture (M0-close G13). It is the
		// verbatim failure text of a Claude Code subagent turn (model
		// claude-sonnet-5) that stopped when the account hit its weekly
		// usage limit on 2026-09-25, during the M0-close orchestration
		// session. Wire facts: the Anthropic API answered HTTP 429 with
		// error type "rate_limit"; the CLI rendered it as the message below
		// (the "You've hit your weekly limit" entry of
		// code.claude.com/docs/en/errors plus the "· resets <time> (<tz>)"
		// suffix). No source URL is claimed: a live API response has no
		// URL. The retrievable reference is the Anthropic request id the
		// 429 carried (request-id header / error body "request_id"),
		// anthropicLiveRequestID below, which Anthropic can look up
		// (M0-close re-check G13).
		anthropicLiveRequestID = "req_011CfPG8Xq463kN1MmBUhZjJ"
		anthropicLive          = "Anthropic API HTTP 429, error type rate_limit, weekly usage limit, model claude-sonnet-5, Claude Code subagent 2026-09-25, request id " + anthropicLiveRequestID + " (verbatim CLI text; no URL exists for a live API response)"
	)
	cases := []struct {
		name   string
		source string
		msg    string
		want   bool
	}{
		// ── Claude Code (code.claude.com/docs/en/errors) ─────────────────
		{"claude session limit", claudeErrorsDoc, "You've hit your session limit", true},
		{"claude weekly limit", claudeErrorsDoc, "You've hit your weekly limit", true},
		{"claude opus limit", claudeErrorsDoc, "You've hit your Opus limit", true},
		{"claude sonnet limit", claudeErrorsDoc, "You've hit your Sonnet limit", true},
		{"claude monthly spend limit", claudeErrorsDoc, "You've hit your monthly spend limit", true},
		{"claude individual spend limit", claudeErrorsDoc, "You've hit your individual spend limit", true},
		{"claude org spend limit", claudeErrorsDoc, "You've hit your org's monthly spend limit", true},
		{"claude team shared budget", claudeErrorsDoc, "You've hit your team's shared budget", true},
		{"claude individual usage limit", claudeErrorsDoc, "You've hit your individual usage limit", true},
		{"claude credit balance", claudeErrorsDoc, "Credit balance is too low", true},
		{"claude 1M context credits", claudeErrorsDoc, "Usage credits required for 1M context", true},
		{"claude temporarily limiting", claudeErrorsDoc, "Server is temporarily limiting requests", true},
		{"claude session limit curly", claudeErrorsDoc + " + U+2019 variant", "You’ve hit your session limit", true},
		{"claude opus limit curly", claudeErrorsDoc + " + U+2019 variant", "You’ve hit your Opus limit", true},

		// ── Live Anthropic fixture ───────────────────────────────────────
		{"anthropic weekly limit live", anthropicLive, "You've hit your weekly limit · resets 7pm (Asia/Jerusalem)", true},
		{"anthropic weekly limit live curly", anthropicLive + " + U+2019 variant", "You’ve hit your weekly limit · resets 7pm (Asia/Jerusalem)", true},

		// ── Codex (codex-rs/protocol/src/error.rs, UsageLimitReachedError) ─
		{"codex usage limit bare", codexErrorRS, "You’ve hit your usage limit.", true},
		{"codex usage limit try again at", codexErrorRS, "You’ve hit your usage limit. Try again at 3:00 PM.", true},
		{"codex usage limit named model", codexErrorRS, "You’ve hit your usage limit for gpt-5-codex. Switch to another model now, or try again at 3:00 PM.", true},
		{"codex usage limit plus upsell", codexErrorRS, "You’ve hit your usage limit. Upgrade to Plus to continue using Codex (https://chatgpt.com/explore/plus), or try again later.", true},
		{"codex usage limit ascii apostrophe", codexErrorRS + " (ASCII variant)", "You've hit your usage limit. Try again later.", true},
		{"codex spend cap owner", codexErrorRS, "You hit your spend cap set in your workspace. Increase your spend cap to continue.", true},
		{"codex spend cap member", codexErrorRS, "You hit your spend cap set by the owner of your workspace. Ask an owner to increase your spend cap to continue.", true},
		{"codex workspace out of credits", codexErrorRS, "Your workspace is out of credits. Add credits to continue.", true},

		// ── Negative rows: unrelated failures must NOT classify ───────────
		{"neg compile error", "synthetic", "internal/foo/bar.go:12:3: undefined: fooBar\nFAIL\tgithub.com/x/y [build failed]", false},
		{"neg git push rejected", "synthetic", "! [rejected]        main -> main (fetch first)\nerror: failed to push some refs to 'github.com:acme/app.git'", false},
		{"neg network timeout", "synthetic", "dial tcp 10.0.0.12:443: i/o timeout", false},
		{"neg http 500 body", "synthetic", `HTTP 500 Internal Server Error: {"error":"internal_error","message":"unexpected server failure"}`, false},
		{"neg tracker 403", "synthetic", "linear: HTTP 403 Forbidden: you do not have access to this team", false},
		// Near-miss rows: fragments of the new patterns in unrelated contexts.
		{"near-miss try again later", "synthetic", "lock held by another process; try again later", false},
		{"near-miss usage banner", "synthetic", "usage: itervox [flags]\n  -workflow string", false},
		{"near-miss result limit", "synthetic", "limit of 100 results returned; pass --all to page", false},
		{"near-miss you hit your (no apostrophe)", "synthetic", "if you hit your editor's save key the watcher reloads; nothing to do", false},
		{"near-miss disk usage", "synthetic", "disk usage is at 91%; session closed", false},
		// M0-close G12: the two generic phrases CORE-009 added, verbatim, in
		// non-rate-limit contexts. A bare substring match on either one
		// classified these as rate-limited.
		{"neg try again at network timeout", "synthetic (codex-verdict G12 example)", "network timeout; try again at 15:00", false},
		{"neg try again at maintenance lock", "synthetic", "database is locked for maintenance; try again at 02:30 UTC", false},
		{"neg you've hit your breakpoint", "synthetic", "You've hit your breakpoint at main.go:42; press c to continue", false},
		// M0-close re-check G12: "limit" must be a whole word — a clause
		// rule matching it as a bare substring classified "limiter" as limit
		// wording (codex-recheck.md counterexample, verbatim).
		{"neg you've hit your breakpoint at limiter.go", "synthetic (codex-recheck G12 counterexample)", "You've hit your breakpoint at limiter.go:42; press c to continue", false},
		{"neg you've hit your budgeting helper", "synthetic", "You've hit your budgeting helper's assertion", false},
		// M0-close re-check 2 N10: the clause is cut at maxGap bytes, and
		// that cut must not act as a word boundary — here it falls right
		// after "limit" inside "limiter" (codex-recheck2.md counterexample,
		// verbatim). Boundaries are judged against the original message.
		{"neg you've hit your breakpoint at long limiter.go path", "synthetic (codex-recheck2 N10 counterexample)", "You've hit your breakpoint at internal/request_dispatcher/limiter.go:42; press c to continue", false},
		{"neg you've hit your first error curly", "synthetic", "Looks like you’ve hit your first compile error — see the log above", false},
		// Limit wording elsewhere in the message must not complete the
		// opener: the clause rule requires it in the SAME clause.
		{"neg you've hit your + later limit wording", "synthetic", "You've hit your breakpoint; output limited to 100 lines", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// M0-close re-check G13: a live row must carry a retrievable
			// reference — the provider request id — not only wire facts.
			if strings.HasPrefix(tc.source, anthropicLive) && !strings.Contains(tc.source, anthropicLiveRequestID) {
				t.Fatalf("live row %q has no provider request id in its source %q", tc.name, tc.source)
			}
			if got := IsRateLimitFailure(tc.msg); got != tc.want {
				t.Fatalf("IsRateLimitFailure(%q) = %v, want %v (source: %s)", tc.msg, got, tc.want, tc.source)
			}
		})
	}

	// Operator-supplied patterns are normalised the same way: a pattern
	// typed with a curly apostrophe must match the ASCII message and vice
	// versa (the pattern list itself stays under cfgMu at the call site).
	t.Run("operator pattern curly/ascii normalisation", func(t *testing.T) {
		if !IsRateLimitFailureWithPatterns("You've been throttled", []string{"you’ve been throttled"}) {
			t.Fatal("curly operator pattern did not match ASCII message")
		}
		if !IsRateLimitFailureWithPatterns("You’ve been throttled", []string{"you've been throttled"}) {
			t.Fatal("ASCII operator pattern did not match curly message")
		}
	})
}
