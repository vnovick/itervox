package guardrails

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// client reaches the daemon's guardrail endpoints with the run's token:
//
//	POST /api/v1/agent-actions/{identifier}/guardrail/events      Event
//	POST /api/v1/agent-actions/{identifier}/guardrail/approvals   ApprovalRequest → ApprovalStatus
//	GET  /api/v1/agent-actions/{identifier}/guardrail/approvals/{id}              → ApprovalStatus
//	POST /api/v1/agent-actions/{identifier}/guardrail/lanes       LaneReport
//
// Without an endpoint, reports go to the run's local refusal log instead
// (refusalLog), which the run reads back (Runtime.RefusedApprovals).
type client struct {
	base, token, identifier string
	logPath                 string
	http                    *http.Client
}

func newClient(p Policy) client {
	c := client{base: strings.TrimRight(p.DaemonURL, "/"), token: p.Token, identifier: p.Identifier,
		http: &http.Client{Timeout: 10 * time.Second}}
	if p.Remote { // an SSH worker: the turn's reverse-forwarded socket (remote.go)
		c.base, c.token = "", os.Getenv(TokenEnv)
		if sock := os.Getenv(SocketEnv); sock != "" {
			// localhost: the daemon's DNS-rebinding check accepts it, and
			// the request does arrive on the daemon's loopback listener.
			c.base = "http://localhost"
			c.http.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			}}
		}
	}
	if p.Dir != "" {
		c.logPath = filepath.Join(p.Dir, refusalLogName)
	}
	return c
}

func (c client) enabled() bool { return c.base != "" && c.token != "" && c.identifier != "" }

func (c client) endpoint(parts ...string) string {
	return c.base + "/api/v1/agent-actions/" + url.PathEscape(c.identifier) + "/guardrail/" + strings.Join(parts, "/")
}

func (c client) do(method, endpoint string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, endpoint, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// report tells the daemon about a refused operation. Best effort: the
// refusal stands whether or not the report lands.
func (c client) report(ev Event) {
	if !c.enabled() {
		appendRefusal(c.logPath, ev)
		return
	}
	_ = c.do(http.MethodPost, c.endpoint("events"), ev, nil)
}

// reportLane records a lane a pushed change reached. Unlike report it is
// not best effort: merge policy depends on it, so the caller refuses the
// push when it fails. A few attempts ride out a busy event loop.
func (c client) reportLane(rep LaneReport) error {
	var err error
	for attempt := 0; attempt < laneReportAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(approvalPoll)
		}
		if err = c.do(http.MethodPost, c.endpoint("lanes"), rep, nil); err == nil {
			return nil
		}
	}
	return err
}

// laneReportAttempts bounds reportLane's retries.
const laneReportAttempts = 3

// awaitDecision raises req and polls until it is decided. A run of
// approvalMaxFailures failed calls ends the wait with an error.
func (c client) awaitDecision(req ApprovalRequest) (ApprovalStatus, error) {
	if !c.enabled() {
		return ApprovalStatus{}, errors.New("no daemon endpoint")
	}
	var st ApprovalStatus
	failures := 0
	for st.ID == "" {
		if err := c.do(http.MethodPost, c.endpoint("approvals"), req, &st); err != nil {
			if failures++; failures >= approvalMaxFailures {
				return st, err
			}
			time.Sleep(approvalPoll)
			continue
		}
		if st.ID == "" {
			return st, errors.New("the daemon returned no request id")
		}
	}
	failures = 0
	id := st.ID
	for st.Status == StatusPending {
		time.Sleep(approvalPoll)
		var next ApprovalStatus
		if err := c.do(http.MethodGet, c.endpoint("approvals", url.PathEscape(id)), nil, &next); err != nil {
			if failures++; failures >= approvalMaxFailures {
				return st, err
			}
			continue
		}
		failures = 0
		st = next
		st.ID = id
	}
	return st, nil
}
