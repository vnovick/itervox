package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/config"
)

// `itervox quickstart` (#74): one command from a repository to a running
// board. It detects the tracker and agent CLI, writes (or migrates) the
// workflow, gets the tracker credential in place, creates missing GitHub
// labels, runs doctor, then starts the daemon in the background and waits for
// it to report ready.
//
// Re-running is safe: an existing schema-2 workflow is kept as is, an older
// one is migrated only after confirmation, nothing is overwritten without
// asking, and a daemon that is already running is reported instead of
// started twice.

const quickstartDefaultReadyTimeout = 90 * time.Second

type quickstartOptions struct {
	Dir          string
	Workflow     string
	Tracker      string
	Runner       string
	Yes          bool
	NoStart      bool
	ReadyTimeout time.Duration
}

// Seams tests replace: PATH lookup, `gh auth token`, and the daemon command.
var (
	quickstartLookPath = exec.LookPath
	quickstartGHToken  = func(ctx context.Context) (string, error) {
		out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	// quickstartDaemonCommand builds the command that runs the daemon for
	// workflowPath (absolute): this same binary with -workflow.
	quickstartDaemonCommand = func(workflowPath string) (*exec.Cmd, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return exec.Command(exe, "-workflow", workflowPath), nil
	}
)

func runQuickstart(args []string) {
	opts, err := parseQuickstartFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "itervox quickstart: %v\n", err)
		fatalExit(2)
	}
	if code := quickstart(opts, os.Stdin, os.Stdout); code != 0 {
		fatalExit(code)
	}
}

func parseQuickstartFlags(args []string) (quickstartOptions, error) {
	fs := flag.NewFlagSet("quickstart", flag.ContinueOnError)
	opts := quickstartOptions{ReadyTimeout: quickstartDefaultReadyTimeout}
	fs.StringVar(&opts.Dir, "dir", ".", "repository directory")
	fs.StringVar(&opts.Workflow, "workflow", "", "workflow path (default <dir>/WORKFLOW.md)")
	fs.StringVar(&opts.Tracker, "tracker", "", "tracker kind for a new workflow: github or linear (default: detected)")
	fs.StringVar(&opts.Runner, "runner", "", "agent CLI for a new workflow: claude or codex (default: detected)")
	fs.BoolVar(&opts.Yes, "yes", false, "answer yes to every confirmation")
	fs.BoolVar(&opts.Yes, "y", false, "shorthand for --yes")
	fs.BoolVar(&opts.NoStart, "no-start", false, "stop after the checks; do not start the daemon")
	fs.DurationVar(&opts.ReadyTimeout, "ready-timeout", quickstartDefaultReadyTimeout, "how long to wait for the daemon to report ready")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch opts.Tracker {
	case "", "github", "linear":
	default:
		return opts, fmt.Errorf("unknown --tracker %q (github or linear)", opts.Tracker)
	}
	switch opts.Runner {
	case "", "claude", "codex":
	default:
		return opts, fmt.Errorf("unknown --runner %q (claude or codex)", opts.Runner)
	}
	return opts, nil
}

