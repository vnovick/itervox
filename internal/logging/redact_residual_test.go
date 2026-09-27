package logging

import (
	"strings"
	"testing"
)

// M2-close (CORE-167 residual): every shape the independent verifier's probe
// found leaking must be masked, and every diagnostic it found over-redacted
// must be kept. The table is the verifier's probe, turned into assertions.
func TestRedactResidualShapesMasked(t *testing.T) {
	cases := []struct{ name, in, secret string }{
		{"sk-proj", "key sk-proj-Q9x2LmN4pR7tV1wY3zA6bC8dE0fG2hJ5kL7mN9pQ end", "Q9x2LmN4pR7tV1wY3zA6"},
		{"sk-proj short20", "key sk-proj-abcdefghij1234567890 end", "abcdefghij1234567890"},
		{"ghp", "tok ghp_aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5 end", "aB3dE5fG7hJ9kL1mN3pQ"},
		{"github_pat real", "tok github_pat_11ABCDEFG0abcdefghijklmn_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456 end", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"},
		{"xoxb", "slack xoxb-1234567890-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx end", "AbCdEfGhIjKlMnOpQrStUvWx"},
		{"xapp", "slack xapp-1-A0123456789-1234567890123-abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789 end", "abcdef0123456789abcdef0123456789abcdef"},
		{"AKIA", "id AKIAIOSFODNN7EXAMPLE end", "AKIAIOSFODNN7EXAMPLE"},
		{"aws secret bare", "secret wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY end", "wJalrXUtnFEMI/K7MDENG"},
		{"aws creds lowercase", "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "wJalrXUtnFEMI/K7MDENG"},
		{"aws secret starting slash", "aws_secret_access_key=/Jalr7XUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "Jalr7XUtnFEMI/K7MDENG"},
		{"jwt", "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U end", "dozjgNryP4J3jVmNHl0w5N"},
		{"bearer", "Authorization: Bearer abc.def-ghi_jkl123", "abc.def-ghi_jkl123"},
		{"bearer json", `{"Authorization":"Bearer 5f3c8a1b2d4e6f7a8b9c0d1e2f3a4b5c"}`, "5f3c8a1b2d4e6f7a8b9c0d1e2f3a4b5c"},
		{"basic auth", "Authorization: Basic dXNlcjpzM2NyZXRQYXNz", "dXNlcjpzM2NyZXRQYXNz"},
		{"url userinfo", "clone https://bob:Hunter2Pass@git.example.com/r.git failed", "Hunter2Pass"},
		{"url empty user", "clone https://:Hunter2PassWord9@git.example.com/r.git", "Hunter2PassWord9"},
		{"base64 40", "cookie u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q end", "u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q"},
		{"base64 in abs path", "GET /hooks/u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q failed", "u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q"},
		{"hex blob lowercase key", "api_key=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "9f86d081884c7d659a2feaa0c55ad015"},
		{"hex blob json", `{"apiKey":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}`, "9f86d081884c7d659a2feaa0c55ad015"},
		{"x-api-key header", "x-api-key: 9f86d081884c7d659a2feaa0c55ad015", "9f86d081884c7d659a2feaa0c55ad015"},
		{"UPPER env", "export OPENAI_API_KEY=abcdef", "abcdef"},
		{"env mixed", "export GITLAB_TOKEN=glpat-xxxxxxxxxxxxxxxxxxxx", "glpat-xxxx"},
		{"glpat bare", "glpat-AbCdEf1234567890xYz1", "glpat-AbCdEf1234567890xYz1"},
		{"lowercase password", "password=S3cr3tP@ssw0rd!", "S3cr3tP@ssw0rd!"},
		{"stripe", "sk_live_51H8abcDEFghiJKLmnoPQRstu", "sk_live_51H8abcDEFghiJKL"},
		{"anthropic", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789", "AbCdEfGhIjKlMnOpQrStUvWx"},
		{"google api", "AIzaSyA1b2C3d4E5f6G7h8I9j0KlMnOpQrStUvW", "AIzaSyA1b2C3d4E5f6G7h8I9j0"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", "MIIEow"},
		{"base64 no digit", "tok AbCdEfGhIjKlMnOpQrStUvWxYzAbCdEfGhIjKl end", "AbCdEfGhIjKlMnOpQrStUvWxYzAbCdEfGhIjKl"},
		// Additional lowercase / header shapes named in the verdict.
		{"passwd", "passwd: Tr0ub4dor&3xyz", "Tr0ub4dor&3xyz"},
		{"secret=", "client secret=Zq8Vw3nR5tY7uI9oP2aS", "Zq8Vw3nR5tY7uI9oP2aS"},
		{"apiKey header", "X-Api-Key: 1f2e3d4c5b6a79881f2e3d4c5b6a7988", "1f2e3d4c5b6a79881f2e3d4c5b6a7988"},
		{"token segment in wordy path", "POST /Users/developer/projects/itervox/webhooks/u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q failed", "u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q"},
		{"basic header, value not user:pass", "authorization: Basic Zm9vYmFyYmF6cXV4", "Zm9vYmFyYmF6cXV4"},
		{"basic bare", "used Basic dXNlcjpzM2NyZXRQYXNz for the call", "dXNlcjpzM2NyZXRQYXNz"},
		{"url user token no password", "clone https://oauth2:gl-9aZ8yX7wV6uT5sR4@gitlab.example.com/r.git", "gl-9aZ8yX7wV6uT5sR4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := RedactString(c.in); strings.Contains(out, c.secret) {
				t.Fatalf("leak: %q -> %q", c.in, out)
			}
		})
	}
}

func TestRedactKeepsDiagnostics(t *testing.T) {
	keep := []string{
		"at commit 3f2a9c1e8b7d6054a1b2c3d4e5f60718293a4b5c",
		"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"open /Users/dev/project/internal/orchestrator/event_loop.go:42",
		"open internal/orchestrator/recent_failures_test.go:12",
		"ENG-1234 moved",
		"github.com/vnovick/itervox/internal/agent.(*ClaudeRunner).RunTurn",
		"TestSSHLargePromptReachesAgentByteExact/bin_bash/claude/binaryish",
		"request_id=req_011CUz8xY7AbCdEfGh2JkLmNoPq3",
		"msg_01XFDUDYJgAACzvnptvVoYEL rate limited",
		"session 0f3b2c1a-9d8e-4f7a-b6c5-d4e3f2a1b0c9",
		"PR https://github.com/vnovick/itervox/pull/123",
		"branch itervox/ENG-42-fix-the-thing-quickly-2026",
		"file web/src/pages/Dashboard/components/AutomationQueueDetailPanel.tsx",
		"sha512-AbCdEf1234567890AbCdEf1234567890AbCdEf12== integrity",
		"go/pkg/mod/golang.org/x/tools@v0.21.1-0.20240508182429-e35e4ccd0d2d/go",
		"claude-opus-4-1-20250805 model",
		"input_tokens=123 output_tokens=456",
		"the password is required",
		"Basic information about the run",
	}
	for _, k := range keep {
		if out := RedactString(k); out != k {
			t.Errorf("over-redacted: %q -> %q", k, out)
		}
	}
}
