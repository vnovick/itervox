//go:build !windows

package statusui

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

var stdinIsTerminal = func() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

var openControllingTTY = func() (int, io.Closer, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("statusui: open controlling tty: %w", err)
	}
	return int(tty.Fd()), tty, nil
}

func foregroundTTYFD() (int, io.Closer, error) {
	if stdinIsTerminal() {
		return int(os.Stdin.Fd()), nil, nil
	}
	return openControllingTTY()
}

func currentForegroundTTYProcessGroups() (int, int, error) {
	fd, closer, err := foregroundTTYFD()
	if err != nil {
		return 0, 0, err
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}

	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return 0, 0, fmt.Errorf("statusui: get foreground tty process group: %w", err)
	}

	return foreground, syscall.Getpgrp(), nil
}

func currentForegroundTTYProcessGroupExists(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, nil
	}
	err := syscall.Kill(-pgid, 0)
	switch err {
	case nil, syscall.EPERM:
		return true, nil
	case syscall.ESRCH:
		return false, nil
	default:
		return false, fmt.Errorf("statusui: check foreground tty process group %d: %w", pgid, err)
	}
}

func currentSetForegroundTTYProcessGroup(pgid int) error {
	fd, closer, err := foregroundTTYFD()
	if err != nil {
		return err
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, pgid); err != nil {
		return fmt.Errorf("statusui: reclaim foreground tty process group %d: %w", pgid, err)
	}
	return nil
}