// quickstart runs every step and returns the exit code.
func quickstart(opts quickstartOptions, in io.Reader, out io.Writer) int {
	reader := bufio.NewReader(in)
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	workflowPath := opts.Workflow
	if workflowPath == "" {
		workflowPath = filepath.Join(dir, "WORKFLOW.md")
	}
	if workflowPath, err = filepath.Abs(workflowPath); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	itervoxDir := filepath.Join(filepath.Dir(workflowPath), ".itervox")

	// 1. Workflow: keep, migrate (asked) or create.
	if _, statErr := os.Stat(workflowPath); statErr == nil {
		if code := quickstartExistingWorkflow(workflowPath, opts.Yes, reader, out); code != 0 {
			return code
		}
	} else if !os.IsNotExist(statErr) {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", statErr)
		return 1
	} else {
		det, err := detectQuickstart(dir, opts.Tracker, opts.Runner)
		if err != nil {
			_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(out, "itervox quickstart: tracker %s (%s), agent %s (%s)\n", det.Tracker, det.TrackerReason, det.Runner, det.RunnerReason)
		if err := scaffoldWorkflow(workflowPath, dir, det.Tracker, det.Runner, out); err != nil {
			_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
			return 1
		}
		ensureEnvStub(itervoxDir, det.Tracker)
		if err := finalizeItervoxGitignore(itervoxDir); err != nil {
			_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		}
	}

	// 2. A daemon already running for this workflow is the end state.
	if url, running := quickstartRunningDaemon(workflowPath); running {
		_, _ = fmt.Fprintf(out, "itervox quickstart: the daemon is already running for %s\n", workflowPath)
		quickstartPrintAccess(out, workflowPath, url)
		return 0
	}

	// 3. Tracker credential.
	if code := quickstartCredential(workflowPath, itervoxDir, opts.Yes, reader, out); code != 0 {
		return code
	}

	// 4. Missing GitHub labels (asked) and the doctor checks.
	report, code := runDoctorFix(workflowPath, opts.Yes, reader, out)
	if code != 0 {
		_, _ = fmt.Fprint(out, report)
		_, _ = fmt.Fprintf(out, "\nitervox quickstart: doctor found the problems above; fix them and run `itervox quickstart` again.\n")
		return code
	}
	_, _ = fmt.Fprintf(out, "itervox quickstart: doctor checks passed\n")

	if opts.NoStart {
		_, _ = fmt.Fprintf(out, "itervox quickstart: not starting the daemon (--no-start); run `itervox -workflow %s`\n", workflowPath)
		return 0
	}

	// 5. Start the daemon and wait for it to be ready.
	url, err := quickstartStartDaemon(workflowPath, itervoxDir, opts.ReadyTimeout, out)
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	quickstartPrintAccess(out, workflowPath, url)
	_, _ = fmt.Fprintf(out, "Stop it with: itervox stop -workflow %s\n", workflowPath)
	return 0
}

// quickstartExistingWorkflow keeps a schema-2 workflow and migrates an older
// one after confirmation.
func quickstartExistingWorkflow(workflowPath string, yes bool, in *bufio.Reader, out io.Writer) int {
	version, err := workflowSchemaVersion(workflowPath)
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	if version == config.LatestWorkflowSchemaVersion {
		_, _ = fmt.Fprintf(out, "itervox quickstart: using the existing %s\n", workflowPath)
		return 0
	}
	if !quickstartConfirm(in, out, yes, fmt.Sprintf("%s uses workflow schema %d; migrate it to schema %d with `itervox init --update` (a .bak copy is kept)?", workflowPath, version, config.LatestWorkflowSchemaVersion)) {
		_, _ = fmt.Fprintf(out, "itervox quickstart: left %s unchanged; migrate it with `itervox init --update --workflow %s`, then re-run.\n", workflowPath, workflowPath)
		return 1
	}
	result, err := migrateWorkflowToSchema2(workflowPath, false, time.Now().UTC())
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "itervox quickstart: migrated %s (backup %s)\n", workflowPath, result.BackupPath)
	itervoxDir := filepath.Join(filepath.Dir(workflowPath), ".itervox")
	fm, _ := readQuickstartFrontMatter(workflowPath)
	ensureEnvStub(itervoxDir, fm.Tracker.Kind)
	if err := finalizeItervoxGitignore(itervoxDir); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
	}
	return 0
}

// quickstartFrontMatter is the part of the front matter quickstart reads
// before the workflow is valid (config.Load rejects a workflow whose tracker
// token is not set yet, which is exactly the state quickstart fixes).
type quickstartFrontMatter struct {
	Version int `yaml:"itervox_schema_version"`
	Tracker struct {
		Kind string `yaml:"kind"`
	} `yaml:"tracker"`
}

func readQuickstartFrontMatter(workflowPath string) (quickstartFrontMatter, error) {
	var doc quickstartFrontMatter
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		return doc, err
	}
	front, _, ok := splitWorkflowFrontMatter(string(raw))
	if !ok {
		return doc, fmt.Errorf("%s has no YAML front matter", workflowPath)
	}
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return doc, fmt.Errorf("parse %s: %w", workflowPath, err)
	}
	return doc, nil
}

// workflowSchemaVersion reads itervox_schema_version (0 when absent).
func workflowSchemaVersion(workflowPath string) (int, error) {
	doc, err := readQuickstartFrontMatter(workflowPath)
	return doc.Version, err
}

