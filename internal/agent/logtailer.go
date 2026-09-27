package agent

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/procgroup"
)

// maxSubLogLines is the upper bound on parsed sublog entries returned per
// fetch (and persisted in memory by callers). Bound exists to keep dashboard
// payloads reasonable for long-running agent sessions: at ~200 bytes per
// entry this caps each issue's tail at ~1 MiB, well below SSE/HTTP body
// thresholds. Excess lines are dropped from the *front* of the slice (oldest
// first) so the most recent activity is always visible.
const maxSubLogLines = 5000

// newMaxScanner returns a bufio.Scanner with a 1 MiB buffer, suitable for
// reading JSONL lines that may exceed the default 64 KiB limit. The 1 MiB
// cap matches the largest reasonable single-line agent emit (long
// concatenated tool inputs / stack traces) without making truncation common.
func newMaxScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	return s
}

// SublogFetcher retrieves parsed session log entries for one issue.
// The dir argument is the per-issue log directory on whichever host holds the files.
// Implementations: LocalSublogFetcher (disk), SSHSublogFetcher (remote tar-over-SSH).
type SublogFetcher interface {
	FetchSubLogs(ctx context.Context, dir string) ([]domain.IssueLogEntry, error)
}

// LocalSublogFetcher reads session logs from the local filesystem.
type LocalSublogFetcher struct{}

func (LocalSublogFetcher) FetchSubLogs(_ context.Context, dir string) ([]domain.IssueLogEntry, error) {
	return parseSessionLogsMulti(dir)
}

// SSHSublogFetcher fetches session logs from a remote host over SSH.
type SSHSublogFetcher struct{ Host string }

func (s SSHSublogFetcher) FetchSubLogs(ctx context.Context, dir string) ([]domain.IssueLogEntry, error) {
	return sshFetchLogs(ctx, s.Host, dir)
}

// sshFetchLogs fetches and parses session logs from a remote host using
// short-lived ssh exec calls. Returns nil when the remote directory is absent.
// Files named "codex-*.jsonl" are parsed with ParseCodexLine; all other
// .jsonl files are parsed with ParseLine (Claude Code stream-json format).
//
// Session IDs are derived from filenames so each log entry is stamped with the
// session that produced it — matching the behaviour of the local ParseSessionLogs
// path and enabling per-run log isolation in the Timeline view.
//
// Error contract (CORE-126, fix round 1 M5). Every FetchSubLogs caller
// treats a non-nil error as a failed fetch and discards the entries, so:
//   - total failure (nothing could be fetched) returns an error, which the
//     server reports as fetch_failed instead of "no logs yet";
//   - partial success returns the entries it has, plus an in-band ERROR entry
//     naming the part that failed, and a nil error.
//
// An ssh transport failure (exit 255: unreachable host, host-key mismatch,
// auth refusal) or a timeout on the Claude fetch is total: the Codex fetch
// would hit the same host and, for an unreachable one, double the
// ConnectTimeout wait, so it is skipped.
func sshFetchLogs(ctx context.Context, host, dir string) ([]domain.IssueLogEntry, error) {
	claudeEntries, claudeErr := sshFetchRemoteJSONL(ctx, host, dir, "-name '*.jsonl' ! -name 'codex-*.jsonl'", ParseLine, "claude")
	if claudeErr != nil && sshTransportFailed(ctx, claudeErr) {
		return nil, claudeErr
	}
	codexEntries, codexErr := sshFetchRemoteJSONL(ctx, host, dir, "-name 'codex-*.jsonl'", ParseCodexLine, "codex")
	if claudeErr != nil && codexErr != nil {
		return nil, errors.Join(claudeErr, codexErr)
	}

	var failures []domain.IssueLogEntry
	for _, err := range []error{claudeErr, codexErr} {
		if err != nil {
			failures = append(failures, sublogFailureEntry("", err.Error()))
		}
	}
	return capSubLogEntries(append(claudeEntries, codexEntries...), failures), nil
}

// sshTransportFailed reports whether err from sshFetchRemoteJSONL means the
// host itself could not be used: ssh's own exit status 255, or the fetch
// context ending.
func sshTransportFailed(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 255
}

// sublogFailureEntry is the in-band timeline entry that reports a part of a
// sublog fetch that failed while the rest succeeded.
func sublogFailureEntry(sessionID, msg string) domain.IssueLogEntry {
	return domain.IssueLogEntry{Level: "ERROR", Event: "error", Message: msg, SessionID: sessionID}
}

// capSubLogEntries bounds a fetch result to maxSubLogLines, dropping the
// OLDEST regular entries first. The in-band failure entries are always kept
// (appended last), so a failure in an early file cannot be trimmed away by a
// large later file (fix round 1, M4).
func capSubLogEntries(entries, failures []domain.IssueLogEntry) []domain.IssueLogEntry {
	if len(entries) == 0 && len(failures) == 0 {
		return nil // "no logs" stays nil, as before
	}
	failures = failures[:min(len(failures), maxSubLogLines)]
	room := maxSubLogLines - len(failures)
	if len(entries) > room {
		entries = entries[len(entries)-room:]
	}
	out := make([]domain.IssueLogEntry, 0, len(entries)+len(failures))
	out = append(out, entries...)
	return append(out, failures...)
}

