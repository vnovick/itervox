package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// quickstartDetection is what `itervox quickstart` found in the repository
// and environment (#74), with the reason for each choice so the operator can
// see why.
type quickstartDetection struct {
	Tracker       string // "github" | "linear" | "local"
	TrackerReason string
	Runner        string // "claude" | "codex"
	RunnerReason  string
}

// detectQuickstart chooses the tracker and agent CLI for a new workflow.
//
// Tracker: --tracker wins; otherwise a real LINEAR_API_KEY in the
// environment means Linear (a key is a deliberate signal, and many GitHub
// repositories track work in Linear); otherwise an origin remote on
// github.com means GitHub Issues when it would work now (GITHUB_TOKEN set,
// or `gh auth status` exits 0); otherwise the local file tracker (#85),
// which needs no account, so the first run never waits on a credential.
// Runner: --runner wins; otherwise claude, then codex, whichever is on PATH
// first. Credentials are checked for presence only: no value is read into
// the output.
func detectQuickstart(dir, trackerFlag, runnerFlag string) (quickstartDetection, error) {
	var det quickstartDetection
	remote := scanRepo(dir).RemoteURL
	shown := displayRemote(remote)
	switch {
	case trackerFlag != "":
		det.Tracker, det.TrackerReason = trackerFlag, "--tracker"
	case isRealSecret(os.Getenv("LINEAR_API_KEY")):
		det.Tracker, det.TrackerReason = "linear", "LINEAR_API_KEY is set; pass --tracker github to use GitHub Issues"
	case gitRemoteHost(remote) == "github.com":
		if how, ok := quickstartGitHubAuth(); ok {
			det.Tracker, det.TrackerReason = "github", "origin is "+shown+" and "+how
			break
		}
		det.Tracker = "local"
		det.TrackerReason = "origin " + shown + " is on github.com, but gh is not logged in and GITHUB_TOKEN is not set; issues are files in .itervox/issues/ (run `gh auth login` and pass --tracker github to use GitHub Issues)"
	default:
		reason := "no origin remote"
		if remote != "" {
			reason = "origin " + shown + " is not on github.com"
		}
		det.Tracker = "local"
		det.TrackerReason = reason + " and LINEAR_API_KEY is not set; issues are files in .itervox/issues/ (pass --tracker github or --tracker linear to use a service)"
	}

	if runnerFlag != "" {
		if _, err := quickstartLookPath(runnerFlag); err != nil {
			return det, fmt.Errorf("--runner %s: %s is not on PATH", runnerFlag, runnerFlag)
		}
		det.Runner, det.RunnerReason = runnerFlag, "--runner"
		return det, nil
	}
	for _, cli := range []string{"claude", "codex"} {
		if path, err := quickstartLookPath(cli); err == nil {
			det.Runner, det.RunnerReason = cli, path
			return det, nil
		}
	}
	msg := "neither claude nor codex is on PATH; install one (see https://itervox.dev/getting-started/) and re-run"
	var others []string
	for _, cli := range []string{"opencode", "cursor-agent"} {
		if _, err := quickstartLookPath(cli); err == nil {
			others = append(others, cli)
		}
	}
	if len(others) > 0 {
		msg += fmt.Sprintf(" (found %s: quickstart writes a claude or codex workflow; add a profile for it on the Agents page afterwards)", strings.Join(others, " and "))
	}
	return det, errors.New(msg)
}

// quickstartGitHubAuth reports whether GitHub Issues would work without a
// further step, and how: GITHUB_TOKEN is set (presence only), or `gh auth
// status` exits 0 (its output is discarded, never printed).
func quickstartGitHubAuth() (string, bool) {
	if isRealSecret(os.Getenv("GITHUB_TOKEN")) {
		return "GITHUB_TOKEN is set", true
	}
	if _, err := quickstartLookPath("gh"); err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), quickstartGHAuthTimeout)
	defer cancel()
	if quickstartGHAuthStatus(ctx) == nil {
		return "gh is logged in", true
	}
	return "", false
}

// quickstartGHAuthTimeout bounds `gh auth status`.
const quickstartGHAuthTimeout = 5 * time.Second

// quickstartGHAuthStatus runs `gh auth status` and reports only whether it
// exited 0. Tests replace it (TestMain makes it fail), so no test runs gh.
var quickstartGHAuthStatus = func(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// displayRemote is a git remote as quickstart prints it: a URL's user
// information (https://user:token@host/...) is removed, so a credential
// embedded in origin is never printed.
func displayRemote(remote string) string {
	if !strings.Contains(remote, "://") {
		return remote
	}
	u, err := url.Parse(remote)
	if err != nil {
		return "(a remote URL that does not parse)"
	}
	u.User = nil
	return u.String()
}

// gitRemoteHost returns the lower-cased host of a git remote URL in either
// scp form (git@github.com:owner/repo.git) or URL form
// (https://github.com/owner/repo, ssh://git@github.com/owner/repo).
func gitRemoteHost(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	if !strings.Contains(remote, "://") {
		// scp-like: [user@]host:path
		hostPart, _, ok := strings.Cut(remote, ":")
		if !ok {
			return ""
		}
		if _, h, found := strings.Cut(hostPart, "@"); found {
			hostPart = h
		}
		return strings.ToLower(hostPart)
	}
	u, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
