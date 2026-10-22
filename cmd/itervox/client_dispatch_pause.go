package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// dispatchPauseState is the response of POST /api/v1/dispatch/pause|resume
// (#213): the pause the daemon applied, null when dispatch runs.
type dispatchPauseState struct {
	DispatchPaused *struct {
		Reason string     `json:"reason"`
		Until  *time.Time `json:"until"`
	} `json:"dispatchPaused"`
}

// dispatchPauseCommand runs `itervox pause [--reason r] [--for d]` and
// `itervox resume` without an issue: pause or resume ALL dispatch (#213).
// Running agents continue either way.
func dispatchPauseCommand(ctx context.Context, c *daemonClient, name, reason string, pauseFor time.Duration,
	out io.Writer, fail func(error) int, usage func(string) int) int {
	var res dispatchPauseState
	if name == "resume" {
		if reason != "" || pauseFor != 0 {
			return usage("usage: itervox resume (--reason and --for are pause flags)")
		}
		if err := c.request(ctx, http.MethodPost, "/dispatch/resume", nil, &res); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintln(out, "Resumed dispatch; waiting issues start as slots allow.")
		return 0
	}
	if pauseFor < 0 {
		return usage("--for must be positive, e.g. --for 2h")
	}
	body := map[string]string{}
	if reason != "" {
		body["reason"] = reason
	}
	if pauseFor > 0 {
		body["until"] = time.Now().Add(pauseFor).UTC().Format(time.RFC3339)
	}
	if err := c.request(ctx, http.MethodPost, "/dispatch/pause", body, &res); err != nil {
		return fail(err)
	}
	msg := "Paused all dispatch; running agents continue, nothing new starts"
	if p := res.DispatchPaused; p != nil && p.Until != nil {
		msg += " until " + p.Until.Local().Format("15:04 Mon Jan 2")
	} else {
		msg += " until itervox resume"
	}
	_, _ = fmt.Fprintln(out, msg+".")
	return 0
}
