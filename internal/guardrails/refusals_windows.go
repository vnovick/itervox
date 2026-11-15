//go:build windows

package guardrails

// openNoFollow is 0 on Windows, which has no O_NOFOLLOW; appendRefusal's
// Lstat check already refuses anything but a regular file.
const openNoFollow = 0
