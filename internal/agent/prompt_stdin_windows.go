//go:build windows

package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// unlinkedTempFile creates promptFile's temp file with
// FILE_FLAG_DELETE_ON_CLOSE: Windows cannot unlink an open file, so the
// file is instead deleted by the OS when the last handle to it — the
// daemon's or the agent's inherited stdin — closes, on every exit path.
func unlinkedTempFile() (*os.File, error) {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	path := filepath.Join(os.TempDir(), "itervox-prompt-"+hex.EncodeToString(rnd[:]))
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("agent: prompt file: %w", err)
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		return nil, fmt.Errorf("agent: prompt file: %w", err)
	}
	return os.NewFile(uintptr(h), path), nil
}

// localPromptRedirect is empty on Windows: the login shell (Git Bash) is
// handed the prompt as its own stdin by setPromptFD3, which the CLI it
// runs inherits. exec cannot pass an fd 3 on Windows (ExtraFiles is
// unsupported there), so a login profile that reads stdin could consume
// the prompt — Git Bash's default profile does not.
const localPromptRedirect = ""

// setPromptFD3 makes f the login shell's stdin on Windows (see
// localPromptRedirect).
func setPromptFD3(cmd *exec.Cmd, f *os.File) {
	if f != nil {
		cmd.Stdin = f
	}
}
