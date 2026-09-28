package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	charmlog "github.com/charmbracelet/log"

	"github.com/vnovick/itervox/internal/logging"
)

// sentinelToken is a recognisable fake bearer token. It is deliberately NOT
// passed to logging.RegisterSecret: the assertions below must hold because
// the announce path never emits the token to a log sink, not because the
// redacting wrapper happens to scrub it afterwards.
const sentinelToken = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

// tokenSinks mirrors main()'s logger topology: a charmlog stderr handler
// shared by the unwrapped stderrOnly logger and the redacting fanout, plus
// the rotating-file sink in BOTH formats (text and json, CORE-059-ready).
type tokenSinks struct {
	stderr, fileText, fileJSON bytes.Buffer
	stderrOnly, fanout         *slog.Logger
}

func newTokenSinks() *tokenSinks {
	s := &tokenSinks{}
	stderrHandler := charmlog.NewWithOptions(&s.stderr, charmlog.Options{Level: charmlog.InfoLevel})
	s.stderrOnly = slog.New(stderrHandler)
	s.fanout = slog.New(logging.NewRedactingHandler(logging.NewFanoutHandler(
		stderrHandler,
		newFileLogHandler(&s.fileText, slog.LevelInfo, "text"),
		newFileLogHandler(&s.fileJSON, slog.LevelInfo, "json"),
	)))
	return s
}

func (s *tokenSinks) assertFileSinksClean(t *testing.T) {
	t.Helper()
	if strings.Contains(s.fileText.String(), sentinelToken) {
		t.Errorf("text file sink contains the token:\n%s", s.fileText.String())
	}
	if strings.Contains(s.fileJSON.String(), sentinelToken) {
		t.Errorf("json file sink contains the token:\n%s", s.fileJSON.String())
	}
}

// TestDashboardURLNotPrintedWithoutTTY pins CORE-012: the tokenised
// dashboard URL reaches stderr only when stderr is a terminal or the
// operator opts in with ITERVOX_PRINT_TOKEN=1; --no-print-token always
// wins; headless auto-generated tokens are discoverable via a 0600 file
// under the logs dir; no log FILE sink ever carries the token.
func TestDashboardURLNotPrintedWithoutTTY(t *testing.T) {
	const addr = "127.0.0.1:8090"
	tokenURL := "?token=" + sentinelToken
	fp := tokenFingerprint(sentinelToken)
	if strings.Contains(sentinelToken, strings.TrimPrefix(fp, "sha256:")) {
		t.Fatalf("fingerprint %q leaks a substring of the token", fp)
	}

	cases := []struct {
		name        string
		generated   bool
		stderrTTY   bool
		printEnv    string
		noPrint     bool
		wantURLShow bool
	}{
		{name: "pinned, stderr piped (headless)", stderrTTY: false},
		{name: "pinned, stderr piped, ITERVOX_PRINT_TOKEN=1", stderrTTY: false, printEnv: "1", wantURLShow: true},
		{name: "pinned, stderr piped, ITERVOX_PRINT_TOKEN=0", stderrTTY: false, printEnv: "0"},
		{name: "pinned, --no-print-token beats ITERVOX_PRINT_TOKEN=1", stderrTTY: false, printEnv: "1", noPrint: true},
		{name: "pinned, interactive terminal", stderrTTY: true, wantURLShow: true},
		{name: "pinned, interactive terminal, --no-print-token", stderrTTY: true, noPrint: true},
		{name: "generated, stderr piped (headless)", generated: true, stderrTTY: false},
		{name: "generated, interactive terminal", generated: true, stderrTTY: true, wantURLShow: true},
		{name: "generated, --no-print-token beats ITERVOX_PRINT_TOKEN=1", generated: true, printEnv: "1", noPrint: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logsDir := t.TempDir()
			tokenFile := filepath.Join(logsDir, apiTokenFileName)
			// A stale file from a previous generated-token run must not
			// survive into a pinned-token run.
			if !tc.generated {
				if err := os.WriteFile(tokenFile, []byte("stale\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s := newTokenSinks()
			announceDashboardToken(s.stderrOnly, s.fanout, dashboardTokenAnnounce{
				Addr:        addr,
				Token:       sentinelToken,
				Generated:   tc.generated,
				LogsDir:     logsDir,
				NoPrintFlag: tc.noPrint,
				PrintEnv:    tc.printEnv,
				StderrIsTTY: tc.stderrTTY,
			})
			stderr := s.stderr.String()
			// Checked first (and non-fatally) so a file-sink leak is
			// reported even when stderr also leaks.
			s.assertFileSinksClean(t)

			if tc.wantURLShow {
				if !strings.Contains(stderr, tokenURL) {
					t.Fatalf("stderr should carry the tokenised URL, got:\n%s", stderr)
				}
			} else {
				if strings.Contains(stderr, sentinelToken) {
					t.Fatalf("stderr must not contain the token, got:\n%s", stderr)
				}
				if !strings.Contains(stderr, fp) {
					t.Fatalf("stderr should name the token fingerprint %q, got:\n%s", fp, stderr)
				}
			}

			if tc.generated {
				got, err := os.ReadFile(tokenFile)
				if err != nil {
					t.Fatalf("generated token file not written: %v", err)
				}
				if strings.TrimSpace(string(got)) != sentinelToken {
					t.Fatalf("token file content = %q, want the generated token", got)
				}
				info, err := os.Stat(tokenFile)
				if err != nil {
					t.Fatal(err)
				}
				if perm := info.Mode().Perm(); perm != 0o600 {
					t.Fatalf("token file perm = %o, want 0600", perm)
				}
				if !tc.wantURLShow && !strings.Contains(stderr, tokenFile) {
					t.Fatalf("headless stderr should name the token file %q, got:\n%s", tokenFile, stderr)
				}
			} else if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
				t.Fatalf("pinned-token run must remove a stale %s (stat err=%v)", apiTokenFileName, err)
			}
		})
	}
}

// TestShouldPrintDashboardTokenPrecedence pins the override order:
// --no-print-token > ITERVOX_PRINT_TOKEN > stderr-is-a-TTY.
func TestShouldPrintDashboardTokenPrecedence(t *testing.T) {
	cases := []struct {
		noPrint bool
		env     string
		tty     bool
		want    bool
	}{
		{false, "", false, false},
		{false, "", true, true},
		{false, "1", false, true},
		{false, "true", false, true},
		{false, "0", true, false},
		{false, "garbage", false, false},
		{false, "garbage", true, true},
		{true, "1", true, false},
		{true, "", true, false},
	}
	for _, tc := range cases {
		if got := shouldPrintDashboardToken(tc.noPrint, tc.env, tc.tty); got != tc.want {
			t.Errorf("shouldPrintDashboardToken(noPrint=%v, env=%q, tty=%v) = %v, want %v", tc.noPrint, tc.env, tc.tty, got, tc.want)
		}
	}
}
