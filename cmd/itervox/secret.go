package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/joho/godotenv"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// `itervox secret` (#88) sets values in .itervox/.env without them ever
// appearing on a command line, in shell history or in output, which is
// what the deploy skill uses for tracker keys, agent credentials and the
// dashboard token.
//
//	itervox secret set KEY [--workflow WORKFLOW.md] [--replace]
//	itervox secret list [--workflow WORKFLOW.md]

// secretKeyRe is a valid environment variable name.
var secretKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Seams tests replace.
var (
	secretStdinIsTerminal = func() bool { return term.IsTerminal(os.Stdin.Fd()) }
	secretReadPassword    = func() ([]byte, error) { return term.ReadPassword(os.Stdin.Fd()) }
)

func runSecret(args []string) {
	fatalExit(secret(args, os.Stdin, os.Stdout, os.Stderr))
}

// secret runs `itervox secret` and returns the exit code.
func secret(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 || (args[0] != "set" && args[0] != "list") {
		_, _ = fmt.Fprintln(errOut, "usage: itervox secret set KEY [--workflow WORKFLOW.md] [--replace]\n       itervox secret list [--workflow WORKFLOW.md]")
		return 2
	}
	fs := flag.NewFlagSet("secret "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	workflow := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md (the file is .itervox/.env next to it)")
	replace := fs.Bool("replace", false, "replace a value that is already set (rotation)")
	// Flags may come before or after KEY.
	var positional []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	envPath := filepath.Join(filepath.Dir(*workflow), ".itervox", ".env")

	if args[0] == "list" {
		if len(positional) > 0 {
			_, _ = fmt.Fprintf(errOut, "itervox secret list: unexpected argument %q\n", positional[0])
			return 2
		}
		return secretList(envPath, out, errOut)
	}
	if len(positional) != 1 {
		_, _ = fmt.Fprintln(errOut, "itervox secret set: name exactly one KEY; the value is read from the terminal (not echoed) or from stdin")
		return 2
	}
	key := positional[0]
	if !secretKeyRe.MatchString(key) {
		_, _ = fmt.Fprintf(errOut, "itervox secret set: %q is not a variable name\n", key)
		return 2
	}
	value, err := readSecretValue(key, in, errOut)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox secret set: %v\n", err)
		return 1
	}
	if err := setEnvSecret(envPath, key, value, *replace); err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox secret set: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "itervox secret: saved %s to %s (value not shown)\n", key, envPath)
	return 0
}

// readSecretValue reads the value without echo from a terminal, or the
// first line of piped stdin.
func readSecretValue(key string, in io.Reader, errOut io.Writer) (string, error) {
	var value string
	if secretStdinIsTerminal() {
		_, _ = fmt.Fprintf(errOut, "%s (input hidden): ", key)
		raw, err := secretReadPassword()
		_, _ = fmt.Fprintln(errOut)
		if err != nil {
			return "", err
		}
		value = string(raw)
	} else {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value = line
	}
	value = strings.TrimRight(value, "\r\n")
	if strings.TrimSpace(value) == "" {
		return "", errors.New("empty value; nothing saved")
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("the value has a line break; nothing saved")
	}
	return value, nil
}

// envLine renders key=value so that godotenv, which the daemon loads .env
// with, reads exactly value back. godotenv has no quoting that holds every
// value (a value ending in a backslash cannot be quoted, and its own Marshal
// output does not always parse), so the candidate forms are tried in turn
// and the first that round-trips is used; a value none can carry is refused.
func envLine(key, value string) (string, error) {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
	for _, line := range []string{
		key + "='" + value + "'",
		key + `="` + esc.Replace(value) + `"`,
		key + "=" + value,
	} {
		if m, err := godotenv.Unmarshal(line); err == nil && len(m) == 1 && m[key] == value {
			return line, nil
		}
	}
	return "", errors.New("this value cannot be written to a .env file so that it reads back unchanged (for example, quotes combined with a trailing backslash); nothing saved")
}

// placeholderRe is an `itervox init` stub value such as
// lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx: a prefix, then only x's.
var placeholderRe = regexp.MustCompile(`^[A-Za-z_]*x{8,}$`)

