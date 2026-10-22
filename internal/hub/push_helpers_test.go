package hub

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// pushRequest is one request the fake push service received.
type pushRequest struct {
	Path   string
	Header http.Header
	Body   []byte
}

// fakePushService is a push service on an httptest TLS server: it records
// every request and answers status (201 by default).
type fakePushService struct {
	ts     *httptest.Server
	status atomic.Int32
	mu     sync.Mutex
	reqs   []pushRequest
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	f := &fakePushService{}
	f.status.Store(http.StatusCreated)
	f.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, pushRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		f.mu.Unlock()
		w.WriteHeader(int(f.status.Load()))
	}))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakePushService) requests() []pushRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushRequest(nil), f.reqs...)
}

// hubConfig points a hub's push channel at the fake (the only endpoint
// host it accepts).
func (f *fakePushService) hubConfig(cfg Config) Config {
	host := strings.TrimPrefix(f.ts.URL, "https://")
	cfg.PushHTTPClient = f.ts.Client()
	cfg.PushEndpointAllowed = func(u *url.URL) bool { return u.Scheme == "https" && u.Host == host }
	return cfg
}

// fakeBrowser is a browser's push subscription: its key pair and auth
// secret, as PushSubscription.toJSON() gives them.
type fakeBrowser struct {
	priv     *ecdh.PrivateKey
	auth     []byte
	endpoint string
}

func newFakeBrowser(t *testing.T, f *fakePushService, id string) *fakeBrowser {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	auth := make([]byte, 16)
	_, err = rand.Read(auth)
	require.NoError(t, err)
	return &fakeBrowser{priv: priv, auth: auth, endpoint: f.ts.URL + "/push/" + id}
}

func (b *fakeBrowser) subscribeBody(name string) string {
	body, _ := json.Marshal(map[string]any{"name": name, "subscription": map[string]any{
		"endpoint": b.endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(b.priv.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(b.auth),
		},
	}})
	return string(body)
}

// subscribe registers b with token and returns the device id.
func (b *fakeBrowser) subscribe(t *testing.T, th *testHub, token, name string) string {
	t.Helper()
	resp := doHub(t, th.ts.URL+"/hub/api/v1/push/subscribe", http.MethodPost, token, strings.NewReader(b.subscribeBody(name)))
	body := readBody(t, resp)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var out struct {
		Device PushDeviceRow `json:"device"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	return out.Device.ID
}

// open decrypts a push request's body as the browser.
func (b *fakeBrowser) open(t *testing.T, r pushRequest) map[string]any {
	t.Helper()
	plain := decryptPushForTest(t, r.Body, b.priv, b.auth)
	var out map[string]any
	require.NoError(t, json.Unmarshal(plain, &out))
	return out
}

// verifyVAPID checks a push request's "vapid t=JWT, k=key" header: the
// ES256 signature over the JWT, against the key it names, and returns the
// JWT claims and the key.
func verifyVAPID(t *testing.T, header string) (claims map[string]any, key string) {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid t=")
	require.True(t, ok, "a vapid Authorization header")
	jwt, key, ok := strings.Cut(rest, ", k=")
	require.True(t, ok)
	raw, err := base64.RawURLEncoding.DecodeString(key)
	require.NoError(t, err)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	require.NoError(t, err)
	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3)
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	require.Len(t, sig, 64)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	require.True(t, ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])), "the VAPID signature verifies")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims, key
}

// pushNotifier is the hub's push channel notifier.
func pushNotifier(t *testing.T, h *Hub) *channelNotifier {
	t.Helper()
	for _, cn := range h.chanNotifiers {
		if cn.ch.Name() == ChannelPush {
			return cn
		}
	}
	t.Fatal("no push channel notifier")
	return nil
}

func notifyCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*1e9)
	t.Cleanup(cancel)
	return ctx
}
