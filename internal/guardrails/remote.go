package guardrails

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/agent"
)

// Guardrails on SSH worker hosts (0.3.4). A run on an SSH worker gets the
// same runtime directory as a local run — policy, shims, hooks — built ON
// the worker by the itervox binary itself (the `guardrail-remote install`
// entrypoint calls Prepare there, so the repository's own hooks directory
// is resolved by the worker's git, exactly as locally). The binary is this
// daemon's own executable, uploaded once per content hash to
// ~/.cache/itervox/guardrails/<hash>/itervox on the worker and only when the
// worker's `uname -s`/`uname -m` match the platform it was built for: the
// shims and hook then run the very decision code the daemon was built with
// (one protocol, no version skew). Another platform is refused (InstallRemote
// returns ErrRemotePlatform) and the worker decides what happens: the run
// does not start unless guardrails.allow_unguarded_ssh.
//
// Callback: each turn's ssh reverse-forwards a fresh Unix socket in the
// runtime directory to the daemon's API (agent.RemoteEnv), and the shims
// reach the token-gated guardrail/* routes through it. The token and the
// socket path reach the shims in the agent's environment (TokenEnv,
// SocketEnv), exported by the remote script, which travels on ssh's stdin —
// never in a command line and never in the remote policy file. Without a
// callback (no listener, or the worker's sshd refuses the forward) the
// guards still refuse locally on the worker and an approval command is
// refused (no human can be asked), exactly like a local run without a
// daemon endpoint.

// remoteEntrypoint is the worker-side entrypoint of InstallRemote and the
// remote runtime's methods.
const remoteEntrypoint = "guardrail-remote"

// TokenEnv and SocketEnv carry a remote run's guardrail token and callback
// socket to its shims and hook.
const (
	TokenEnv  = "ITERVOX_GUARDRAIL_TOKEN"
	SocketEnv = "ITERVOX_GUARDRAIL_SOCKET"
)

// remoteProtocol is the install request/result format. The binary on both
// ends is the same build, so a mismatch means a corrupted cache entry.
const remoteProtocol = 1

// remoteCacheDir is where the worker keeps uploaded binaries, relative to
// the remote user's home (ssh starts there).
const remoteCacheDir = ".cache/itervox/guardrails"

// ErrRemotePlatform: the worker is not the platform this binary was built
// for, so its shims cannot run there.
var ErrRemotePlatform = errors.New("guardrails: the SSH worker's platform does not match this itervox binary")

// remoteInstallRequest is what `guardrail-remote install` reads on stdin.
type remoteInstallRequest struct {
	Protocol            int           `json:"protocol"`
	WorkDir             string        `json:"work_dir"`
	Identifier          string        `json:"identifier"`
	IssueBranch         string        `json:"issue_branch"`
	ProtectedBranches   []string      `json:"protected_branches"`
	AllowForcePush      bool          `json:"allow_force_push"`
	ApprovalCommands    []string      `json:"approval_commands"`
	EnvApprovalCommands []EnvApproval `json:"env_approval_commands,omitempty"`
	ApprovalsEnabled    bool          `json:"approvals_enabled"`
	PreApproved         []string      `json:"pre_approved,omitempty"`
	Lanes               Lanes         `json:"lanes"`
	BaseRefs            []string      `json:"base_refs,omitempty"`
}

// remoteInstallResult is what it prints.
type remoteInstallResult struct {
	Protocol            int    `json:"protocol"`
	Dir                 string `json:"dir"`
	BinDir              string `json:"bin_dir"`
	PolicyPath          string `json:"policy_path"`
	GitConfigParameters string `json:"git_config_parameters"`
	ExeSHA256           string `json:"exe_sha256"`
}

