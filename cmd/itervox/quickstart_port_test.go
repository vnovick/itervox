//go:build unix

package main

import (
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQuickstartIgnoresBusyPort8090: the quickstart tests do not depend on
// the machine's port 8090. A developer running itervox (a daemon or the hub
// on the default port) must still see them pass, so this test holds 8090
// itself (when it is free) and runs quickstart on a fixture that uses the
// default port.
func TestQuickstartIgnoresBusyPort8090(t *testing.T) {
	if ln, err := net.Listen("tcp", "127.0.0.1:8090"); err == nil {
		t.Cleanup(func() { _ = ln.Close() })
	} // else: something already holds it, which is the case under test
	dir, _, _ := quickstartGitHubProject(t, wantStateLabels)
	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, NoStart: true}, strings.NewReader(""), &out)
	require.Equal(t, 0, code, out.String())
	assert.NotContains(t, out.String(), "port 8090 in use")
}

// TestDoctorWarnsWhenAnotherProcessHoldsThePort: with the probe reporting a
// holder that is not this workflow's daemon, doctor still warns and exits 1
// (the TestMain stub only keeps the machine's real port 8090 out of the
// other tests).
func TestDoctorWarnsWhenAnotherProcessHoldsThePort(t *testing.T) {
	orig := doctorPortHolder
	t.Cleanup(func() { doctorPortHolder = orig })
	var probed []int
	doctorPortHolder = func(port int) (string, int) {
		probed = append(probed, port)
		return " (held by nginx 4242 root)", 4242
	}
	_, wf, _ := quickstartGitHubProject(t, wantStateLabels)
	report, code := runDoctorChecks(wf, nil)
	assert.Equal(t, []int{8090}, probed, "the configured (default) port is probed")
	assert.Contains(t, report, "WARNING: port 8090 in use (held by nginx 4242 root)")
	assert.Equal(t, 1, code)
}
