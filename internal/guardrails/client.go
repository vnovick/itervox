package guardrails

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client reaches the daemon's guardrail endpoints with the run's token:
//
//	POST /api/v1/agent-actions/{identifier}/guardrail/events      Event
//	POST /api/v1/agent-actions/{identifier}/guardrail/approvals   ApprovalRequest → ApprovalStatus
//	GET  /api/v1/agent-actions/{identifier}/guardrail/approvals/{id}              → ApprovalStatus
type client struct {
	base, token, identifier string
	http                    *http.Client
}

func newClient(p Policy) client {
	return client{base: strings.TrimRight(p.DaemonURL, "/"), token: p.Token, identifier: p.Identifier,
		http: &http.Client{Timeout: 10 * time.Second}}
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
		return
	}
	_ = c.do(http.MethodPost, c.endpoint("events"), ev, nil)
}

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
