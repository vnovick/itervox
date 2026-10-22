package hub

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// TestVAPIDKeysGeneratedOnceAndPrivate (#231): the hub's VAPID key pair is
// created on first use only, stored 0600 (a wider file is tightened), the
// same key is served by every later call and by a hub restarted over the
// same state directory, only the public key ever leaves the hub, and push
// requests are signed with it.
func TestVAPIDKeysGeneratedOnceAndPrivate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "hub")
	keyPath := filepath.Join(stateDir, VAPIDKeyFileName)
	th := startTestHub(t, Config{StateDir: stateDir})
	_, err := os.Stat(keyPath)
	require.True(t, os.IsNotExist(err), "no key before push is first used")

	getKey := func(th *testHub) string {
		t.Helper()
		resp := doHub(t, th.ts.URL+"/hub/api/v1/push/vapid-public-key", http.MethodGet, th.secrets[apitokens.ScopeViewer], nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var out map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		require.Len(t, out, 1, "the public key only")
		return out["publicKey"]
	}
	resp := doHub(t, th.ts.URL+"/hub/api/v1/push/vapid-public-key", http.MethodGet, "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	first := getKey(th)
	raw, err := base64.RawURLEncoding.DecodeString(first)
	require.NoError(t, err)
	require.Len(t, raw, 65, "an uncompressed P-256 point")
	require.Equal(t, byte(0x04), raw[0])

	info, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the private key is 0600")
	keyFile, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, first, getKey(th), "generated once")

	// A restarted hub reuses the file; a file another user could read is
	// tightened back to 0600.
	th.close()
	require.NoError(t, os.Chmod(keyPath, 0o644))
	th2 := startTestHub(t, Config{StateDir: stateDir})
	assert.Equal(t, first, getKey(th2), "the same key after a restart")
	info, err = os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	after, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	assert.Equal(t, keyFile, after, "never regenerated")

	// Push requests carry the public key and a JWT it verifies.
	f := newFakePushService(t)
	th3 := startTestHub(t, f.hubConfig(Config{StateDir: stateDir}))
	b := newFakeBrowser(t, f, "dev1")
	b.subscribe(t, th3, th3.secrets[apitokens.ScopeOperator], "Phone")
	require.NoError(t, th3.h.push.Send(notifyCtx(t), Notification{Kind: KindInputRequired, Project: "alpha", Identifier: "ENG-1", Title: "ENG-1 needs input", Urgent: true}))
	reqs := f.requests()
	require.Len(t, reqs, 1)
	claims, key := verifyVAPID(t, reqs[0].Header.Get("Authorization"))
	assert.Equal(t, first, key)
	assert.Equal(t, f.ts.URL, claims["aud"], "the audience is the push service's origin")
	assert.Equal(t, DefaultVAPIDSubject, claims["sub"])
	for _, r := range reqs {
		assert.NotContains(t, string(r.Body), string(keyFile))
		for _, v := range r.Header {
			assert.NotContains(t, strings.Join(v, " "), "PRIVATE KEY")
		}
	}
}

