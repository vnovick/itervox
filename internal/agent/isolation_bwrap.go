package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// bubblewrap confines an agent with bwrap on Linux (#258): a read-only bind
// of /, /dev bound read-write (so /dev/null and terminals work), the
// writable paths bound read-write, the readable re-exposures bound
// read-only, and an empty read-only tmpfs (directories) or /dev/null (files)
// over each denied path. No PID, network or user-visible namespace changes
// beyond the mount namespace: the agent keeps the daemon's process group,
// so the group kill on cancel still reaches it, and its network.
type bubblewrap struct {
	// path is the bwrap binary; empty means look it up on PATH.
	path string
}

// newBubblewrap returns the bubblewrap mechanism.
func newBubblewrap() IsolationMechanism { return bubblewrap{} }

func (bubblewrap) Name() string { return "bubblewrap" }

func (b bubblewrap) binary() (string, error) {
	if b.path != "" {
		return b.path, nil
	}
	return exec.LookPath("bwrap")
}

// Available runs a trivial command under the same base arguments, because
// an installed bwrap still fails where user namespaces are disabled.
func (b bubblewrap) Available() error {
	bin, err := b.binary()
	if err != nil {
		return errors.New("bwrap is not installed")
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		return fmt.Errorf("cannot find true for the probe: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := append(bubblewrapArgs(IsolationPolicy{}), "--", truePath)
	if out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("bwrap cannot create a sandbox here (%v): %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b bubblewrap) Wrap(argv []string, p IsolationPolicy) ([]string, error) {
	bin, err := b.binary()
	if err != nil {
		return nil, fmt.Errorf("%w: bwrap is not installed", ErrIsolationUnavailable)
	}
	out := append([]string{bin}, bubblewrapArgs(p)...)
	out = append(out, "--")
	return append(out, argv...), nil
}

// bubblewrapArgs returns the bwrap options enforcing p, without the binary
// and the command. Rules are applied shallowest first so the deeper one
// wins (see IsolationPolicy.orderedRules); a denied directory's tmpfs is
// made read-only only after every bind inside it, because bwrap creates
// those mount points in it.
func bubblewrapArgs(p IsolationPolicy) []string {
	args := []string{"--die-with-parent", "--ro-bind", "/", "/", "--dev-bind", "/dev", "/dev"}
	var deniedDirs []string
	for _, r := range p.orderedRules() {
		switch r.kind {
		case ruleWrite:
			args = append(args, "--bind", r.path, r.path)
		case ruleRead:
			args = append(args, "--ro-bind", r.path, r.path)
		case ruleDeny:
			if r.dir {
				args = append(args, "--tmpfs", r.path)
				deniedDirs = append(deniedDirs, r.path)
			} else {
				args = append(args, "--ro-bind", "/dev/null", r.path)
			}
		}
	}
	for _, d := range deniedDirs {
		args = append(args, "--remount-ro", d)
	}
	return args
}