// errUnparsable is returned for a dotenv file the daemon's loader rejects.
// It never carries the parser's error: godotenv quotes the input around the
// fault, which is secret values.
var errUnparsable = errors.New("does not parse as a dotenv file (the daemon would not load it); fix it first, nothing saved")

// parseEnv parses a whole dotenv file the way the daemon loads it.
func parseEnv(content string) (map[string]string, error) {
	m, err := godotenv.Unmarshal(content)
	if err != nil {
		return nil, errUnparsable
	}
	return m, nil
}

// secretStatus is "empty", "placeholder" or "set" for a value.
func secretStatus(value string) string {
	v := strings.TrimSpace(value)
	switch {
	case v == "":
		return "empty"
	case placeholderRe.MatchString(v):
		return "placeholder"
	}
	return "set"
}

// resolveEnvLink follows path through symlinks, dangling ones included, so
// the file the link points at is the one written.
func resolveEnvLink(path string) string {
	for range 40 {
		target, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	return path
}

// setEnvSecret sets key in the dotenv file at path. It replaces a
// placeholder or empty line for key, appends otherwise, and replaces a real
// value only with replace. The file is written atomically, mode 0600; a
// symlinked file is written through to its target, and an existing file
// keeps its owner (so a root-run rotation stays readable by the service
// user).
func setEnvSecret(path, key, value string, replace bool) error {
	path = resolveEnvLink(path)
	info, statErr := os.Stat(path)
	ownerDir := filepath.Dir(path)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	if len(raw) > 0 {
		lines = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
	line, err := envLine(key, value)
	if err != nil {
		return err
	}
	// Decide on the value the daemon would load (the last assignment
	// wins), before touching any line.
	before, err := parseEnv(string(raw))
	if err != nil {
		return fmt.Errorf("%s %w", path, err)
	}
	if secretStatus(before[key]) == "set" && !replace {
		return fmt.Errorf("%s already sets %s; pass --replace to rotate it", path, key)
	}
	replaced := false
	for i, l := range lines {
		k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok || strings.TrimSpace(strings.TrimPrefix(k, "export ")) != key {
			continue
		}
		if replaced {
			lines[i] = "" // a rotated value leaves no older copy behind
			continue
		}
		lines[i] = line
		replaced = true
	}
	if !replaced {
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n") + "\n"
	// The edit is line-based; prove it changed exactly key, so a line
	// inside another (multi-line) value is never rewritten.
	after, err := parseEnv(content)
	if err != nil || after[key] != value || !sameOtherValues(before, after, key) {
		return fmt.Errorf("%s: cannot set %s without changing other lines of the file (is %s inside a multi-line value?); edit it by hand, nothing saved", path, key, key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := atomicfs.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	// Keep the owner (a root-run rotation stays readable by the service
	// user); a new file takes its directory's owner.
	if statErr != nil {
		info, statErr = os.Stat(ownerDir)
	}
	if statErr == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
			if err := os.Chown(path, int(st.Uid), int(st.Gid)); err != nil {
				return fmt.Errorf("keep the owner of %s: %w", path, err)
			}
		}
	}
	return nil
}

// sameOtherValues reports whether a and b agree on every variable but key.
func sameOtherValues(a, b map[string]string, key string) bool {
	for k, v := range a {
		if w, ok := b[k]; k != key && (!ok || w != v) {
			return false
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok && k != key {
			return false
		}
	}
	return true
}

// secretList prints each variable the daemon would load, in file order,
// with whether it is set, a placeholder or empty, never its value. Names
// come from the parsed file, so the continuation lines of a multi-line value
// are never printed.
func secretList(path string, out, errOut io.Writer) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox secret list: %v\n", err)
		return 1
	}
	vars, err := parseEnv(string(raw))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox secret list: %s %v\n", path, err)
		return 1
	}
	// File order for the names godotenv parsed; a line that only looks like
	// an assignment (inside a quoted value) names nothing it parsed, or
	// a variable printed once anyway.
	var names []string
	seen := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		k, _, ok := strings.Cut(strings.TrimSpace(l), "=")
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		if _, parsed := vars[k]; ok && parsed && !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	rest := make([]string, 0)
	for k := range vars {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	for _, k := range append(names, rest...) {
		_, _ = fmt.Fprintf(out, "%s: %s\n", k, secretStatus(vars[k]))
	}
	return 0
}