// runRemote is the worker side: `guardrail-remote <op> [args…]`.
//
//	install                 request on stdin → result on stdout
//	probe <socket> <id>     GET a guardrail route through the callback (token from TokenEnv)
//	set-branch <policy> <b> Runtime.SetIssueBranch
//	refused <policy>        Runtime.RefusedApprovals as JSON
//	remove <dir>            Runtime.Remove (only an itervox-guardrails-* runtime directory)
func runRemote(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		_, _ = fmt.Fprintln(stderr, "guardrails: usage: itervox guardrail-remote install|probe|set-branch|refused|remove …")
		return 2
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	switch op, rest := args[1], args[2:]; {
	case op == "install":
		res, err := remoteInstall(stdin)
		if err != nil {
			return fail(err)
		}
		_ = json.NewEncoder(stdout).Encode(res)
		return 0
	case op == "probe" && len(rest) == 2:
		_ = os.Setenv(SocketEnv, rest[0])
		c := newClient(Policy{Identifier: rest[1], Remote: true})
		if !c.enabled() {
			return fail(errors.New("guardrails: probe: no socket or token"))
		}
		var st ApprovalStatus
		if err := c.do("GET", c.endpoint("approvals", "probe"), nil, &st); err != nil {
			return fail(fmt.Errorf("guardrails: probe: %w", err))
		}
		return 0
	case op == "set-branch" && len(rest) == 2:
		p, err := LoadPolicy(rest[0])
		if err != nil {
			return fail(err)
		}
		if err := (&Runtime{policy: p}).SetIssueBranch(rest[1]); err != nil {
			return fail(err)
		}
		return 0
	case op == "refused" && len(rest) == 1:
		p, err := LoadPolicy(rest[0])
		if err != nil {
			return fail(err)
		}
		_ = json.NewEncoder(stdout).Encode((&Runtime{policy: p}).RefusedApprovals())
		return 0
	case op == "remove" && len(rest) == 1:
		dir := filepath.Clean(rest[0])
		if !strings.HasPrefix(filepath.Base(dir), "itervox-guardrails-") {
			return fail(fmt.Errorf("guardrails: remove: %s is not a guardrails runtime directory", dir))
		}
		if _, err := os.Stat(filepath.Join(dir, policyFileName)); err != nil {
			if os.IsNotExist(err) {
				return 0
			}
			return fail(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fail(err)
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "guardrails: unknown guardrail-remote operation %q\n", op)
		return 2
	}
}

// remoteInstall prepares the runtime directory on the worker.
func remoteInstall(stdin io.Reader) (remoteInstallResult, error) {
	var req remoteInstallRequest
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&req); err != nil {
		return remoteInstallResult{}, fmt.Errorf("guardrails: install: read request: %w", err)
	}
	if req.Protocol != remoteProtocol {
		return remoteInstallResult{}, fmt.Errorf("guardrails: install: protocol %d, this binary speaks %d", req.Protocol, remoteProtocol)
	}
	exe, err := os.Executable()
	if err != nil {
		return remoteInstallResult{}, fmt.Errorf("guardrails: install: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	sum, _, err := fileDigest(exe)
	if err != nil {
		return remoteInstallResult{}, fmt.Errorf("guardrails: install: %w", err)
	}
	rt, err := Prepare(context.Background(), Options{
		ParentDir:           remoteParentDir(),
		Executable:          exe,
		WorkDir:             req.WorkDir,
		Identifier:          req.Identifier,
		IssueBranch:         req.IssueBranch,
		ProtectedBranches:   req.ProtectedBranches,
		AllowForcePush:      req.AllowForcePush,
		ApprovalCommands:    req.ApprovalCommands,
		EnvApprovalCommands: req.EnvApprovalCommands,
		ApprovalsEnabled:    req.ApprovalsEnabled,
		PreApproved:         req.PreApproved,
		Lanes:               req.Lanes,
		BaseRefs:            req.BaseRefs,
		remote:              true,
	})
	if err != nil {
		return remoteInstallResult{}, err
	}
	return remoteInstallResult{
		Protocol:            remoteProtocol,
		Dir:                 rt.Dir(),
		BinDir:              rt.BinDir(),
		PolicyPath:          rt.PolicyPath(),
		GitConfigParameters: rt.gitConfigParameters(),
		ExeSHA256:           sum,
	}, nil
}

// remoteParentDir is where the worker's runtime directory goes: the
// callback socket lives in it, and a Unix socket path is limited to about
// 100 bytes, so a long TMPDIR (macOS: /var/folders/…/T/) gives way to /tmp.
func remoteParentDir() string {
	dir := os.TempDir()
	if len(dir) > 40 {
		if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
			return "/tmp"
		}
	}
	return dir
}

// RemoteOptions configures InstallRemote.
type RemoteOptions struct {
	// Host is the SSH worker (agent.ssh_hosts entry).
	Host string
	// Options are the run's guardrails as for Prepare. Executable is the
	// LOCAL itervox binary uploaded to the worker; WorkDir is the run's
	// workspace path, which exists on the worker too (shared file system).
	// DaemonURL, Token, ParentDir, PatchOut and StripEnv are not used.
	Options
	// ForwardTarget is the daemon API's local host:port the turns
	// reverse-forward to; "" without a listener (no callback).
	ForwardTarget string
	// Token is the run's guardrail token: it is used for the callback
	// probe and exported to the agent (TokenEnv), never written remotely.
	Token string
}

// RemoteRuntime is one run's guardrails directory on an SSH worker.
type RemoteRuntime struct {
	host, bin            string
	dir, binDir, policy  string
	gitConfigParameters  string
	forwardTarget, token string
	callback             bool
	callbackErr          error
}

// remoteOpTimeout bounds each ssh call InstallRemote and the runtime's
// methods make (the upload has its own, larger bound).
const remoteOpTimeout = 60 * time.Second

// remoteUploadTimeout bounds the binary upload.
const remoteUploadTimeout = 10 * time.Minute

// InstallRemote installs a run's guardrails on o.Host: it checks the
// worker's platform, uploads this binary when the worker's cache does not
// hold it, prepares the runtime directory there and, with a ForwardTarget
// and Token, checks the callback through a reverse-forwarded socket. A
// failed callback check is not an error (the guards still refuse on the
// worker; approvals are refused): CallbackErr reports it. Worker goroutine.
func InstallRemote(ctx context.Context, o RemoteOptions) (*RemoteRuntime, error) {
	if o.Host == "" || o.Executable == "" {
		return nil, errors.New("guardrails: remote: no host or no itervox executable")
	}
	sum, size, err := cachedDigest(o.Executable)
	if err != nil {
		return nil, fmt.Errorf("guardrails: remote: %w", err)
	}
	r := &RemoteRuntime{host: o.Host, bin: remoteCacheDir + "/" + sum[:16] + "/itervox",
		forwardTarget: o.ForwardTarget, token: o.Token}
	if err := r.ensureBinary(ctx, o.Executable, size); err != nil {
		return nil, err
	}
	req := remoteInstallRequest{
		Protocol: remoteProtocol, WorkDir: o.WorkDir, Identifier: o.Identifier, IssueBranch: o.IssueBranch,
		ProtectedBranches: o.ProtectedBranches, AllowForcePush: o.AllowForcePush,
		ApprovalCommands: o.ApprovalCommands, EnvApprovalCommands: o.EnvApprovalCommands,
		ApprovalsEnabled: o.ApprovalsEnabled && o.ForwardTarget != "" && o.Token != "",
		PreApproved:      o.PreApproved, Lanes: o.Lanes, BaseRefs: o.BaseRefs,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("guardrails: remote: %w", err)
	}
	var res remoteInstallResult
	for attempt := 0; ; attempt++ {
		out, err := r.script(ctx, "printf %s "+agent.ShellQuote(string(data))+" | ./"+r.bin+" "+remoteEntrypoint+" install")
		if err != nil {
			return nil, fmt.Errorf("guardrails: remote: install on %s: %w", o.Host, err)
		}
		if err := json.Unmarshal(lastLine(out), &res); err != nil || res.Protocol != remoteProtocol || res.Dir == "" {
			return nil, fmt.Errorf("guardrails: remote: install on %s: unexpected reply %q", o.Host, truncate(string(out), 200))
		}
		if res.ExeSHA256 == sum {
			break
		}
		r.dir = res.Dir
		r.Remove()
		if attempt > 0 {
			return nil, fmt.Errorf("guardrails: remote: the binary cached on %s does not match this itervox", o.Host)
		}
		if err := r.upload(ctx, o.Executable); err != nil { // a corrupted cache entry: replace it once
			return nil, err
		}
	}
	r.dir, r.binDir, r.policy, r.gitConfigParameters = res.Dir, res.BinDir, res.PolicyPath, res.GitConfigParameters
	if o.ForwardTarget != "" && o.Token != "" {
		r.callbackErr = r.probe(ctx, o.Identifier)
		r.callback = r.callbackErr == nil
	}
	return r, nil
}

// ensureBinary checks the worker's platform and uploads the binary when
// the worker's cache does not have it at the right size.
func (r *RemoteRuntime) ensureBinary(ctx context.Context, exe string, size int64) error {
	out, err := r.script(ctx, "uname -s; uname -m; f=./"+r.bin+"; if [ -f \"$f\" ] && [ -x \"$f\" ]; then wc -c < \"$f\"; else echo 0; fi")
	if err != nil {
		return fmt.Errorf("guardrails: remote: probe %s: %w", r.host, err)
	}
	lines := strings.Fields(string(out))
	if len(lines) < 3 {
		return fmt.Errorf("guardrails: remote: probe %s: unexpected reply %q", r.host, truncate(string(out), 200))
	}
	goos, goarch := platformOf(lines[len(lines)-3], lines[len(lines)-2])
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		return fmt.Errorf("%w: %s is %s/%s (uname %s %s), this binary is %s/%s", ErrRemotePlatform, r.host,
			goos, goarch, lines[len(lines)-3], lines[len(lines)-2], runtime.GOOS, runtime.GOARCH)
	}
	if have, _ := strconv.ParseInt(lines[len(lines)-1], 10, 64); have == size {
		return nil
	}
	return r.upload(ctx, exe)
}

