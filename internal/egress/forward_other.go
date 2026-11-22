//go:build !unix

package egress

import (
	"errors"
	"io"
)

// Main is the forwarder, which only bubblewrap's bridge mode (Linux) uses.
func Main(_ []string, stderr io.Writer) int {
	return usage(stderr, errors.New("not supported on this operating system"))
}