// quickstartCredential makes sure the tracker credential the workflow needs
// is set. For GitHub it offers the gh CLI's token, saved to the gitignored
// .itervox/.env after confirmation.
func quickstartCredential(workflowPath, itervoxDir string, yes bool, in *bufio.Reader, out io.Writer) int {
	envPath := filepath.Join(itervoxDir, ".env")
	doc, err := readQuickstartFrontMatter(workflowPath)
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	if trackerAPIKeyResolved(workflowPath) {
		return 0
	}
	switch doc.Tracker.Kind {
	case "github":
		if isRealSecret(os.Getenv("GITHUB_TOKEN")) {
			return 0 // set, but the workflow reads another variable: doctor reports it
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		token, err := quickstartGHToken(ctx)
		cancel()
		if err != nil || !isRealSecret(token) {
			_, _ = fmt.Fprintf(out, "itervox quickstart: no GitHub token. Run `gh auth login`, or set GITHUB_TOKEN in %s, then re-run.\n", envPath)
			return 1
		}
		if !quickstartConfirm(in, out, yes, fmt.Sprintf("Use your gh CLI token for the tracker and save it to %s (gitignored)?", envPath)) {
			_, _ = fmt.Fprintf(out, "itervox quickstart: set GITHUB_TOKEN in %s, then re-run.\n", envPath)
			return 1
		}
		if err := setEnvFileVar(envPath, "GITHUB_TOKEN", token); err != nil {
			_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
			return 1
		}
		_ = os.Setenv("GITHUB_TOKEN", token)
		_, _ = fmt.Fprintf(out, "itervox quickstart: saved GITHUB_TOKEN to %s\n", envPath)
	case "linear":
		if !isRealSecret(os.Getenv("LINEAR_API_KEY")) {
			_, _ = fmt.Fprintf(out, "itervox quickstart: set LINEAR_API_KEY in %s (create a key under Linear → Settings → API), then re-run.\n", envPath)
			return 1
		}
	}
	return 0
}

// trackerAPIKeyResolved reports whether the workflow loads with a real
// tracker.api_key (a literal or a $VAR that is set).
func trackerAPIKeyResolved(workflowPath string) bool {
	cfg, err := config.Load(workflowPath)
	return err == nil && isRealSecret(cfg.Tracker.APIKey)
}

// isRealSecret rejects empty values and the ensureEnvStub placeholders.
func isRealSecret(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && !strings.Contains(v, "xxxxxxxx")
}

// setEnvFileVar sets key=value in a dotenv file: it replaces an existing
// placeholder or empty line for key, appends otherwise, and refuses to
// replace a real value. The file is written 0600.
func setEnvFileVar(path, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(raw) == 0 {
		lines = nil
	}
	replaced := false
	for i, line := range lines {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(strings.TrimPrefix(k, "export ")) != key {
			continue
		}
		if isRealSecret(strings.Trim(v, `"' `)) {
			return fmt.Errorf("%s already sets %s; not overwriting it", path, key)
		}
		lines[i] = key + "=" + value
		replaced = true
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// quickstartConfirm asks a [y/N] question; anything but y/yes (including
// EOF) is no. yes answers it without reading.
func quickstartConfirm(in *bufio.Reader, out io.Writer, yes bool, question string) bool {
	if yes {
		_, _ = fmt.Fprintf(out, "%s [y/N] y (--yes)\n", question)
		return true
	}
	_, _ = fmt.Fprintf(out, "%s [y/N] ", question)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	_, _ = fmt.Fprintln(out)
	return false
}

// quickstartRunningDaemon reports whether a live daemon owns this workflow,
// with the dashboard URL it published.
func quickstartRunningDaemon(workflowPath string) (string, bool) {
	pid, _, _, err := readPIDFile(workflowPath)
	if err != nil || pid <= 0 || !processAlive(pid) {
		return "", false
	}
	raw, _ := os.ReadFile(dashboardURLFilePath(workflowPath))
	return strings.TrimSpace(string(raw)), true
}

// quickstartStartDaemon starts the daemon in its own session with output in
// .itervox/logs/quickstart-daemon.log, then waits until it has published its
// dashboard URL and GET /api/v1/ready returns 200.
func quickstartStartDaemon(workflowPath, itervoxDir string, timeout time.Duration, out io.Writer) (string, error) {
	logPath := filepath.Join(itervoxDir, "logs", "quickstart-daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer func() { _ = logFile.Close() }()

	cmd, err := quickstartDaemonCommand(workflowPath)
	if err != nil {
		return "", err
	}
	cmd.Dir = filepath.Dir(workflowPath)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own session: the daemon outlives this command and the terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// A stale URL file from an earlier daemon must not be mistaken for this one.
	_ = os.Remove(dashboardURLFilePath(workflowPath))
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start daemon: %w", err)
	}
	_, _ = fmt.Fprintf(out, "itervox quickstart: started the daemon (pid %d, log %s); waiting for it to be ready...\n", cmd.Process.Pid, logPath)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return "", fmt.Errorf("the daemon exited before it was ready (%v); see %s", err, logPath)
		default:
		}
		if raw, err := os.ReadFile(dashboardURLFilePath(workflowPath)); err == nil {
			url := strings.TrimSpace(string(raw))
			if url != "" && quickstartReady(client, url) {
				return url, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("the daemon did not report ready within %s; it is still running (pid %d), see %s", timeout, cmd.Process.Pid, logPath)
}

func quickstartReady(client *http.Client, dashboardURL string) bool {
	resp, err := client.Get(strings.TrimRight(dashboardURL, "/") + "/api/v1/ready")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// quickstartPrintAccess prints the dashboard URL and where its token is.
func quickstartPrintAccess(out io.Writer, workflowPath, url string) {
	if url == "" {
		url = "(not published yet — see " + dashboardURLFilePath(workflowPath) + ")"
	}
	_, _ = fmt.Fprintf(out, "\nDashboard: %s\n", url)
	cfg, err := config.Load(workflowPath)
	switch {
	case err == nil && cfg.Server.AllowUnauthenticatedLAN:
		_, _ = fmt.Fprintf(out, "API token: none (server.allow_unauthenticated is true)\n")
	case os.Getenv("ITERVOX_API_TOKEN") != "":
		_, _ = fmt.Fprintf(out, "API token: the ITERVOX_API_TOKEN you set (environment or .itervox/.env)\n")
	default:
		_, _ = fmt.Fprintf(out, "API token: %s (a new one is generated on every daemon start)\n",
			filepath.Join(defaultLogsDir(workflowPath), "api-token"))
	}
}