// upload copies exe into the worker's cache: written under a temporary
// name, then renamed, so a concurrent run never executes half a binary.
func (r *RemoteRuntime) upload(ctx context.Context, exe string) error {
	f, err := os.Open(exe)
	if err != nil {
		return fmt.Errorf("guardrails: remote: %w", err)
	}
	defer func() { _ = f.Close() }()
	dir := filepath.Dir(r.bin)
	part := r.bin + "." + randomID() + ".part"
	command := "umask 077 && mkdir -p " + dir + " && cat > " + part + " && chmod 700 " + part + " && mv -f " + part + " " + r.bin
	uctx, cancel := context.WithTimeout(ctx, remoteUploadTimeout)
	defer cancel()
	if err := agent.RemoteUpload(uctx, r.host, command, f); err != nil {
		return fmt.Errorf("guardrails: remote: upload itervox to %s: %w", r.host, err)
	}
	slog.Info("guardrails: uploaded the itervox binary to an SSH worker", "worker_host", r.host, "path", "~/"+r.bin)
	return nil
}

// probe checks the callback: the worker's binary calls a guardrail route
// through a reverse-forwarded socket with the run's token.
func (r *RemoteRuntime) probe(ctx context.Context, identifier string) error {
	sock := r.dir + "/cb-probe.sock"
	script := "export " + TokenEnv + "=" + agent.ShellQuote(r.token) + "; ./" + r.bin + " " + remoteEntrypoint +
		" probe " + agent.ShellQuote(sock) + " " + agent.ShellQuote(identifier) + "; s=$?; rm -f " + agent.ShellQuote(sock) + "; exit $s"
	_, err := r.script(ctx, script, agent.ForwardArgs(sock, r.forwardTarget)...)
	return err
}