// isCodexLogFile returns true for filenames matching "codex-*.jsonl"
// (including the legacy "codex-session.jsonl").
func isCodexLogFile(name string) bool {
	return strings.HasPrefix(name, "codex-") && strings.HasSuffix(name, ".jsonl")
}

// maxSSHStderrBytes bounds the ssh diagnostic kept for a failed sublog fetch.
const maxSSHStderrBytes = 4 << 10

// sshFetchRemoteJSONL fetches matching .jsonl session files from dir on host in
// a single SSH connection. The remote produces a tar archive; the Go client
// reads it with archive/tar so session IDs come from tar header filenames, the
// same source as the local readJSONLFile path. Remote requirement: tar must be
// available on the SSH host (standard on Linux/macOS).
//
// The remote script ends in `|| true`, so a missing directory or a failing
// find/tar on the remote side still exits 0 and yields no entries; a non-zero
// exit is therefore ssh's own (transport/auth, status 255) or a kill, and is
// returned as an error carrying ssh's stderr (CORE-126). Entries parsed before
// a failure are returned alongside the error.
func sshFetchRemoteJSONL(ctx context.Context, host, dir, findPredicate string, parseFn func([]byte) (StreamEvent, error), label string) ([]domain.IssueLogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Collect matching files into a variable first so we can skip the tar call
	// entirely when none exist (tar with zero arguments is an error on some platforms).
	script := `files=$(find ` + ShellQuote(dir) + ` -maxdepth 1 ` + findPredicate + ` 2>/dev/null | sort); ` +
		`[ -n "$files" ] && tar -cf - -C ` + ShellQuote(dir) + ` $(basename -a $files) 2>/dev/null || true`

	remoteArg, payload := remoteBashInvocation("-c", script)
	// -T (round 3, m3): never a PTY, even under ssh_config `RequestTTY
	// force` — a terminal would echo the stdin script and translate CRLF,
	// corrupting the tar stream read below.
	sshArgs := append([]string{"-T"}, sshStrictHostOption(host)...)
	sshArgs = append(sshArgs, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, remoteArg)
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	// Same held-open stdin as the agent turns (CORE-155): the wrapper kills
	// the remote find/tar when the fetch is abandoned (30s timeout, ctx
	// cancel). They are short-lived writers that a dead channel would also
	// stop with SIGPIPE, but one transport for every ssh call keeps a single
	// wrapper to verify — and closing stdin early would now kill the tar.
	stdinFeed, err := attachRemoteStdin(cmd, payload)
	if err != nil {
		return nil, fmt.Errorf("logtailer: ssh %s (%s logs): %w", host, label, err)
	}
	// Own process group, killed whole on ctx expiry, with a WaitDelay: a
	// non-*os.File Stderr makes Wait also wait for its copy goroutine, so a
	// descendant still holding the stdout or stderr pipe after ssh is killed
	// (e.g. a ProxyCommand) could otherwise hold the drain below and Wait
	// open indefinitely.
	procgroup.Configure(cmd, 5*time.Second)
	stderr := newTailBuffer(maxSSHStderrBytes)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("logtailer: ssh %s (%s logs): stdout pipe: %w", host, label, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("logtailer: ssh %s (%s logs): start: %w", host, label, err)
	}
	stdinFeed.start()

	entries := parseRemoteJSONLTar(stdout, parseFn, host, label)
	// CORE-135: parseRemoteJSONLTar stops reading at the end-of-archive
	// marker or on a tar error, possibly with most of the stream still
	// queued. os/exec forbids Wait before all pipe reads complete: ssh would
	// block writing into the full pipe and Wait would block on ssh until the
	// 30s context above killed it. Drain the rest first (bounded by the same
	// context — cancellation kills ssh, which closes the pipe).
	_, _ = io.Copy(io.Discard, stdout)

	waitErr := cmd.Wait()
	stdinFeed.join() // Wait closed ssh's stdin pipe; the writer has returned
	if waitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			waitErr = fmt.Errorf("%w (%w)", ctxErr, waitErr)
		}
		err := fmt.Errorf("logtailer: ssh %s (%s logs): %w", host, label, waitErr)
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		slog.Warn("logtailer: ssh sublog fetch failed", "host", host, "parser", label, "error", err)
		return entries, err
	}
	return entries, nil
}

// sessionIDFromFilename derives a log session ID from a .jsonl file name —
// whether a local path (readJSONLFileMultiWith) or a tar entry name
// (parseRemoteJSONLTar's remote fetch path). Both callers must agree on this
// formula since the dashboard correlates local and remote session logs by ID.
func sessionIDFromFilename(name string) string {
	return strings.TrimSuffix(filepath.Base(name), ".jsonl")
}

