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
	"strings"

	"github.com/charmbracelet/x/term"

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

// quoteEnvValue quotes value so godotenv (which the daemon loads .env with)
// reads it back unchanged: single quotes are literal; a value containing a
// single quote is double-quoted with \, " and $ escaped.
func quoteEnvValue(value string) string {
	if !strings.Contains(value, "'") {
		return "'" + value + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
	return `"` + r.Replace(value) + `"`
}

// setEnvSecret sets key in the dotenv file at path. It replaces a
// placeholder or empty line for key, appends otherwise, and replaces a real
// value only with replace. The file is written atomically, mode 0600.
func setEnvSecret(path, key, value string, replace bool) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	if len(raw) > 0 {
		lines = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
	line := key + "=" + quoteEnvValue(value)
	replaced := false
	for i, l := range lines {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok || strings.TrimSpace(strings.TrimPrefix(k, "export ")) != key {
			continue
		}
		if replaced {
			lines[i] = "" // a later duplicate would win over the new value
			continue
		}
		if isRealSecret(strings.Trim(v, `"' `)) && !replace {
			return fmt.Errorf("%s already sets %s; pass --replace to rotate it", path, key)
		}
		lines[i] = line
		replaced = true
	}
	if !replaced {
		lines = append(lines, line)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfs.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// secretList prints each variable in the file with whether it is set, a
// placeholder or empty, never its value.
func secretList(path string, out, errOut io.Writer) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox secret list: %v\n", err)
		return 1
	}
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"' `)
		status := "set"
		switch {
		case v == "":
			status = "empty"
		case !isRealSecret(v):
			status = "placeholder"
		}
		_, _ = fmt.Fprintf(out, "%s: %s\n", strings.TrimSpace(strings.TrimPrefix(k, "export ")), status)
	}
	return 0
}