// script runs a short script on the worker in the remote user's home.
func (r *RemoteRuntime) script(ctx context.Context, script string, sshArgs ...string) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, remoteOpTimeout)
	defer cancel()
	return agent.RemoteScript(sctx, r.host, "cd || exit 1; "+script, sshArgs...)
}

// Dir is the runtime directory on the worker.
func (r *RemoteRuntime) Dir() string { return r.dir }

// BinDir is the worker's shim directory.
func (r *RemoteRuntime) BinDir() string { return r.binDir }

// Callback reports whether the shims reach the daemon (reports and
// approvals); CallbackErr is why not, when a callback was asked for.
func (r *RemoteRuntime) Callback() bool     { return r.callback }
func (r *RemoteRuntime) CallbackErr() error { return r.callbackErr }

// AgentEnv is the run environment of the run's turns on the worker: the
// shims first on PATH, core.hooksPath through GIT_CONFIG_PARAMETERS, the
// dashboard token blanked and, with a callback, the guardrail token and a
// per-turn reverse-forwarded socket.
func (r *RemoteRuntime) AgentEnv() agent.RunEnv {
	env := &agent.RemoteEnv{
		Exports:     []string{"GIT_CONFIG_PARAMETERS=" + r.gitConfigParameters, "ITERVOX_API_TOKEN="},
		PathPrepend: r.binDir,
	}
	if r.callback {
		env.Exports = append(env.Exports, TokenEnv+"="+r.token)
		env.ForwardDir, env.ForwardTarget, env.ForwardEnv = r.dir, r.forwardTarget, SocketEnv
	}
	return agent.RunEnv{Remote: env}
}

