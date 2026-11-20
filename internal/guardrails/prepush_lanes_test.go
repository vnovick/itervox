package guardrails

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// laneDaemon is the daemon side of the guardrail endpoints a pre-push hook
// calls: a real Approvals behind the approval routes (the test plays the
// human through Decide), and the events and lane reports it received.
type laneDaemon struct {
	srv       *httptest.Server
	approvals *Approvals
	mu        sync.Mutex
	events    []Event
	lanes     []LaneReport
	requests  []ApprovalRequest
}

func newLaneDaemon(t *testing.T) *laneDaemon {
	t.Helper()
	d := &laneDaemon{approvals: NewApprovals()}
	const prefix = "/api/v1/agent-actions/ENG-1/guardrail/"
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		route := strings.TrimPrefix(r.URL.Path, prefix)
		switch {
		case r.Method == http.MethodPost && route == "events":
			var ev Event
			_ = json.NewDecoder(r.Body).Decode(&ev)
			d.mu.Lock()
			d.events = append(d.events, ev)
			d.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && route == "lanes":
			var rep LaneReport
			_ = json.NewDecoder(r.Body).Decode(&rep)
			d.mu.Lock()
			d.lanes = append(d.lanes, rep)
			d.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && route == "approvals":
			var req ApprovalRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			d.mu.Lock()
			d.requests = append(d.requests, req)
			d.mu.Unlock()
			st, _ := d.approvals.Request("ENG-1", "run1", req)
			_ = json.NewEncoder(w).Encode(st)
		case r.Method == http.MethodGet && strings.HasPrefix(route, "approvals/"):
			_ = json.NewEncoder(w).Encode(d.approvals.Status("ENG-1", "run1", strings.TrimPrefix(route, "approvals/")))
		default:
			http.Error(w, "no route", http.StatusNotFound)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

// pending returns the one pending approval request, once there is one.
func (d *laneDaemon) pending(t *testing.T) Approval {
	t.Helper()
	var got Approval
	require.Eventually(t, func() bool {
		d.approvals.mu.Lock()
		defer d.approvals.mu.Unlock()
		for _, a := range d.approvals.byID {
			if a.Status == StatusPending {
				got = *a
				return true
			}
		}
		return false
	}, 20*time.Second, 20*time.Millisecond, "the push never raised an approval request")
	return got
}

func (d *laneDaemon) eventKinds() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, e := range d.events {
		out = append(out, e.Kind)
	}
	return out
}

func (d *laneDaemon) laneReports() []LaneReport {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]LaneReport(nil), d.lanes...)
}

// testLanes are the lanes the pre-push tests use: the default human list's
// shape plus a second-opinion area.
var testLanes = Lanes{
	Human:         []string{".github/**", ".itervox/**", "!.itervox/handoff/**", "Makefile"},
	SecondOpinion: []string{"auth/**"},
}

func newLaneRig(t *testing.T, d *laneDaemon, approvals bool) *rig {
	t.Helper()
	return newRig(t, func(o *Options) {
		o.Lanes = testLanes
		o.BaseRefs = []string{"origin/main", "main"}
		if d != nil {
			o.DaemonURL, o.Token, o.ApprovalsEnabled = d.srv.URL, "tok", approvals
		}
	})
}

// writeCommit writes rel (creating its directory) and commits it.
func (r *rig) writeCommit(rel, content, msg string) {
	r.t.Helper()
	path := filepath.Join(r.work, rel)
	require.NoError(r.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(r.t, os.WriteFile(path, []byte(content), 0o644))
	git(r.t, r.work, "add", rel)
	git(r.t, r.work, "commit", "-q", "-m", msg)
}

// startPush starts `git push origin HEAD` as the agent, in the background.
func (r *rig) startPush() (wait func() (string, int)) {
	r.t.Helper()
	cmd := r.agentCmd("git push origin HEAD")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	require.NoError(r.t, cmd.Start())
	return func() (string, int) {
		err := cmd.Wait()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return buf.String(), exitErr.ExitCode()
		}
		require.NoError(r.t, err)
		return buf.String(), 0
	}
}

