package agent

import (
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
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
func sshFetchLogs(ctx context.Context, host, dir string) ([]domain.IssueLogEntry, error) {
	claudeEntries := sshFetchRemoteJSONL(ctx, host, dir, "-name '*.jsonl' ! -name 'codex-*.jsonl'", ParseLine, "claude")
	codexEntries := sshFetchRemoteJSONL(ctx, host, dir, "-name 'codex-*.jsonl'", ParseCodexLine, "codex")

	all := append(claudeEntries, codexEntries...)
	if len(all) > maxSubLogLines {
		all = all[len(all)-maxSubLogLines:]
	}
	return all, nil
}

// isCodexLogFile returns true for filenames matching "codex-*.jsonl"
// (including the legacy "codex-session.jsonl").
func isCodexLogFile(name string) bool {
	return strings.HasPrefix(name, "codex-") && strings.HasSuffix(name, ".jsonl")
}

// sshFetchRemoteJSONL fetches matching .jsonl session files from dir on host in
// a single SSH connection. The remote produces a tar archive; the Go client
// reads it with archive/tar so session IDs come from tar header filenames, the
// same source as the local readJSONLFile path. Remote requirement: tar must be
// available on the SSH host (standard on Linux/macOS).
func sshFetchRemoteJSONL(ctx context.Context, host, dir, findPredicate string, parseFn func([]byte) (StreamEvent, error), label string) []domain.IssueLogEntry {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Collect matching files into a variable first so we can skip the tar call
	// entirely when none exist (tar with zero arguments is an error on some platforms).
	script := `files=$(find ` + shellQuote(dir) + ` -maxdepth 1 ` + findPredicate + ` 2>/dev/null | sort); ` +
		`[ -n "$files" ] && tar -cf - -C ` + shellQuote(dir) + ` $(basename -a $files) 2>/dev/null || true`

	sshArgs := append(sshStrictHostOption(host), "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, "bash", "-c", script)
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Warn("logtailer: ssh stdout pipe failed", "host", host, "parser", label, "error", err)
		return nil
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("logtailer: ssh start failed", "host", host, "parser", label, "error", err)
		return nil
	}
	defer func() { _ = cmd.Wait() }()

	return parseRemoteJSONLTar(stdout, parseFn, host, label)
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
		sessionID := strings.TrimSuffix(filepath.Base(hdr.Name), ".jsonl")
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

// streamLineToEntry converts one stream-json line to an IssueLogEntry.
// sessionID is stamped on every returned entry.
// Returns (entry, false) when the line should be skipped.
func streamLineToEntry(line []byte, sessionID string) (domain.IssueLogEntry, bool) {
	entries := streamLineToEntriesWith(line, ParseLine, sessionID)
	if len(entries) == 0 {
		return domain.IssueLogEntry{}, false
	}
	return entries[0], true
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

	var all []domain.IssueLogEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		parseFn := ParseLine
		if isCodexLogFile(e.Name()) {
			parseFn = ParseCodexLine
		}
		lines, err := readJSONLFileMultiWith(path, parseFn)
		if err != nil {
			continue
		}
		all = append(all, lines...)
	}
	if len(all) > maxSubLogLines {
		all = all[len(all)-maxSubLogLines:]
	}
	return all, nil
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

	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")

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
