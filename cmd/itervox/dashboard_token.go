package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/logging"
)

// apiTokenFileName is the file, under the logs dir, that holds an
// AUTO-GENERATED API token (mode 0600) so a headless operator — systemd,
// container, CI, `itervox 2>file` — can discover it without the token ever
// being written to stderr or a log sink (CORE-012). Pinned tokens
// (ITERVOX_API_TOKEN set by the operator) are never written: the operator
// already has them, and a stale file from an earlier generated run is
// removed so the path never names an invalid token.
const apiTokenFileName = "api-token"

// printTokenEnv forces the tokenised dashboard URL onto stderr even when
// stderr is not a terminal. --no-print-token still wins.
const printTokenEnv = "ITERVOX_PRINT_TOKEN"

// dashboardTokenAnnounce is everything announceDashboardToken needs. It is
// a plain value so the decision is testable without run()'s wiring.
type dashboardTokenAnnounce struct {
	Addr        string // actual bind address, host:port
	Token       string // the active bearer token (pinned or generated)
	Generated   bool   // true when this process auto-generated Token (any generation)
	LogsDir     string // resolved --logs-dir; api-token lives here
	NoPrintFlag bool   // --no-print-token
	PrintEnv    string // raw ITERVOX_PRINT_TOKEN value
	StderrIsTTY bool   // term.IsTerminal(os.Stderr.Fd())
}

// shouldPrintDashboardToken decides whether the tokenised URL may go to
// stderr. Precedence: --no-print-token always suppresses; otherwise a
// parseable ITERVOX_PRINT_TOKEN decides (1/true forces the print even when
// stderr is piped, 0/false suppresses it even on a terminal); otherwise the
// URL is printed only when stderr is a terminal. The gate is stderr — not
// stdin — because stderr is the descriptor the line is written to.
func shouldPrintDashboardToken(noPrintFlag bool, printEnv string, stderrIsTTY bool) bool {
	if noPrintFlag {
		return false
	}
	if v, err := strconv.ParseBool(printEnv); err == nil {
		return v
	}
	return stderrIsTTY
}

// tokenFingerprint is a short, non-reversible identifier for a token so an
// operator can tell WHICH token is live (e.g. compare with the api-token
// file or their pinned value) without the line carrying a credential. It
// cannot authenticate anyone.
func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

// writeAPITokenFile atomically writes token to <logsDir>/api-token with
// mode 0600 and returns the path.
func writeAPITokenFile(logsDir, token string) (string, error) {
	path := filepath.Join(logsDir, apiTokenFileName)
	if err := atomicfs.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return path, fmt.Errorf("itervox: write api token file: %w", err)
	}
	return path, nil
}

// removeAPITokenFile deletes a stale api-token file; absence is not an error.
func removeAPITokenFile(logsDir string) error {
	err := os.Remove(filepath.Join(logsDir, apiTokenFileName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// announceDashboardToken tells the operator how to reach the dashboard.
//
//   - stderrOnly is main()'s unwrapped stderr logger. It is the ONLY sink the
//     tokenised URL is ever written to, and only when
//     shouldPrintDashboardToken allows it.
//   - logger is the redacting default fanout (stderr + rotating file). It
//     only ever receives the token-free URL, the fingerprint and the token
//     file path, so it stays safe when a later change (CORE-059) routes
//     headless stderr through a JSON handler.
//
// An auto-generated token is always written to <logsDir>/api-token (0600),
// whether or not the URL is printed, so discovery is the same in every mode.
func announceDashboardToken(stderrOnly, logger *slog.Logger, a dashboardTokenAnnounce) {
	if a.Token == "" {
		return
	}
	tokenFile := ""
	if a.Generated {
		path, err := writeAPITokenFile(a.LogsDir, a.Token)
		if err != nil {
			logger.Warn("server: could not write the generated API token file — pin ITERVOX_API_TOKEN, or set "+printTokenEnv+"=1 to print it on stderr",
				"path", path, "error", err)
		} else {
			tokenFile = path
		}
	} else if err := removeAPITokenFile(a.LogsDir); err != nil {
		logger.Warn("server: could not remove stale API token file",
			"path", filepath.Join(a.LogsDir, apiTokenFileName), "error", err)
	}

	if shouldPrintDashboardToken(a.NoPrintFlag, a.PrintEnv, a.StderrIsTTY) {
		// Token must NEVER hit the rotating log file: stderrOnly bypasses
		// the file sink and the redacting wrapper by design.
		stderrOnly.Info("dashboard URL (carries token — copy/paste once)",
			"url", fmt.Sprintf("http://%s/?token=%s", a.Addr, a.Token))
		return
	}
	attrs := []any{
		"url", fmt.Sprintf("http://%s/", a.Addr),
		"token_fingerprint", tokenFingerprint(a.Token),
	}
	switch {
	case tokenFile != "":
		attrs = append(attrs, "token_file", tokenFile)
	case a.Generated:
		// Write failed; the Warn above already told the operator what to do.
	default:
		attrs = append(attrs, "token_source", "ITERVOX_API_TOKEN")
	}
	attrs = append(attrs, "hint", "stderr is not a terminal (or --no-print-token/"+printTokenEnv+"=0 is set), so the token is withheld; set "+printTokenEnv+"=1 to print it")
	logger.Info("dashboard URL (token withheld from logs)", attrs...)
}

// generatedAPIToken is the token THIS process auto-generated, or "" when it
// never generated one. It outlives run() generations on purpose: the
// generated value is installed in the process environment and keeps
// authenticating across every config reload, so each generation must still
// recognise it as generated and keep <logs-dir>/api-token in place (M0-close
// G4). A pinned ITERVOX_API_TOKEN never matches it, so a stale file from an
// earlier generated-token boot is still removed.
var (
	generatedAPITokenMu sync.Mutex
	generatedAPIToken   string
)

// ensureAPIToken installs an auto-generated ITERVOX_API_TOKEN when none is
// set and the operator has not opted out (shouldGenerateToken). It reports
// whether the ACTIVE token is one this process generated (true on the
// generating run() and on every later reload) and whether it was generated
// by this call.
func ensureAPIToken(allowUnauthenticated bool) (generated, freshly bool, err error) {
	generatedAPITokenMu.Lock()
	defer generatedAPITokenMu.Unlock()
	if shouldGenerateToken(allowUnauthenticated, os.Getenv("ITERVOX_API_TOKEN")) {
		tok, err := generateAPIToken()
		if err != nil {
			return false, false, fmt.Errorf("server: auto-generating API token: %w", err)
		}
		if err := os.Setenv("ITERVOX_API_TOKEN", tok); err != nil {
			return false, false, fmt.Errorf("server: setting ITERVOX_API_TOKEN: %w", err)
		}
		// Register the freshly generated token for exact-value log
		// redaction — see logging.RegisterSecret's doc comment.
		logging.RegisterSecret(tok)
		generatedAPIToken = tok
		return true, true, nil
	}
	active := os.Getenv("ITERVOX_API_TOKEN")
	return active != "" && active == generatedAPIToken, false, nil
}