// guardrails.lanes: a push that changes .github/workflows/ci.yml is held
// for a human, names the path, and does not reach the remote until
// approved; the approval lets that push through once (a later push of new
// pipeline changes asks again); a rejection refuses the push with the
// human's reply; without an approval channel (tmux sessions) it is refused
// and the agent is told to ask the user.
func TestPrePushHumanLaneHoldsForApproval(t *testing.T) {
	d := newLaneDaemon(t)
	r := newLaneRig(t, d, true)
	r.commit("code")
	out, code := r.agent("git push -q origin HEAD")
	require.Zero(t, code, out)
	before := r.remoteRef("refs/heads/itervox/eng-1")

	r.writeCommit(".github/workflows/ci.yml", "jobs: {test: {continue-on-error: true}}\n", "make CI pass")
	wait := r.startPush()
	a := d.pending(t)
	assert.Equal(t, LaneHuman, a.Lane)
	assert.Equal(t, []string{".github/workflows/ci.yml"}, a.Paths, "only the pipeline file, not the earlier code commit")
	assert.Equal(t, 1, a.PathsTotal)
	assert.Equal(t, "git push origin refs/heads/itervox/eng-1", a.Command)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, r.remoteRef("refs/heads/itervox/eng-1"), "nothing reaches the remote before the approval")

	require.True(t, d.approvals.Decide(a.ID, true, "approve"))
	out, code = wait()
	require.Zero(t, code, out)
	assert.Contains(t, out, "guardrails.lanes.human: it changes `.github/workflows/ci.yml`")
	assert.Contains(t, out, "approved; running `git push origin refs/heads/itervox/eng-1` once")
	assert.Equal(t, git(t, r.work, "rev-parse", "HEAD"), r.remoteRef("refs/heads/itervox/eng-1"))
	assert.Equal(t, []LaneReport{{Lane: LaneHuman, Paths: []string{".github/workflows/ci.yml"}, PathsTotal: 1}}, d.laneReports(),
		"the approved push is reported for merge policy")

	// The approval was spent by that push: new pipeline commits ask again,
	// and a rejection refuses them with the reply as the reason.
	pushed := r.remoteRef("refs/heads/itervox/eng-1")
	r.writeCommit("Makefile", "test:\n\ttrue\n", "skip tests")
	wait = r.startPush()
	b := d.pending(t)
	assert.NotEqual(t, a.ID, b.ID)
	assert.Equal(t, []string{"Makefile"}, b.Paths)
	require.True(t, d.approvals.Decide(b.ID, false, "no, fix the test instead"))
	out, code = wait()
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "the operator rejected `git push origin refs/heads/itervox/eng-1`: no, fix the test instead")
	assert.Equal(t, pushed, r.remoteRef("refs/heads/itervox/eng-1"), "a rejected push does not reach the remote")
	assert.Len(t, d.laneReports(), 1, "a refused push reports no lane")

	// No approval channel (a tmux session): refused at once, reported.
	td := newLaneDaemon(t)
	tmux := newLaneRig(t, td, false)
	tmux.writeCommit(".github/workflows/ci.yml", "x\n", "ci")
	out, code = tmux.agent("git push origin HEAD")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "approvals are not available here. Ask the user to review and push these changes themselves")
	assert.Empty(t, tmux.remoteRef("refs/heads/itervox/eng-1"))
	assert.Equal(t, []string{KindLaneHuman}, td.eventKinds())
}

// guardrails.lanes: a push of code and tests only is in the auto lane: it
// goes straight through, asks nothing and reports nothing. A push that
// touches a second-opinion path also goes through, and is reported.
func TestPrePushAutoLanePasses(t *testing.T) {
	d := newLaneDaemon(t)
	r := newLaneRig(t, d, true)
	r.writeCommit("internal/server/handlers.go", "package server\n", "fix")
	r.writeCommit("internal/server/handlers_test.go", "package server\n", "test")
	r.writeCommit(".itervox/handoff/2026-11-14T10-00-00Z_coder.md", "done\n", "handoff")
	out, code := r.agent("git push origin HEAD")
	require.Zero(t, code, out)
	assert.Equal(t, git(t, r.work, "rev-parse", "HEAD"), r.remoteRef("refs/heads/itervox/eng-1"))
	assert.NotContains(t, out, "needs a human approval")
	assert.Empty(t, d.approvals.byID, "no approval was asked")
	assert.Empty(t, d.laneReports())
	assert.Empty(t, d.eventKinds())

	r.writeCommit("auth/token.go", "package auth\n", "auth change")
	out, code = r.agent("git push origin HEAD")
	require.Zero(t, code, out)
	assert.Equal(t, git(t, r.work, "rev-parse", "HEAD"), r.remoteRef("refs/heads/itervox/eng-1"))
	assert.Equal(t, []LaneReport{{Lane: LaneSecondOpinion, Paths: []string{"auth/token.go"}, PathsTotal: 1}}, d.laneReports())
}

