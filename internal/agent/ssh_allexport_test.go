package agent

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
)

// TestRemoteWrapperClearsAllexport (M2-close): a login profile that leaves
// `set -a` (allexport) on used to make every wrapper and script assignment
// an environment variable — the NUL-framed script `s` and the prompt
// variable. With a large prompt the next exec failed with E2BIG ("Argument
// list too long": the wrapper's `wc` on macOS at 1 MiB, any exec on Linux
// from 128 KiB) and the wrapper exited 97 blaming a profile that reads
// stdin; with a small one the prompt leaked into the environment of the
// agent and every tool it ran. BASH_ENV runs a `set -a` file before the -c
// command, exactly as a login profile runs before it under `bash -lc`. The 1 MiB prompt must reach
// the agent's stdin byte-exact and appear in neither the agent's env.
func TestRemoteWrapperClearsAllexport(t *testing.T) {
	rng := rand.New(rand.NewPCG(97, 3))
	const alphabet = "abcdefghijklmnopqrstuvwxyz ABCDEFGHIJ0123456789\n'\"\\$`!{}()"
	mk := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		return string(b)
	}
	// The agent: report any prompt-carrying variable in its environment,
	// then echo its stdin.
	agent := `sh -c 'env | grep -E "^(itervox_prompt|s)=" >/dev/null && echo ENV-LEAK; cat'`

	profile := t.TempDir() + "/allexport_profile"
	if err := os.WriteFile(profile, []byte("set -a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bashes := [][2]string{{"path_bash", ""}}
	if _, err := os.Stat("/bin/bash"); err == nil {
		bashes = append(bashes, [2]string{"bin_bash_3.2", "/bin"})
	}
	for _, size := range []int{1 << 10, 1 << 20} {
		prompt := mk(size)
		script := remotePromptAssignment(prompt) + agent + remotePromptRedirect + "; echo END"
		for _, wb := range bashes {
			t.Run(fmt.Sprintf("%s/%d", wb[0], size), func(t *testing.T) {
				out, code := runWrapperUnderBash(t, script, wb[1], "BASH_ENV="+profile)
				if code != 0 {
					t.Fatalf("exit %d; output %.300q", code, out)
				}
				if strings.HasPrefix(out, "ENV-LEAK") {
					t.Fatal("the prompt reached the agent's environment")
				}
				if got := strings.TrimSuffix(out, "END\n"); got != prompt {
					t.Fatalf("prompt differs: want %d bytes, got %d (%.200q)", len(prompt), len(got), got)
				}
			})
		}
	}
}
