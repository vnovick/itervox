package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretWorkflow(t *testing.T) (workflow, envPath string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "WORKFLOW.md"), filepath.Join(dir, ".itervox", ".env")
}

func pipedSecret(t *testing.T) {
	t.Helper()
	old := secretStdinIsTerminal
	secretStdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { secretStdinIsTerminal = old })
}

// TestSecretSetRoundTripsThroughDotenv (#88): any value `itervox secret set`
// saves reads back unchanged through the loader the daemon uses, the file is
// 0600, and the value never appears in the command's output.
func TestSecretSetRoundTripsThroughDotenv(t *testing.T) {
	pipedSecret(t)
	values := map[string]string{
		"A": "sk-ant-abc123",
		"B": `pa$$word#not-a-comment`,
		"C": `it's "quoted" \ and $HOME`,
		"D": "  spaced value = with equals  ",
		"E": "trailing'",
		"F": `ends with a backslash\`,
		"G": `it's "wrapped"`,
		"H": `\n literal, ${VAR}, ` + "`tick`" + `, ünïcode, #lead`,
		"I": `=starts with equals`,
		"J": `$dollar at start`,
		"K": `back\slash 'and' quote\`,
	}
	workflow, envPath := secretWorkflow(t)
	for k, v := range values {
		var out, errOut bytes.Buffer
		code := secret([]string{"set", k, "--workflow", workflow}, strings.NewReader(v+"\n"), &out, &errOut)
		require.Equal(t, 0, code, errOut.String())
		assert.NotContains(t, out.String()+errOut.String(), strings.TrimSpace(v), "the value is never printed")
		assert.Contains(t, out.String(), "value not shown")
	}
	got, err := godotenv.Read(envPath)
	require.NoError(t, err)
	for k, v := range values {
		assert.Equal(t, v, got[k], k)
	}
	info, err := os.Stat(envPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestSecretSetRefusesToOverwriteWithoutReplace (#88): a real value is kept
// unless --replace is given; a placeholder is replaced; an existing 0644 file
// becomes 0600; duplicates of the key are removed.
func TestSecretSetRefusesToOverwriteWithoutReplace(t *testing.T) {
	pipedSecret(t)
	workflow, envPath := secretWorkflow(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("# header\nLINEAR_API_KEY=lin_api_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\nTOKEN=real\nTOKEN=dupe\nREALX=abc-xxxxxxxx-123\n"), 0o644))

	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := secret(append([]string{"set"}, args...), strings.NewReader("new-value\n"), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	code, msg := run("LINEAR_API_KEY", "--workflow", workflow)
	require.Equal(t, 0, code, msg)
	code, msg = run("TOKEN", "--workflow", workflow)
	assert.Equal(t, 1, code)
	assert.Contains(t, msg, "--replace")
	code, msg = run("--replace", "TOKEN", "--workflow", workflow)
	require.Equal(t, 0, code, msg)

	code, _ = run("REALX", "--workflow", workflow)
	assert.Equal(t, 1, code, "a real value containing x's is not a placeholder")

	got, err := godotenv.Read(envPath)
	require.NoError(t, err)
	assert.Equal(t, "abc-xxxxxxxx-123", got["REALX"])
	assert.Equal(t, "new-value", got["LINEAR_API_KEY"])
	assert.Equal(t, "new-value", got["TOKEN"], "the later duplicate no longer overrides the rotation")
	data, _ := os.ReadFile(envPath)
	assert.Contains(t, string(data), "# header")
	info, _ := os.Stat(envPath)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestSecretSetReadsTheTerminalWithoutEcho (#88): on a terminal the value is
// read with echo off, and only the prompt is printed.
func TestSecretSetReadsTheTerminalWithoutEcho(t *testing.T) {
	oldT, oldR := secretStdinIsTerminal, secretReadPassword
	secretStdinIsTerminal = func() bool { return true }
	secretReadPassword = func() ([]byte, error) { return []byte("hidden-secret"), nil }
	t.Cleanup(func() { secretStdinIsTerminal, secretReadPassword = oldT, oldR })
	workflow, envPath := secretWorkflow(t)
	var out, errOut bytes.Buffer
	require.Equal(t, 0, secret([]string{"set", "ANTHROPIC_API_KEY", "--workflow", workflow}, strings.NewReader(""), &out, &errOut))
	assert.Contains(t, errOut.String(), "ANTHROPIC_API_KEY (input hidden): ")
	assert.NotContains(t, out.String()+errOut.String(), "hidden-secret")
	got, err := godotenv.Read(envPath)
	require.NoError(t, err)
	assert.Equal(t, "hidden-secret", got["ANTHROPIC_API_KEY"])
}

// TestSecretListShowsStatusNotValues (#88).
func TestSecretListShowsStatusNotValues(t *testing.T) {
	workflow, envPath := secretWorkflow(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("# c\nA='s3cret-value'\nB=\nC=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\nexport D=x1\n"), 0o600))
	var out, errOut bytes.Buffer
	require.Equal(t, 0, secret([]string{"list", "--workflow", workflow}, nil, &out, &errOut))
	assert.Equal(t, "A: set\nB: empty\nC: placeholder\nD: set\n", out.String())
	assert.NotContains(t, out.String(), "s3cret")
}

// TestSecretRefusesAValueDotenvCannotCarry (#88): a value no .env form
// reads back unchanged is refused, and the file is left untouched.
func TestSecretRefusesAValueDotenvCannotCarry(t *testing.T) {
	pipedSecret(t)
	workflow, envPath := secretWorkflow(t)
	var out, errOut bytes.Buffer
	code := secret([]string{"set", "A", "--workflow", workflow}, strings.NewReader(`it's "x" #\`+"\n"), &out, &errOut)
	if code == 0 {
		got, err := godotenv.Read(envPath)
		require.NoError(t, err)
		assert.Equal(t, `it's "x" #\`, got["A"], "when it is accepted it must read back exactly")
		return
	}
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "nothing saved")
	_, err := os.Stat(envPath)
	assert.True(t, os.IsNotExist(err))
}

// TestSecretRejectsBadInput (#88).
func TestSecretRejectsBadInput(t *testing.T) {
	pipedSecret(t)
	workflow, envPath := secretWorkflow(t)
	for _, tc := range []struct {
		args  []string
		stdin string
		code  int
	}{
		{[]string{}, "", 2},
		{[]string{"get", "A"}, "", 2},
		{[]string{"set"}, "v\n", 2},
		{[]string{"set", "BAD-NAME"}, "v\n", 2},
		{[]string{"set", "A", "B"}, "v\n", 2},
		{[]string{"set", "A"}, "\n", 1},
		{[]string{"set", "A"}, "   \n", 1},
	} {
		var out, errOut bytes.Buffer
		args := tc.args
		if len(args) > 0 {
			args = append(args, "--workflow", workflow)
		}
		assert.Equal(t, tc.code, secret(args, strings.NewReader(tc.stdin), &out, &errOut), "%v", tc.args)
	}
	_, err := os.Stat(envPath)
	assert.True(t, os.IsNotExist(err), "nothing was written")
}

// TestSecretSetKeepsSymlinkAndOwner (#88): a symlinked .env is written
// through to its target, and a file root rewrites keeps its owner, so the
// service user can still read it.
func TestSecretSetKeepsSymlinkAndOwner(t *testing.T) {
	pipedSecret(t)
	workflow, envPath := secretWorkflow(t)
	target := filepath.Join(t.TempDir(), "real.env")
	require.NoError(t, os.WriteFile(target, []byte("A=\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(envPath), 0o755))
	require.NoError(t, os.Symlink(target, envPath))
	if os.Geteuid() == 0 {
		require.NoError(t, os.Chown(target, 4242, 4242))
	}
	var out, errOut bytes.Buffer
	require.Equal(t, 0, secret([]string{"set", "A", "--workflow", workflow}, strings.NewReader("v\n"), &out, &errOut), errOut.String())

	fi, err := os.Lstat(envPath)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the link is kept")
	got, err := godotenv.Read(target)
	require.NoError(t, err)
	assert.Equal(t, "v", got["A"])
	if os.Geteuid() == 0 {
		info, err := os.Stat(target)
		require.NoError(t, err)
		st := info.Sys().(*syscall.Stat_t)
		assert.Equal(t, uint32(4242), st.Uid, "the owner is kept")
	}
}
