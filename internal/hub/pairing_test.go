package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/server"
)

// issuePairing asks the hub for a pairing code with token.
func issuePairing(t *testing.T, th *testHub, token string) (int, PairingResponse) {
	t.Helper()
	resp := doHub(t, th.ts.URL+"/hub/api/v1/pairing", http.MethodPost, token, nil)
	var out PairingResponse
	if resp.StatusCode == http.StatusCreated {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	}
	return resp.StatusCode, out
}

// exchangePairing trades a code for a device token (no Authorization).
func exchangePairing(t *testing.T, th *testHub, code, name string) (int, string, DeviceRow) {
	t.Helper()
	body, _ := json.Marshal(PairingExchangeRequest{Code: code, Name: name})
	resp := doHub(t, th.ts.URL+"/hub/api/v1/pairing/exchange", http.MethodPost, "", strings.NewReader(string(body)))
	var out struct {
		Token  string    `json:"token"`
		Device DeviceRow `json:"device"`
	}
	if resp.StatusCode == http.StatusCreated {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	}
	return resp.StatusCode, out.Token, out.Device
}

func whoami(t *testing.T, th *testHub, token string) (int, Principal) {
	t.Helper()
	resp := doHub(t, th.ts.URL+"/hub/api/v1/whoami", http.MethodGet, token, nil)
	var out struct {
		Principal Principal `json:"principal"`
	}
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	}
	return resp.StatusCode, out.Principal
}

// TestPairingCodeIsOneTimeAndExpires (#231): only an admin issues pairing
// codes; a code exchanged once yields a device-scoped operator token and
// never works again; a code older than ten minutes is refused; and a run
// of wrong codes is throttled.
func TestPairingCodeIsOneTimeAndExpires(t *testing.T) {
	th := startTestHub(t, Config{})
	clock := &fakeClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	th.h.pairing.now = clock.now

	status, _ := issuePairing(t, th, "")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = issuePairing(t, th, th.secrets[apitokens.ScopeOperator])
	assert.Equal(t, http.StatusForbidden, status, "pairing mints a token: admin only")

	status, p := issuePairing(t, th, th.secrets[apitokens.ScopeAdmin])
	require.Equal(t, http.StatusCreated, status)
	assert.Len(t, p.Code, 32, "160 random bits")
	assert.Equal(t, "/hub/pair#code="+p.Code, p.Path, "the code rides in the fragment")
	assert.Equal(t, clock.now().Add(PairingCodeTTL), p.ExpiresAt)
	assert.Equal(t, 10*time.Minute, PairingCodeTTL)

	status, token, dev := exchangePairing(t, th, p.Code, "Vlad's phone")
	require.Equal(t, http.StatusCreated, status)
	require.NotEmpty(t, token)
	assert.Equal(t, "Vlad's phone", dev.Name)
	code, who := whoami(t, th, token)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, apitokens.ScopeOperator, who.Scope, "a device token is operator scope")
	assert.Equal(t, DeviceTokenPrefix+"Vlad's phone", who.Name)
	assert.Equal(t, dev.ID, who.TokenID)

	status, _, _ = exchangePairing(t, th, p.Code, "Another phone")
	assert.Equal(t, http.StatusBadRequest, status, "a code works once")

	_, p2 := issuePairing(t, th, th.secrets[apitokens.ScopeAdmin])
	clock.advance(PairingCodeTTL - time.Second)
	_, p3 := issuePairing(t, th, th.secrets[apitokens.ScopeAdmin])
	clock.advance(time.Second)
	status, _, _ = exchangePairing(t, th, p2.Code, "Late phone")
	assert.Equal(t, http.StatusBadRequest, status, "a code expires after ten minutes")
	status, _, _ = exchangePairing(t, th, p3.Code, "")
	assert.Equal(t, http.StatusBadRequest, status, "a device needs a name")
	status, _, _ = exchangePairing(t, th, p3.Code, "On-time phone")
	assert.Equal(t, http.StatusCreated, status, "a bad request does not spend the code")

	clock.advance(pairingFailureWindow + time.Second) // forget the two failures above
	for range maxPairingFailures {
		status, _, _ = exchangePairing(t, th, "WRONGCODEWRONGCODEWRONGCODEWRONG", "x")
		assert.Equal(t, http.StatusBadRequest, status)
	}
	_, p4 := issuePairing(t, th, th.secrets[apitokens.ScopeAdmin])
	status, _, _ = exchangePairing(t, th, p4.Code, "Phone")
	assert.Equal(t, http.StatusTooManyRequests, status, "wrong codes are throttled")
	clock.advance(pairingFailureWindow + time.Second)
	_, p5 := issuePairing(t, th, th.secrets[apitokens.ScopeAdmin])
	status, _, _ = exchangePairing(t, th, p5.Code, "Phone")
	assert.Equal(t, http.StatusCreated, status, "until the window passes")
}

