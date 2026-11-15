package guardrails

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDaemon records the events a shim or hook reports.
type fakeDaemon struct {
	mu     sync.Mutex
	events []Event
	srv    *httptest.Server
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	d := &fakeDaemon{}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/api/v1/agent-actions/ENG-1/guardrail/events" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		var ev Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		d.mu.Lock()
		d.events = append(d.events, ev)
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeDaemon) kinds() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, e := range d.events {
		out = append(out, e.Kind)
	}
	return out
}

// #204: gh pr merge, glab mr merge, a merge through the API and a git merge
// into the default branch are refused for agents; the real programs never
// run, and every refusal is reported to the daemon.
func TestShimRefusesSelfMerge(t *testing.T) {
	d := newFakeDaemon(t)
	r := newRig(t, func(o *Options) { o.DaemonURL, o.Token = d.srv.URL, "tok" })
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	for _, name := range []string{"gh", "glab"} {
		fakeProgram(t, fakes, name, "echo "+name+" \"$@\" >> "+ran+"\n")
	}
	for _, script := range []string{
		"gh pr merge 12 --squash",
		"gh pr merge --auto",
		"glab mr merge 7",
		"gh api -X PUT repos/acme/app/pulls/12/merge",
		"gh api graphql -f query='mutation { mergePullRequest(input: {}) { clientMutationId } }'",
	} {
		out, code := r.agent(script, fakes)
		assert.Equal(t, 1, code, "%s: %s", script, out)
		assert.Contains(t, out, "agents do not merge their own pull requests", script)
	}
	_, err := os.Stat(ran)
	assert.True(t, os.IsNotExist(err), "no real gh/glab ran")

	git(t, r.work, "checkout", "-q", "main")
	mainBefore := git(t, r.work, "rev-parse", "HEAD")
	git(t, r.work, "checkout", "-q", "itervox/eng-1")
	r.commit("feature")
	git(t, r.work, "checkout", "-q", "main")
	out, code := r.agent("git merge itervox/eng-1")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "merging into main is refused")
	assert.Equal(t, mainBefore, git(t, r.work, "rev-parse", "HEAD"), "main did not move")

	assert.Equal(t, []string{KindSelfMerge, KindSelfMerge, KindSelfMerge, KindSelfMerge, KindSelfMerge, KindSelfMerge}, d.kinds())
}

// #204: everything else goes to the real program unchanged: the same
// arguments (spaces, quotes, empty strings), stdin, environment and exit
// status — and a git command runs real git with the hook path in place.
func TestShimForwardsOtherCommandsUnchanged(t *testing.T) {
	r := newRig(t, nil)
	fakes := filepath.Join(r.root, "fakebin")
	out := filepath.Join(r.root, "out")
	fakeProgram(t, fakes, "gh", "for a in \"$@\"; do printf '[%s]\\n' \"$a\"; done > "+out+"\n"+
		"cat >> "+out+"\necho \"env=$GUARD_TEST_VAR\" >> "+out+"\nexit 7\n")

	got, code := r.agent(`echo piped | GUARD_TEST_VAR=kept gh pr view 12 --json 'title,body' "two words" "" --jq '.title'`, fakes)
	assert.Equal(t, 7, code, "the real program's exit status comes back: %s", got)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "[pr]\n[view]\n[12]\n[--json]\n[title,body]\n[two words]\n[]\n[--jq]\n[.title]\npiped\nenv=kept\n", string(data))

	// gh pr create / gh pr list are not merges.
	_, code = r.agent("gh pr create --title x --body y", fakes)
	assert.Equal(t, 7, code)

	// Real git: forwarded, with core.hooksPath pointing at the runtime.
	got, code = r.agent("git config core.hooksPath && git status --short && git log -1 --format=%s")
	require.Zero(t, code, got)
	assert.Equal(t, r.rt.HooksDir()+"\nbase\n", got)

	// The program that is not on PATH at all.
	got, code = r.agent("glab version")
	assert.Equal(t, 127, code, got)
	assert.Contains(t, got, "command not found")
}

