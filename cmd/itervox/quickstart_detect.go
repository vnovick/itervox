package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// quickstartDetection is what `itervox quickstart` found in the repository
// and environment (#74), with the reason for each choice so the operator can
// see why.
type quickstartDetection struct {
	Tracker       string // "github" | "linear"
	TrackerReason string
	Runner        string // "claude" | "codex"
	RunnerReason  string
}

// detectQuickstart chooses the tracker and agent CLI for a new workflow.
//
// Tracker: --tracker wins; otherwise a real LINEAR_API_KEY in the
// environment means Linear (a key is a deliberate signal, and many GitHub
// repositories track work in Linear); otherwise an origin remote on
// github.com means GitHub Issues. Runner: --runner wins; otherwise claude,
// then codex, whichever is on PATH first.
func detectQuickstart(dir, trackerFlag, runnerFlag string) (quickstartDetection, error) {
	var det quickstartDetection
	remote := scanRepo(dir).RemoteURL
	switch {
	case trackerFlag != "":
		det.Tracker, det.TrackerReason = trackerFlag, "--tracker"
	case isRealSecret(os.Getenv("LINEAR_API_KEY")):
		det.Tracker, det.TrackerReason = "linear", "LINEAR_API_KEY is set; pass --tracker github to use GitHub Issues"
	case gitRemoteHost(remote) == "github.com":
		det.Tracker, det.TrackerReason = "github", "origin is "+remote
	default:
		reason := "no origin remote"
		if remote != "" {
			reason = "origin " + remote + " is not on github.com"
		}
		return det, fmt.Errorf("could not choose a tracker: %s and LINEAR_API_KEY is not set; pass --tracker github or --tracker linear", reason)
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
	return det, errors.New("neither claude nor codex is on PATH; install one (see https://itervox.dev/getting-started/) and re-run")
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