func parseRemoteJSONLTar(r io.Reader, parseFn func([]byte) (StreamEvent, error), host, label string) []domain.IssueLogEntry {
	var entries []domain.IssueLogEntry
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Warn("logtailer: tar read error, returning partial results", "host", host, "parser", label, "error", err)
			break
		}
		sessionID := sessionIDFromFilename(hdr.Name)
		scanner := newMaxScanner(tr)
		for scanner.Scan() {
			entries = append(entries, streamLineToEntriesWith(scanner.Bytes(), parseFn, sessionID)...)
		}
		if scanner.Err() != nil {
			slog.Warn("logtailer: scanner error in tar entry", "host", host, "parser", label, "file", hdr.Name, "error", scanner.Err())
		}
	}
	return entries
}

// parseSessionLogsMulti reads all *.jsonl files in dir, parses each line, and
// returns all entries from a stream event (e.g., a turn with both text blocks
// and tool calls). This is the full-fidelity version used by the API.
// Files matching "codex-*.jsonl" are parsed with ParseCodexLine; all other
// .jsonl files are parsed with ParseLine (Claude Code stream-json format).
// Returns nil (not an error) when dir does not exist or contains no files.
func parseSessionLogsMulti(dir string) ([]domain.IssueLogEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("logtailer: read dir %s: %w", dir, err)
	}

	var all, failures []domain.IssueLogEntry
	var readErrs []error
	tried := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		tried++
		path := filepath.Join(dir, e.Name())
		parseFn := ParseLine
		if isCodexLogFile(e.Name()) {
			parseFn = ParseCodexLine
		}
		lines, err := readJSONLFileMultiWith(path, parseFn)
		all = append(all, lines...)
		if err != nil {
			// CORE-127: keep what was parsed before the failure (e.g. a line
			// over the 1 MiB scanner cap) and surface the failure in the
			// timeline itself, rather than dropping the whole file silently.
			slog.Warn("logtailer: session log read error, returning partial results",
				"file", path, "entries_kept", len(lines), "error", err)
			readErrs = append(readErrs, fmt.Errorf("logtailer: read %s: %w", e.Name(), err))
			failures = append(failures, sublogFailureEntry(strings.TrimSuffix(e.Name(), ".jsonl"),
				fmt.Sprintf("session log %s could not be read in full (%d entries kept): %v", e.Name(), len(lines), err)))
		}
	}
	// Fix round 1, M4: when EVERY file failed, the fetch failed — return an
	// error so the server reaches fetch_failed. A partial success returns
	// its entries plus the in-band failure entries and a nil error, because
	// callers treat an error as a whole-fetch failure and would discard the
	// files that did load.
	if tried > 0 && len(readErrs) == tried {
		return nil, errors.Join(readErrs...)
	}
	return capSubLogEntries(all, failures), nil
}

// readJSONLFileMultiWith reads a .jsonl file using the provided parse function and
// converts each line to zero or more IssueLogEntry.
// The session ID is derived from the filename (without .jsonl extension).
func readJSONLFileMultiWith(path string, parseFn func([]byte) (StreamEvent, error)) ([]domain.IssueLogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	sessionID := sessionIDFromFilename(path)

	var entries []domain.IssueLogEntry
	scanner := newMaxScanner(f)
	for scanner.Scan() {
		entries = append(entries, streamLineToEntriesWith(scanner.Bytes(), parseFn, sessionID)...)
	}
	return entries, scanner.Err()
}

// streamLineToEntriesWith converts one JSONL line to zero or more IssueLogEntry using parseFn.
// sessionID is stamped on every returned entry.
// Supports both Claude Code (ParseLine) and Codex (ParseCodexLine) formats since both
// normalize to the same StreamEvent type.
func streamLineToEntriesWith(line []byte, parseFn func([]byte) (StreamEvent, error), sessionID string) []domain.IssueLogEntry {
	ev, err := parseFn(line)
	if err != nil {
		return nil
	}

	switch ev.Type {
	case EventAssistant:
		if ev.InProgress {
			return nil
		}
		var entries []domain.IssueLogEntry
		for _, text := range ev.TextBlocks {
			if strings.TrimSpace(text) == "" {
				continue
			}
			entries = append(entries, domain.IssueLogEntry{
				Level:     "INFO",
				Event:     "text",
				Message:   text,
				SessionID: sessionID,
			})
		}
		for _, tc := range ev.ToolCalls {
			name := tc.Name
			desc := toolDescription(name, tc.Input)
			msg := name
			if desc != "" {
				msg = name + " — " + desc
			}
			entries = append(entries, domain.IssueLogEntry{
				Level:     "INFO",
				Event:     "action",
				Message:   msg,
				Tool:      name,
				SessionID: sessionID,
			})
		}
		return entries

	case EventResult:
		if ev.IsError {
			return []domain.IssueLogEntry{{
				Level:     "ERROR",
				Event:     "error",
				Message:   ev.ResultText,
				SessionID: sessionID,
			}}
		}
		return nil

	default:
		return nil
	}
}
