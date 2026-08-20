package main

import "testing"

// TestParseLsofHolder pins the PID extraction that lets doctor tell "another
// process holds our port" from "our own daemon is listening".
//
// server.port now defaults to 8090, so config load always populates
// cfg.Server.Port and doctor's `!= nil` guard no longer means "the operator
// named a port". Without the PID comparison, every healthy daemon reported
// its own listening socket as a collision — a WARNING plus exit 1 from
// `itervox doctor` on a working setup.
func TestParseLsofHolder(t *testing.T) {
	const sample = "COMMAND   PID           USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n" +
		"itervox 51234 vladimirnovick    7u  IPv4 0x1234      0t0  TCP 127.0.0.1:8090 (LISTEN)\n"

	desc, pid := parseLsofHolder(sample)
	if pid != 51234 {
		t.Fatalf("pid = %d, want 51234 — doctor cannot recognise its own daemon without it", pid)
	}
	if desc == "" || !contains(desc, "itervox") {
		t.Fatalf("desc = %q, want the operator-facing holder line", desc)
	}

	// Header-only output means nothing is listening.
	if d, p := parseLsofHolder("COMMAND PID USER\n"); d != "" || p != 0 {
		t.Fatalf("header-only output: got (%q, %d), want (\"\", 0)", d, p)
	}
	if d, p := parseLsofHolder(""); d != "" || p != 0 {
		t.Fatalf("empty output: got (%q, %d), want (\"\", 0)", d, p)
	}
	// A malformed PID column must degrade to 0 (treated as "not us") rather
	// than panicking or matching some other process by accident.
	if _, p := parseLsofHolder("COMMAND PID USER\nitervox notanumber vlad\n"); p != 0 {
		t.Fatalf("unparseable pid: got %d, want 0", p)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
