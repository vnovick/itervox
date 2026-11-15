//go:build linux

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// LandlockEntrypoint is the hidden first argument that makes the itervox
// binary act as the Landlock launcher: `itervox <LandlockEntrypoint>
// <policy-json> <argv...>` restricts itself and execs argv (#258).
const LandlockEntrypoint = "__itervox-landlock-exec"

// landlockHelperReady is set by RunIsolationHelper: only a binary that
// handles LandlockEntrypoint at startup may re-execute itself as the
// launcher. Any other binary (a test binary of another package) reports
// Landlock unavailable instead of running its own main with that argument.
var landlockHelperReady atomic.Bool

// RunIsolationHelper must be called first thing in main (and in a test
// binary's TestMain). When args name LandlockEntrypoint it applies the
// policy and execs the command, returning only on failure with the exit
// code to use (ok=true); otherwise it marks this binary as able to act as
// the launcher and returns ok=false.
func RunIsolationHelper(args []string) (code int, ok bool) {
	if len(args) > 1 && args[1] == LandlockEntrypoint {
		return landlockExecMain(args[2:]), true
	}
	landlockHelperReady.Store(true)
	return 0, false
}

// landlock confines an agent with Landlock (kernel 5.13+), the fallback when
// bubblewrap is missing or cannot create namespaces. Landlock only grants,
// so the deny list becomes "grant everything except": every directory on
// the way to a denied path is split into its other entries. Two
// consequences the guide documents: a directory holding a denied path
// cannot be listed itself (its other entries stay usable), and entries
// created there after the run started are not reachable.
type landlock struct {
	// exe is the launcher binary; empty means this executable.
	exe string
}

// newLandlock returns the Landlock mechanism.
func newLandlock() IsolationMechanism { return landlock{} }

func (landlock) Name() string { return "landlock" }

func (l landlock) Available() error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	if abi < 1 {
		return errors.New("landlock is not supported by this kernel")
	}
	if l.exe == "" && !landlockHelperReady.Load() {
		return errors.New("this process cannot re-execute itself as the Landlock launcher")
	}
	return nil
}

func (l landlock) Wrap(argv []string, p IsolationPolicy) ([]string, error) {
	exe := l.exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("%w: cannot locate the itervox binary for the Landlock launcher: %v", ErrIsolationUnavailable, err)
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("agent: encode landlock policy: %w", err)
	}
	return append([]string{exe, LandlockEntrypoint, string(data)}, argv...), nil
}

// landlockABI returns the kernel's Landlock ABI version.
func landlockABI() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, fmt.Errorf("landlock is not supported by this kernel (%v)", errno)
	}
	return int(v), nil
}

const (
	llRead = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	// llFileRights are the rights a rule on a non-directory may carry.
	llFileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
)

// landlockHandled is every filesystem right this ABI knows.
func landlockHandled(abi int) uint64 {
	h := uint64(unix.LANDLOCK_ACCESS_FS_MAKE_SYM<<1) - 1 // ABI 1: EXECUTE..MAKE_SYM
	if abi >= 2 {
		h |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		h |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		h |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return h
}

// landlockGrant is one path_beneath rule.
type landlockGrant struct {
	path   string
	access uint64
}

// landlockGrants turns p into Landlock rules: read everywhere but the
// denied paths, full access to the writable paths (and /dev) but their
// denied parts, read on the re-exposed paths but their denied parts.
func landlockGrants(p IsolationPolicy, handled uint64) []landlockGrant {
	var out []landlockGrant
	add := func(root string, access uint64) {
		for _, r := range complementRoots(root, p.Deny) {
			out = append(out, landlockGrant{path: r, access: access & handled})
		}
	}
	add("/", llRead)
	for _, r := range p.Read {
		add(r, llRead)
	}
	add("/dev", handled)
	for _, w := range p.Write {
		add(w, handled)
	}
	return out
}

// complementRoots returns the paths that together cover root minus every
// denied path inside it: root itself when nothing denied lies inside it,
// nothing when root is itself denied, otherwise the complement within each
// child. A denied ancestor of root does not count: root is a deeper rule
// (a workspace re-exposed under the hidden workspace root) and wins. A
// child symlink is left out (a rule on it would grant its target, which may
// be denied); an unreadable directory is left out (fail closed).
func complementRoots(root string, deny []string) []string {
	var inside bool
	for _, d := range deny {
		if root == d {
			return nil
		}
		if within(d, root) {
			inside = true
		}
	}
	if !inside {
		return []string{root}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		out = append(out, complementRoots(filepath.Join(root, e.Name()), deny)...)
	}
	return out
}

// landlockExecMain is the launcher: args are the policy JSON and the argv
// to run. It never returns on success.
func landlockExecMain(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "itervox: landlock launcher: missing policy or command")
		return 126
	}
	var p IsolationPolicy
	if err := json.Unmarshal([]byte(args[0]), &p); err != nil {
		fmt.Fprintf(os.Stderr, "itervox: landlock launcher: bad policy: %v\n", err)
		return 126
	}
	argv := args[1:]
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "itervox: landlock launcher: %v\n", err)
		return 127
	}
	// Landlock restricts the calling thread; exec from that same thread
	// carries the restriction into the new program.
	runtime.LockOSThread()
	if err := landlockRestrictSelf(p); err != nil {
		fmt.Fprintf(os.Stderr, "itervox: landlock launcher: %v\n", err)
		return 126
	}
	err = syscall.Exec(path, argv, os.Environ())
	fmt.Fprintf(os.Stderr, "itervox: landlock launcher: exec %s: %v\n", path, err)
	return 126
}

// landlockRestrictSelf applies p to the calling thread.
func landlockRestrictSelf(p IsolationPolicy) error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	handled := landlockHandled(abi)
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %v", errno)
	}
	defer func() { _ = unix.Close(int(fd)) }()
	for _, g := range landlockGrants(p, handled) {
		if err := landlockAddRule(int(fd), g); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %v", errno)
	}
	return nil
}

// landlockAddRule adds g; a path that vanished since the policy was built
// is skipped, a non-directory gets only the rights a file can carry.
func landlockAddRule(rulesetFD int, g landlockGrant) error {
	pfd, err := unix.Open(g.path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil //nolint:nilerr // vanished or unreadable: no grant, fail closed
	}
	defer func() { _ = unix.Close(pfd) }()
	var st unix.Stat_t
	if err := unix.Fstat(pfd, &st); err != nil {
		return nil //nolint:nilerr // as above
	}
	access := g.access
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= llFileRights
	}
	if access == 0 {
		return nil
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(pfd)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %v", g.path, errno)
	}
	return nil
}
