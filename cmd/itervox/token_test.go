package main

import "testing"

// TestShouldGenerateToken covers the #48 fix: token auto-generation is
// decided purely by "is a token already set" and "did the operator opt
// out" — bind address is deliberately NOT a parameter, because a loopback
// bind behind a tunnel/reverse proxy is exactly as exposed as a
// non-loopback bind and the daemon cannot detect that from inside the
// process.
func TestShouldGenerateToken(t *testing.T) {
	tests := []struct {
		name               string
		allowUnauthentic   bool
		envToken           string
		wantShouldGenerate bool
	}{
		{
			name:               "empty env token, no opt-out -> generate",
			allowUnauthentic:   false,
			envToken:           "",
			wantShouldGenerate: true,
		},
		{
			name:               "env token already set -> do not generate",
			allowUnauthentic:   false,
			envToken:           "some-pinned-token",
			wantShouldGenerate: false,
		},
		{
			name:               "opt-out set, no env token -> do not generate",
			allowUnauthentic:   true,
			envToken:           "",
			wantShouldGenerate: false,
		},
		{
			name:               "opt-out set AND env token set -> do not generate",
			allowUnauthentic:   true,
			envToken:           "some-pinned-token",
			wantShouldGenerate: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldGenerateToken(tt.allowUnauthentic, tt.envToken)
			if got != tt.wantShouldGenerate {
				t.Errorf("shouldGenerateToken(%v, %q) = %v, want %v",
					tt.allowUnauthentic, tt.envToken, got, tt.wantShouldGenerate)
			}
		})
	}
}
