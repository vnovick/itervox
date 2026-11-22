//go:build unix

package egress

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// Main runs the forwarder; args[0] is ForwardEntrypoint. It returns an exit
// status only on failure: on success the process becomes the agent.
func Main(args []string, stderr io.Writer) int {
	bridges, serve, argv, err := parseForwardArgs(args[1:])
	if err != nil {
		return usage(stderr, err)
	}
	if serve {
		// fd 3 is the readiness pipe the launching copy passed.
		readyPipe := os.NewFile(3, "ready")
		err := serveBridges(bridges, func() {
			_, _ = readyPipe.WriteString("ok\n")
			_ = readyPipe.Close()
		})
		return usage(stderr, err)
	}
	if len(argv) == 0 {
		return usage(stderr, errors.New("no command after --"))
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return usage(stderr, err)
	}
	if err := startServer(args[0], bridges, stderr); err != nil {
		return usage(stderr, err)
	}
	// Exec, not a child: the agent keeps this pid, its fds (fd 3 carries
	// the prompt) and its exit status goes straight to the runner.
	err = syscall.Exec(bin, argv, os.Environ())
	return usage(stderr, err)
}

// startServer starts the serving copy and waits until it listens. The copy
// gets no stdin/stdout and the readiness pipe on fd 3, which replaces any
// inherited fd 3 (the prompt) so the copy never holds it.
func startServer(entry string, bridges []Bridge, stderr io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	args := []string{entry, serveFlag}
	for _, b := range bridges {
		args = append(args, "--listen", strconv.Itoa(b.Port)+"="+b.Socket)
	}
	cmd := exec.Command(self, args...)
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return err
	}
	_ = w.Close()
	// The serving copy is not waited for: it ends with the sandbox's pid
	// namespace. Release it so no zombie bookkeeping remains here.
	_ = cmd.Process.Release()
	line := make(chan error, 1)
	go func() {
		defer recoverEgressGoroutine("forwarder readiness")
		s, err := bufio.NewReader(r).ReadString('\n')
		if err == nil && s != "ok\n" {
			err = errors.New("forwarder did not start")
		}
		line <- err
	}()
	select {
	case err := <-line:
		if err != nil {
			return errors.New("the forwarder's listeners did not start")
		}
		return nil
	case <-time.After(readyTimeout):
		return errors.New("timed out waiting for the forwarder's listeners")
	}
}