// TestPushPayloadIsMinimalAndRedacted (#231): what a push carries, once
// decrypted by the browser (RFC 8291), is exactly kind, project, issue, a
// short redacted title and a deep link: never the body, a digest's items, a
// run id or anything a secret could hide in.
func TestPushPayloadIsMinimalAndRedacted(t *testing.T) {
	f := newFakePushService(t)
	th := startTestHub(t, f.hubConfig(Config{}))
	b := newFakeBrowser(t, f, "dev1")
	b.subscribe(t, th, th.secrets[apitokens.ScopeOperator], "Phone")

	secret := "it" + "x_" + strings.Repeat("Ab3dE", 8)
	agentOutput := "AGENT OUTPUT: wrote /etc/passwd and printed the env"
	n := Notification{
		Channel: ChannelPush, Kind: KindInputRequired, Urgent: true, Project: "alpha", Identifier: "ENG-1",
		Title: "ENG-1 needs input " + secret + " " + strings.Repeat("very long title ", 20),
		Body:  agentOutput, URL: "/hub/p/alpha/?issue=ENG-1", RunID: "run-123", Seq: 42,
		Items: []Notification{{Title: agentOutput}}, TS: time.Now(),
	}
	require.NoError(t, th.h.push.Send(notifyCtx(t), n))
	reqs := f.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, "aes128gcm", r.Header.Get("Content-Encoding"))
	assert.Equal(t, "high", r.Header.Get("Urgency"), "an urgent kind")
	assert.NotEmpty(t, r.Header.Get("TTL"))
	assert.NotContains(t, string(r.Body), "ENG-1", "the body on the wire is encrypted")

	got := b.open(t, r)
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"kind", "project", "issue", "title", "url"}, keys, "the payload is minimal")
	assert.Equal(t, KindInputRequired, got["kind"])
	assert.Equal(t, "alpha", got["project"])
	assert.Equal(t, "ENG-1", got["issue"])
	assert.Equal(t, "/hub/p/alpha/m/ENG-1", got["url"], "a deep link into the mobile issue page")
	title, _ := got["title"].(string)
	assert.LessOrEqual(t, utf8.RuneCountInString(title), maxPushTitleRunes, "a short title")
	assert.True(t, strings.HasPrefix(title, "ENG-1 needs input"))
	plain := string(decryptPushForTest(t, r.Body, b.priv, b.auth))
	assert.NotContains(t, plain, secret, "redacted")
	assert.NotContains(t, plain, "itx_")
	assert.NotContains(t, plain, "AGENT OUTPUT")
	assert.NotContains(t, plain, "run-123")

	// A digest carries its count and a link, never its items.
	require.NoError(t, th.h.push.Send(notifyCtx(t), digestNotification(ChannelPush, []Notification{
		{Project: "alpha", Title: agentOutput}, {Project: "beta", Title: agentOutput},
	}, time.Now())))
	reqs = f.requests()
	require.Len(t, reqs, 2)
	digest := b.open(t, reqs[1])
	assert.Equal(t, NotificationDigest, digest["kind"])
	assert.Equal(t, "2 updates", digest["title"])
	assert.Equal(t, "/hub/m", digest["url"])
	assert.Equal(t, "normal", reqs[1].Header.Get("Urgency"))
	assert.NotContains(t, string(decryptPushForTest(t, reqs[1].Body, b.priv, b.auth)), "AGENT OUTPUT")
}

// TestPushRespectsNotificationSettings (#231): the push channel is routed by
// NotificationSettings.Route like every channel — a kind off, a project
// routed elsewhere, a snoozed project or the push channel switched off
// sends nothing; and a subscription the push service reports gone is
// removed.
func TestPushRespectsNotificationSettings(t *testing.T) {
	f := newFakePushService(t)
	th := startTestHub(t, f.hubConfig(Config{}))
	b := newFakeBrowser(t, f, "dev1")
	b.subscribe(t, th, th.secrets[apitokens.ScopeOperator], "Phone")
	cn := pushNotifier(t, th.h)

	put := func(body string) {
		t.Helper()
		resp := doHub(t, th.ts.URL+"/hub/api/v1/notifications", http.MethodPut, th.secrets[apitokens.ScopeAdmin], strings.NewReader(body))
		require.Equal(t, http.StatusOK, resp.StatusCode, readBody(t, resp))
	}
	put(`{"projects": {
		"beta":  {"snooze_until": "2099-01-01T00:00:00Z"},
		"gamma": {"routes": {"input_required": ["desktop"]}}
	}}`)
	ev := func(project, kind, id string) Event {
		return Event{Project: project, Kind: kind, Identifier: id, TS: time.Now()}
	}
	for _, e := range []Event{
		ev("alpha", "input_required", "A-1"), // default route: push
		ev("beta", "input_required", "B-1"),  // snoozed
		ev("gamma", "input_required", "G-1"), // routed to desktop only
		ev("alpha", "pr_merged", "A-2"),      // off by default
		ev("alpha", "run_started", "A-3"),    // notifies nobody
	} {
		require.NoError(t, cn.Notify(notifyCtx(t), e))
	}
	reqs := f.requests()
	require.Len(t, reqs, 1, "only alpha's input request is routed to push")
	assert.Equal(t, "A-1", b.open(t, reqs[0])["issue"])
	assert.False(t, cn.Enabled("beta", "input_required"), "snoozed")

	put(`{"channels": {"push": {"enabled": false}}}`)
	require.NoError(t, cn.Notify(notifyCtx(t), ev("alpha", "input_required", "A-4")))
	assert.Len(t, f.requests(), 1, "the push channel is switched off")

	put(`{}`)
	f.status.Store(http.StatusGone)
	require.NoError(t, cn.Notify(notifyCtx(t), ev("alpha", "input_required", "A-5")))
	assert.Len(t, f.requests(), 2)
	assert.Empty(t, th.h.push.store.list(), "a gone subscription is removed")
}
