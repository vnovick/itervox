//go:build !unix

package main

import (
	"testing"

	"github.com/vnovick/itervox/internal/orchestrator"
)

// assertWatchShowsPolicyChange: the `itervox watch` check uses the Unix-only
// fake daemon, so it runs on Unix only.
func assertWatchShowsPolicyChange(*testing.T, orchestrator.FeedEvent, string) {}
