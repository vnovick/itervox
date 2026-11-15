package logging

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// macTempTestDir is a macOS $TMPDIR (/var/folders/<2>/<random>/T) plus a
// t.TempDir under it. The whole run looks random (mixed case, digits,
// high entropy) although no segment does on its own, so the whole-token
// check masked it: TestHeadlessOnATTYDoesNotStartTheTUIOrPrintTheToken
// failed on macOS with `path=***.itervox/daemon.pid`.
const macTempTestDir = "/var/folders/w3/pop_prh2gx40ql86z3nd13ru0000gn/T/TestHeadlessOnATTYDoesNotStartTheTUIOrPrintTheToken--headless2662995922/001/"

// TestRedactKeepsMacOSTempDirPaths: an absolute path is judged segment by
// segment, so a temp-dir path survives whole.
func TestRedactKeepsMacOSTempDirPaths(t *testing.T) {
	for _, s := range []string{
		macTempTestDir,
		"itervox: holding PID file claim path=" + macTempTestDir + ".itervox/daemon.pid pid=1244",
		"hub: created a stored token file=" + macTempTestDir + ".itervox/hub-viewer-token.json",
		"/private" + macTempTestDir + "logs/api-token",
		"file://" + macTempTestDir + "logs/itervox.log",
	} {
		assert.Equal(t, s, RedactString(s), "must not be masked: %s", s)
	}
}

// TestRedactMasksSecretsInsideAbsolutePaths: judging a path by segment
// still masks a segment that is a token, and every prefix / query / header
// rule still applies inside a path or URL.
func TestRedactMasksSecretsInsideAbsolutePaths(t *testing.T) {
	hexToken := strings.Repeat("9f86d081884c7d65", 4) // 64 hex, the dashboard token shape
	cases := []struct{ name, in, secret, keep string }{
		{"token segment in a temp-dir path", macTempTestDir + sentinelBase64 + "/out.log", sentinelBase64, "/var/folders/w3/pop_prh2gx40ql86z3nd13ru0000gn/T/"},
		{"token segment mid-path", "/srv/hooks/" + sentinelBase64 + "/deliver/now", sentinelBase64, "/srv/hooks/"},
		{"github token as a segment", macTempTestDir + sentinelGitHub + "/x", sentinelGitHub, "/var/folders/"},
		{"url with a token query", "http://127.0.0.1:8090" + macTempTestDir + "?token=" + hexToken, hexToken, "?token=" + secretMask},
		{"url with a token query, path first", "open https://example.com/api/v1/state?x=1&token=" + hexToken + " now", hexToken, "&token=" + secretMask},
		// Not an absolute-path run (it starts at the host's "com"), so the
		// whole run is judged and masked, as before.
		{"url with a token path segment", "https://hooks.example.com/services/" + sentinelBase64 + "/x", sentinelBase64, "https://hooks.example."},
		{"bearer next to a path", "GET " + macTempTestDir + " Authorization: Bearer " + sentinelBase64, sentinelBase64, macTempTestDir},
		{"key prefix inside a path", macTempTestDir + "sk-ant-" + strings.Repeat("aB3", 12), "sk-ant-" + strings.Repeat("aB3", 12), "/var/folders/"},
		// A base64 secret that starts with '/' and has one more slash in
		// it: fewer than three segments, so the whole run is still judged.
		{"base64 secret with a leading slash", "cookie /u7Hq2Zx9Lm4Kp8Rt1Vw6/Yb3Nc5Df0Gj2Ah7Ks9Q end", "u7Hq2Zx9Lm4Kp8Rt1Vw6", "cookie "},
		// An AWS-style secret starting with '/' and split into three
		// segments: no segment is a word, so it is not taken for a path.
		{"aws secret with a leading slash", "secret /Jalr7XUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY end", "K7MDENG/bPxRfiCYEXAMPLEKEY", "secret "},
		// The same secret without a leading slash keeps the whole-run check.
		{"base64 secret with slashes", "cookie u7Hq2Zx9Lm4Kp8/Rt1Vw6Yb3Nc5/Df0Gj2Ah7Ks9Q end", "u7Hq2Zx9Lm4Kp8", "cookie "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := RedactString(c.in)
			assert.NotContains(t, out, c.secret, "leak: %q -> %q", c.in, out)
			assert.Contains(t, out, c.keep, "over-redacted: %q -> %q", c.in, out)
		})
	}
}
