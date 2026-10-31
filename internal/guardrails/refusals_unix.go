//go:build unix

package guardrails

import "syscall"

// openNoFollow makes the refusal-log open fail on a symlink.
const openNoFollow = syscall.O_NOFOLLOW
