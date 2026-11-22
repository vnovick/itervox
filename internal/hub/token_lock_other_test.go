//go:build !unix && !windows

package hub

import "testing"

// lockTokenStore: apitokens takes no file lock on this platform.
func lockTokenStore(*testing.T, string) func() { return func() {} }