// SetIssueBranch records the issue branch in the worker's policy.
func (r *RemoteRuntime) SetIssueBranch(branch string) error {
	if r == nil || branch == "" {
		return nil
	}
	_, err := r.script(context.Background(), "./"+r.bin+" "+remoteEntrypoint+" set-branch "+
		agent.ShellQuote(r.policy)+" "+agent.ShellQuote(branch))
	return err
}

// RefusedApprovals is Runtime.RefusedApprovals read on the worker.
func (r *RemoteRuntime) RefusedApprovals() []string {
	if r == nil || r.policy == "" {
		return nil
	}
	out, err := r.script(context.Background(), "./"+r.bin+" "+remoteEntrypoint+" refused "+agent.ShellQuote(r.policy))
	if err != nil {
		slog.Warn("guardrails: cannot read the refusals of a run on an SSH worker", "worker_host", r.host, "error", err)
		return nil
	}
	var cmds []string
	_ = json.Unmarshal(lastLine(out), &cmds)
	return cmds
}

// Remove deletes the runtime directory on the worker. Best effort: a
// failure is logged (the directory is in the worker's temp dir, and its
// shims refuse everything once the run's token is revoked). Safe on nil.
func (r *RemoteRuntime) Remove() {
	if r == nil || r.dir == "" {
		return
	}
	if _, err := r.script(context.Background(), "./"+r.bin+" "+remoteEntrypoint+" remove "+agent.ShellQuote(r.dir)); err != nil {
		slog.Warn("guardrails: cannot remove a run's guardrails directory on an SSH worker",
			"worker_host", r.host, "dir", r.dir, "error", err)
		return
	}
	r.dir = ""
}

// platformOf maps `uname -s` and `uname -m` to GOOS and GOARCH.
func platformOf(sysname, machine string) (string, string) {
	goos := strings.ToLower(sysname)
	goarch := strings.ToLower(machine)
	switch goarch {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64", "aarch64_be":
		goarch = "arm64"
	case "i386", "i486", "i586", "i686", "x86":
		goarch = "386"
	case "armv6l", "armv7l", "armv7", "arm":
		goarch = "arm"
	}
	return goos, goarch
}

var (
	digestMu    sync.Mutex
	digestCache = map[string]digestEntry{}
)

type digestEntry struct {
	size    int64
	modTime time.Time
	sum     string
}

// cachedDigest is fileDigest, remembered while the file's size and
// modification time are unchanged (hashing the binary once per daemon).
func cachedDigest(path string) (string, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	digestMu.Lock()
	e, ok := digestCache[path]
	digestMu.Unlock()
	if ok && e.size == fi.Size() && e.modTime.Equal(fi.ModTime()) {
		return e.sum, e.size, nil
	}
	sum, size, err := fileDigest(path)
	if err != nil {
		return "", 0, err
	}
	digestMu.Lock()
	digestCache[path] = digestEntry{size: size, modTime: fi.ModTime(), sum: sum}
	digestMu.Unlock()
	return sum, size, nil
}

// fileDigest is the hex SHA-256 and size of the file at path.
func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func lastLine(out []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return []byte(lines[len(lines)-1])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
