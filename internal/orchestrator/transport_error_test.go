package orchestrator

import "testing"

func TestIsTransportFailure_MatchesConfiguredPatterns(t *testing.T) {
	patterns := []string{"stream disconnected", "connection reset"}
	cases := []struct {
		msg  string
		want bool
	}{
		{"agent runner stream disconnected after 30s", true},
		{"upstream connection reset by peer", true},
		{"STREAM DISCONNECTED — codex", true},
		{"some other error", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsTransportFailure(tc.msg, patterns); got != tc.want {
			t.Errorf("IsTransportFailure(%q) = %v; want %v", tc.msg, got, tc.want)
		}
	}
}

func TestIsTransportFailure_EmptyPatternsNeverMatches(t *testing.T) {
	if IsTransportFailure("stream disconnected", nil) {
		t.Error("nil patterns must never match")
	}
	if IsTransportFailure("stream disconnected", []string{}) {
		t.Error("empty patterns must never match")
	}
}

func TestPausedReasonTransportError_IsCanonical(t *testing.T) {
	if PausedReasonTransportError != "transport_error" {
		t.Errorf("PausedReasonTransportError drifted: %q", PausedReasonTransportError)
	}
}
