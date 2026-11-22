//go:build !linux

package agent

// LandlockEntrypoint is the hidden first argument of the Landlock launcher
// (Linux only, #258).
const LandlockEntrypoint = "__itervox-landlock-exec"

// runLandlockHelper is a no-op where Landlock does not exist.
func runLandlockHelper(_ []string) (code int, ok bool) { return 0, false }
