package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// CORE-167: FailureText becomes the worker exit cause, the retry row's error
// on the dashboard/SSE, and the body of the exhausted-retries tracker
// comment. Agent stderr can carry exported secrets, so FailureText is
// redacted where it is assembled — every consumer inherits it.
func TestFailureTextRedactsStderrSecrets(t *testing.T) {
	secrets := []string{
		"sk-proj-" + "Q9x2LmN4pR7tV1wY3zA6bC8dE0fG2hJ5kL7mN9pQ",
		"ghp_" + "aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5",
		"u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	stderr := strings.Join([]string{
		"Error: 401 Unauthorized",
		"OPENAI_API_KEY=" + secrets[0],
		"token " + secrets[1],
		"cookie " + secrets[2],
		"AWS_SECRET_ACCESS_KEY=" + secrets[3],
	}, "\n")

	for name, got := range map[string]string{
		"parsed failure + stderr": resolveFailureText("agent said "+secrets[1], stderr, errors.New("exit status 1"), nil),
		"stderr only":             resolveFailureText("", stderr, errors.New("exit status 1"), nil),
		"read error + stderr":     resolveFailureText("", stderr, nil, errors.New("read timeout "+secrets[2])),
	} {
		for _, s := range secrets {
			assert.NotContains(t, got, s, name)
		}
		assert.Contains(t, got, "401 Unauthorized", "%s: diagnostic context survives", name)
		assert.Contains(t, got, failureTextStderrLabel, "%s: layout (SplitFailureText) survives", name)
	}
}