// TestDeviceTokenRevocation (#231): an admin lists the paired devices and
// revokes one: its token stops working at once and the push subscriptions
// made with it are removed (no more pushes to that phone). Only paired
// devices can be revoked through this route.
func TestDeviceTokenRevocation(t *testing.T) {
	f := newFakePushService(t)
	th := startTestHub(t, f.hubConfig(Config{}))
	_, p := issuePairing(t, th, th.secrets[apitokens.ScopeAdmin])
	status, token, dev := exchangePairing(t, th, p.Code, "Phone")
	require.Equal(t, http.StatusCreated, status)
	b := newFakeBrowser(t, f, "phone")
	pushID := b.subscribe(t, th, token, "Phone")
	other := newFakeBrowser(t, f, "laptop")
	other.subscribe(t, th, th.secrets[apitokens.ScopeOperator], "Laptop")

	resp := doHub(t, th.ts.URL+"/hub/api/v1/devices", http.MethodGet, token, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "listing devices is admin")
	resp = doHub(t, th.ts.URL+"/hub/api/v1/devices", http.MethodGet, th.secrets[apitokens.ScopeAdmin], nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list struct {
		Devices []DeviceRow `json:"devices"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	require.Len(t, list.Devices, 1, "only paired devices, not the other hub tokens")
	assert.Equal(t, dev.ID, list.Devices[0].ID)
	assert.Equal(t, 1, list.Devices[0].PushDevices)

	resp = doHub(t, th.ts.URL+"/hub/api/v1/push/devices", http.MethodGet, token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, readBody(t, resp), f.ts.URL+"/push/", "endpoints are never listed")

	resp = doHub(t, th.ts.URL+"/hub/api/v1/devices/"+dev.ID, http.MethodDelete, token, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	adminID := ""
	if _, who := whoami(t, th, th.secrets[apitokens.ScopeAdmin]); who.TokenID != "" {
		adminID = who.TokenID
	}
	resp = doHub(t, th.ts.URL+"/hub/api/v1/devices/"+adminID, http.MethodDelete, th.secrets[apitokens.ScopeAdmin], nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "a non-device token is not revoked here")

	resp = doHub(t, th.ts.URL+"/hub/api/v1/devices/"+dev.ID, http.MethodDelete, th.secrets[apitokens.ScopeAdmin], nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	status, _ = whoami(t, th, token)
	assert.Equal(t, http.StatusUnauthorized, status, "the device token stops working at once")
	for _, d := range th.h.push.store.list() {
		assert.NotEqual(t, pushID, d.ID, "the device's push subscription is gone")
	}
	require.NoError(t, th.h.push.Send(notifyCtx(t), Notification{Kind: KindInputRequired, Project: "alpha", Identifier: "A-1", Title: "A-1 needs input"}))
	reqs := f.requests()
	require.Len(t, reqs, 1, "only the laptop gets the push")
	assert.Equal(t, "/push/laptop", reqs[0].Path)
	resp = doHub(t, th.ts.URL+"/hub/api/v1/devices/"+dev.ID, http.MethodDelete, th.secrets[apitokens.ScopeAdmin], nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// A device token revoked elsewhere (`itervox token --hub revoke`) takes
	// its pushes along at the next send.
	_, p2 := issuePairing(t, th, th.secrets[apitokens.ScopeAdmin])
	_, token2, dev2 := exchangePairing(t, th, p2.Code, "Tablet")
	newFakeBrowser(t, f, "tablet").subscribe(t, th, token2, "Tablet")
	_, err := th.h.tokens.Revoke(dev2.ID, "test")
	require.NoError(t, err)
	require.NoError(t, th.h.push.Send(notifyCtx(t), Notification{Kind: KindInputRequired, Project: "alpha", Identifier: "A-2", Title: "A-2 needs input"}))
	reqs = f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "/push/laptop", reqs[1].Path)
	assert.Len(t, th.h.push.store.list(), 1)

	// An operator may revoke a push subscription by id.
	rows := th.h.push.store.list()
	resp = doHub(t, th.ts.URL+"/hub/api/v1/push/devices/"+rows[0].ID, http.MethodDelete, th.secrets[apitokens.ScopeOperator], nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Empty(t, th.h.push.store.list())
}

// TestAllowedOriginsCORS (#231): server.allowed_origins on the hub and on a
// daemon — a listed origin gets CORS headers for itself (allowed), any
// other origin gets none (denied), and a preflight is answered for a listed
// origin only, refused for any other. The
// default is same-origin only. (A browser sends the Authorization header
// cross-origin only after a successful preflight, so a refused preflight
// is what keeps an unlisted page out.)
func TestAllowedOriginsCORS(t *testing.T) {
	const allowed = "https://dash.example.com"
	const evil = "https://evil.example"
	th := startTestHub(t, Config{AllowedOrigins: []string{allowed}})
	daemon := server.New(server.Config{
		Snapshot:       func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:    make(chan struct{}, 1),
		APIToken:       memberBootstrap,
		AllowedOrigins: []string{allowed},
	})
	plain := startTestHub(t, Config{})

	type target struct {
		name    string
		handler http.Handler
		path    string
		token   string
	}
	targets := []target{
		{"hub", th.h.Handler(), "/hub/api/v1/whoami", th.secrets[apitokens.ScopeViewer]},
		{"daemon", daemon, "/api/v1/whoami", memberBootstrap},
	}
	do := func(h http.Handler, method, path, token string, hdr map[string]string) *httptest.ResponseRecorder {
		req, _ := http.NewRequest(method, "http://hub.local:8089"+path, nil)
		req.Host = "hub.local:8089"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, tg := range targets {
		t.Run(tg.name+"/allowed", func(t *testing.T) {
			rec := do(tg.handler, http.MethodGet, tg.path, tg.token, map[string]string{"Origin": allowed, "Sec-Fetch-Site": "cross-site"})
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, allowed, rec.Header().Get("Access-Control-Allow-Origin"))
			assert.Contains(t, rec.Header().Values("Vary"), "Origin")
			assert.Contains(t, rec.Header().Get("Access-Control-Expose-Headers"), RequiredScopeHeader)
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"), "bearer tokens, never cookies")
		})
		t.Run(tg.name+"/denied", func(t *testing.T) {
			rec := do(tg.handler, http.MethodGet, tg.path, tg.token, map[string]string{"Origin": evil, "Sec-Fetch-Site": "cross-site"})
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "no CORS header for an unlisted origin: the browser hides the answer")
			assert.Empty(t, rec.Header().Get("Access-Control-Expose-Headers"))
			assert.Contains(t, rec.Header().Values("Vary"), "Origin")
			rec = do(tg.handler, http.MethodGet, tg.path, "", map[string]string{"Origin": evil, "Sec-Fetch-Site": "cross-site"})
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "and without the preflight it cannot send a token")
			rec = do(tg.handler, http.MethodGet, tg.path, tg.token, map[string]string{"Origin": "http://hub.local:8089", "Sec-Fetch-Site": "same-origin"})
			assert.Equal(t, http.StatusOK, rec.Code, "same-origin is untouched")
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
		})
		t.Run(tg.name+"/preflight", func(t *testing.T) {
			pre := map[string]string{"Origin": allowed, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization,content-type"}
			rec := do(tg.handler, http.MethodOptions, tg.path, "", pre)
			assert.Equal(t, http.StatusNoContent, rec.Code, "answered before auth: a preflight has no token")
			assert.Equal(t, allowed, rec.Header().Get("Access-Control-Allow-Origin"))
			assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "POST")
			assert.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Authorization")
			assert.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Last-Event-ID")
			pre["Origin"] = evil
			rec = do(tg.handler, http.MethodOptions, tg.path, "", pre)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
		})
	}
	t.Run("default is same-origin only", func(t *testing.T) {
		rec := do(plain.h.Handler(), http.MethodOptions, "/hub/api/v1/whoami", "", map[string]string{"Origin": allowed, "Access-Control-Request-Method": "GET"})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})
}