// guardrails.lanes: a new branch is classified against its merge base with
// the issue's base branch, not against everything in its history. The
// base (origin/main) carries a pipeline change the issue branch's commits
// do not make: the agent's code-only push of the new branch passes, and so
// does merging the base in later. A new branch whose own commits change a
// pipeline file is held.
func TestPrePushNewBranchClassifiesAgainstBase(t *testing.T) {
	d := newLaneDaemon(t)
	r := newLaneRig(t, d, false) // no approval channel: a held push is refused at once
	// The base moves on with a pipeline change made by a human.
	git(t, r.work, "checkout", "-q", "main")
	r.writeCommit(".github/workflows/ci.yml", "jobs: {}\n", "human CI change")
	git(t, r.work, "push", "-q", "origin", "main")
	git(t, r.work, "checkout", "-q", "-b", "itervox/eng-1-new", "main~1")

	// The issue branch is new on the remote; its own commit is code only.
	r.writeCommit("main.go", "package main\n", "feature")
	out, code := r.agent("git push origin HEAD:refs/heads/itervox/eng-1")
	require.Zero(t, code, out)
	assert.Equal(t, git(t, r.work, "rev-parse", "HEAD"), r.remoteRef("refs/heads/itervox/eng-1"))

	// Merging the base in brings its pipeline change along: not the agent's.
	git(t, r.work, "merge", "-q", "--no-edit", "origin/main")
	out, code = r.agent("git push origin HEAD:refs/heads/itervox/eng-1")
	require.Zero(t, code, out)
	assert.Equal(t, git(t, r.work, "rev-parse", "HEAD"), r.remoteRef("refs/heads/itervox/eng-1"))
	assert.Empty(t, d.eventKinds())

	// A new branch whose own commit edits a workflow is held.
	other := newLaneRig(t, d, false)
	other.writeCommit(".github/workflows/release.yml", "on: push\n", "release")
	out, code = other.agent("git push -u origin HEAD")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "it changes `.github/workflows/release.yml`")
	assert.Empty(t, other.remoteRef("refs/heads/itervox/eng-1"))
	assert.Equal(t, []string{KindLaneHuman}, d.eventKinds())

	// Unit: with no base to compare with, a new branch is held (fail closed).
	p := Policy{Lanes: testLanes, BaseRefs: []string{"origin/nope"}}
	res := p.classifyPush([]RefUpdate{{LocalRef: "refs/heads/x", LocalSHA: strings.Repeat("1", 40),
		RemoteRef: "refs/heads/x", RemoteSHA: strings.Repeat("0", 40)}}, gitRepo{dir: other.work})
	assert.Equal(t, LaneHuman, res.Lane)
	require.Len(t, res.Human, 1)
	assert.Contains(t, res.Human[0], "changed files unknown for refs/heads/x")
}

// A held push says why, with file and line: the audit finding on the
// pushed workflow and the lane pattern it matched travel in the approval
// request the daemon shows a human.
func TestPrePushHeldPushCarriesReasons(t *testing.T) {
	d := newLaneDaemon(t)
	r := newLaneRig(t, d, true)
	r.writeCommit(".github/workflows/ci.yml", "jobs:\n  test:\n    continue-on-error: true\n", "make CI pass")
	wait := r.startPush()
	a := d.pending(t)
	d.mu.Lock()
	require.NotEmpty(t, d.requests)
	req := d.requests[len(d.requests)-1]
	d.mu.Unlock()
	assert.Equal(t, 2, req.ReasonsTotal)
	assert.Equal(t, []string{
		".github/workflows/ci.yml:3 adds `continue-on-error: true` (failure ignored in a CI workflow (continue-on-error / allow_failure: true))",
		".github/workflows/ci.yml matched .github/** → human",
	}, LaneReasonLines(req.Reasons, req.ReasonsTotal, MaxLaneReasonsShown))
	require.True(t, d.approvals.Decide(a.ID, false, "no"))
	_, code := wait()
	assert.Equal(t, 1, code)
}

func TestLanePathSummary(t *testing.T) {
	assert.Equal(t, "`a`", LanePathSummary([]string{"a"}, 1, 10))
	assert.Equal(t, "`a`, `b` and 3 more", LanePathSummary([]string{"a", "b", "c"}, 5, 2))
}
