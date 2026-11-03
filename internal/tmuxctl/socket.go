package tmuxctl

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ServerSocket returns the path of the server socket a command from this
// client talks to, resolved the way tmux resolves it: Client.Socket, else
// the socket of the session the daemon runs in ($TMUX), else
// $TMUX_TMPDIR (default /tmp)/tmux-<uid>/default.
func (c Client) ServerSocket() string {
	if c.Socket != "" {
		return c.Socket
	}
	if env := os.Getenv("TMUX"); env != "" {
		if sock, _, _ := strings.Cut(env, ","); sock != "" {
			return sock
		}
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default")
}

// ServerMayRun reports whether a tmux server may be listening: false only
// when its socket path does not exist, which a stat answers without
// starting a tmux client (#210). Any other answer — including a stat error
// other than "does not exist" — is true, so a caller never skips a server
// that is there.
func (c Client) ServerMayRun() bool {
	_, err := os.Stat(c.ServerSocket())
	return !errors.Is(err, fs.ErrNotExist)
}
