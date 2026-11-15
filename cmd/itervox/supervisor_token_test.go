package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSupervisorEnv is an in-memory supervisorEnv.
type fakeSupervisorEnv map[string]string

func (f fakeSupervisorEnv) env() supervisorEnv {
	return supervisorEnv{
		lookup: func(k string) (string, bool) { v, ok := f[k]; return v, ok },
		set:    func(k, v string) error { f[k] = v; return nil },
		unset:  func(k string) error { delete(f, k); return nil },
	}
}

// TestAdoptSupervisorTokenInstallsAndClears pins how a supervisor token
// becomes the API token: it replaces a different pinned ITERVOX_API_TOKEN,
// ITERVOX_SUPERVISOR_TOKEN is removed so children do not carry a second copy,
// and an invalid value is refused with an error that does not quote it.
func TestAdoptSupervisorTokenInstallsAndClears(t *testing.T) {
	const tok = "supervisor-token-0123456789abcdef"

	env := fakeSupervisorEnv{supervisorTokenEnv: tok + "\n", "ITERVOX_API_TOKEN": "pinned-in-dotenv-0123456789"}
	adopted, replaced, err := adoptSupervisorToken(env.env())
	if err != nil || !adopted || !replaced {
		t.Fatalf("adopt = (%v, %v, %v), want (true, true, nil)", adopted, replaced, err)
	}
	if env["ITERVOX_API_TOKEN"] != tok {
		t.Fatalf("ITERVOX_API_TOKEN = %q, want the trimmed supervisor token", env["ITERVOX_API_TOKEN"])
	}
	if _, still := env[supervisorTokenEnv]; still {
		t.Fatalf("%s must be removed from the environment after adoption", supervisorTokenEnv)
	}

	same := fakeSupervisorEnv{supervisorTokenEnv: tok, "ITERVOX_API_TOKEN": tok}
	if _, replaced, _ := adoptSupervisorToken(same.env()); replaced {
		t.Fatal("an identical ITERVOX_API_TOKEN is not a replacement")
	}

	none := fakeSupervisorEnv{"ITERVOX_API_TOKEN": "pinned-in-dotenv-0123456789"}
	if adopted, _, err := adoptSupervisorToken(none.env()); adopted || err != nil {
		t.Fatalf("no supervisor token: adopt = (%v, %v), want (false, nil)", adopted, err)
	}
	if none["ITERVOX_API_TOKEN"] != "pinned-in-dotenv-0123456789" {
		t.Fatal("without a supervisor token ITERVOX_API_TOKEN must be untouched")
	}

	for _, bad := range []string{"short-secret", "has a space inside 0123456789", strings.Repeat("x", maxSupervisorTokenLen+1)} {
		env := fakeSupervisorEnv{supervisorTokenEnv: bad}
		_, _, err := adoptSupervisorToken(env.env())
		if err == nil {
			t.Fatalf("token %q should be refused", bad)
		}
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("the refusal must not quote the token: %v", err)
		}
		if _, set := env["ITERVOX_API_TOKEN"]; set {
			t.Fatal("a refused token must not be installed")
		}
	}

	failing := fakeSupervisorEnv{supervisorTokenEnv: tok}
	e := failing.env()
	e.set = func(string, string) error { return errors.New("boom") }
	if _, _, err := adoptSupervisorToken(e); err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("a failed install must error without the value, got %v", err)
	}
}

// TestSupervisedAnnounceNeverPrintsToken pins the dashboard URL line under a
// supervisor token: with stderr a terminal AND ITERVOX_PRINT_TOKEN=1 — the
// two knobs that otherwise force the tokenised URL — neither stderr nor a
// log file sink carries the token, and no api-token file is written.
func TestSupervisedAnnounceNeverPrintsToken(t *testing.T) {
	logsDir := t.TempDir()
	s := newTokenSinks()
	announceDashboardToken(s.stderrOnly, s.fanout, dashboardTokenAnnounce{
		Addr:        "127.0.0.1:8090",
		Token:       sentinelToken,
		LogsDir:     logsDir,
		PrintEnv:    "1",
		StderrIsTTY: true,
		Supervised:  true,
	})
	if strings.Contains(s.stderr.String(), sentinelToken) {
		t.Fatalf("supervised: stderr carries the token:\n%s", s.stderr.String())
	}
	s.assertFileSinksClean(t)
	if !strings.Contains(s.stderr.String(), "http://127.0.0.1:8090/") || !strings.Contains(s.stderr.String(), supervisorTokenEnv) {
		t.Fatalf("supervised: the token-free URL and its token source should be announced:\n%s", s.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(logsDir, apiTokenFileName)); !os.IsNotExist(err) {
		t.Fatalf("a supervisor token must never be written to api-token, stat err=%v", err)
	}
}