func TestShimApprovalUnavailableIsRefused(t *testing.T) {
	d := newFakeDaemon(t)
	r := newRig(t, func(o *Options) { o.DaemonURL, o.Token = d.srv.URL, "tok" }) // ApprovalsEnabled false
	fakes := filepath.Join(r.root, "fakebin")
	ran := filepath.Join(r.root, "ran")
	fakeProgram(t, fakes, "vercel", "echo ran >> "+ran+"\n")
	out, code := r.agent("vercel deploy --prod", fakes)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "needs a human approval")
	_, err := os.Stat(ran)
	assert.True(t, os.IsNotExist(err))
	out, code = r.agent("vercel ls", fakes)
	assert.Zero(t, code, out)
	assert.Equal(t, []string{KindApprovalUnavailable}, d.kinds())
}

func TestShimRefusesHooksPathOverride(t *testing.T) {
	r := newRig(t, nil)
	for _, script := range []string{
		"git -c core.hooksPath=/dev/null push origin HEAD",
		"git -c CORE.HOOKSPATH=/tmp push",
		"git --config-env=core.hooksPath=X push",
	} {
		out, code := r.agent(script)
		assert.Equal(t, 1, code, "%s: %s", script, out)
		assert.Contains(t, out, "replacing or skipping Itervox's git hooks is refused")
	}
}

func TestMatchApproval(t *testing.T) {
	patterns := []string{"vercel --prod", "kubectl apply", "gcloud deploy", "helm upgrade *-prod"}
	cases := []struct {
		cmd  string
		want string
	}{
		{"vercel --prod", "vercel --prod"},
		{"vercel deploy --prod=true", "vercel --prod"},
		{"vercel deploy", ""},
		{"kubectl -n x apply -f k8s/app.yaml", "kubectl apply"},
		{"kubectl get pods", ""},
		{"gcloud run deploy svc", "gcloud deploy"},
		{"helm upgrade app-prod ./chart", "helm upgrade *-prod"},
		{"helm upgrade app-staging ./chart", ""},
	}
	for _, tc := range cases {
		f := strings.Fields(tc.cmd)
		got, _ := MatchApproval(patterns, f[0], f[1:])
		assert.Equal(t, tc.want, got, tc.cmd)
	}
	assert.Equal(t, []string{"gh", "glab", "git", "vercel", "kubectl"}, ShimNames([]string{"vercel --prod", "kubectl apply", "./deploy.sh", "*", "git push"}))
}

func TestIsApproval(t *testing.T) {
	for _, yes := range []string{"approve", "Approved.", "yes, go ahead", "OK", "y"} {
		assert.True(t, IsApproval(yes), yes)
	}
	for _, no := range []string{"", "no", "reject", "not yet, deploy to staging", "maybe"} {
		assert.False(t, IsApproval(no), no)
	}
}

func TestApprovalsRunOnce(t *testing.T) {
	a := NewApprovals()
	req := ApprovalRequest{Key: "k", Command: "vercel --prod"}
	st, created := a.Request("ENG-1", "run1", req)
	require.True(t, created)
	again, created := a.Request("ENG-1", "run1", req)
	assert.False(t, created, "a repeat joins the pending request")
	assert.Equal(t, st.ID, again.ID)
	assert.Equal(t, StatusUnknown, a.Status("ENG-2", "run1", st.ID).Status, "scoped to the issue")
	require.True(t, a.Decide(st.ID, true, "approve"))
	assert.False(t, a.Decide(st.ID, false, "no"), "decided once")
	assert.Equal(t, StatusApproved, a.Status("ENG-1", "run1", st.ID).Status)
	assert.Equal(t, StatusUnknown, a.Status("ENG-1", "run1", st.ID).Status, "an approval is spent by the read that sees it")

	st, _ = a.Request("ENG-1", "run1", req)
	require.True(t, a.Decide(st.ID, true, "yes"))
	joined, created := a.Request("ENG-1", "run1", req)
	assert.False(t, created)
	assert.Equal(t, StatusApproved, joined.Status, "a rerun of the approved command picks the approval up")
	assert.Equal(t, StatusUnknown, a.Status("ENG-1", "run1", st.ID).Status)

	st, _ = a.Request("ENG-1", "run1", req)
	assert.Equal(t, 1, a.Drop("ENG-1"))
	assert.Equal(t, StatusUnknown, a.Status("ENG-1", "run1", st.ID).Status)
}
