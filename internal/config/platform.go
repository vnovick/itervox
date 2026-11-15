package config

import (
	"fmt"
	"runtime"
)

// hostGOOS is the platform ValidateDispatch checks the workflow against.
// A variable so tests can check the Windows rules on any host.
var hostGOOS = runtime.GOOS

// TmuxUnavailableOnWindowsMessage is the load error for
// sessions.tmux_control: true on Windows, where tmux does not run.
const TmuxUnavailableOnWindowsMessage = "sessions.tmux_control: true is not available on Windows: tmux does not run there, so the Sessions panel cannot show or type into tmux panes (remove the setting, or run the daemon on Linux or macOS)"

// validatePlatform refuses settings the daemon cannot honour on goos.
func validatePlatform(cfg *Config, goos string) error {
	if goos == "windows" && cfg.Sessions.TmuxControl {
		return fmt.Errorf("%s", TmuxUnavailableOnWindowsMessage)
	}
	return nil
}
