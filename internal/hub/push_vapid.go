package hub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// VAPID (RFC 8292) identifies the hub to the browsers' push services. The
// key pair is generated locally the first time push is used and kept in
// StateDir/push_vapid.pem (0600, a PKCS #8 EC key). It never leaves the
// machine: only the public key is served (GET
// /hub/api/v1/push/vapid-public-key) and sent in each request's
// Authorization header.

// VAPIDKeyFileName is the hub's VAPID private key inside StateDir.
const VAPIDKeyFileName = "push_vapid.pem"

// DefaultVAPIDSubject is the contact a push service sees in the JWT's sub.
const DefaultVAPIDSubject = "https://itervox.dev"

// vapidTokenTTL is how long one VAPID JWT is valid (RFC 8292 caps it at 24 h).
const vapidTokenTTL = 12 * time.Hour

// vapidKeys is the hub's VAPID key pair, loaded or created on first use.
type vapidKeys struct {
	path string

	mu   sync.Mutex
	priv *ecdsa.PrivateKey
}

func newVAPIDKeys(path string) *vapidKeys { return &vapidKeys{path: path} }

// key returns the key pair, reading StateDir/push_vapid.pem or creating it
// (0600) the first time. A key file another user can read is tightened to
// 0600; a symlink in its place is refused.
func (v *vapidKeys) key() (*ecdsa.PrivateKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.priv != nil {
		return v.priv, nil
	}
	info, err := os.Lstat(v.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		priv, gerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if gerr != nil {
			return nil, fmt.Errorf("hub: push: generate the VAPID key: %w", gerr)
		}
		der, merr := x509.MarshalPKCS8PrivateKey(priv)
		if merr != nil {
			return nil, fmt.Errorf("hub: push: %w", merr)
		}
		data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if werr := atomicfs.WriteFile(v.path, data, 0o600); werr != nil {
			return nil, fmt.Errorf("hub: push: save the VAPID key: %w", werr)
		}
		v.priv = priv
		return priv, nil
	case err != nil:
		return nil, fmt.Errorf("hub: push: %w", err)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("hub: push: %s is not a regular file (symlink refused)", v.path)
	}
	if info.Mode().Perm() != 0o600 {
		_ = os.Chmod(v.path, 0o600)
	}
	data, err := os.ReadFile(v.path)
	if err != nil {
		return nil, fmt.Errorf("hub: push: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("hub: push: %s is not a PEM private key", v.path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("hub: push: %s: %w", v.path, err)
	}
	priv, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P256() {
		return nil, fmt.Errorf("hub: push: %s is not a P-256 key", v.path)
	}
	v.priv = priv
	return priv, nil
}

// publicKey is the application server key a browser subscribes with: the
// uncompressed P-256 point, base64url without padding.
func (v *vapidKeys) publicKey() (string, error) {
	priv, err := v.key()
	if err != nil {
		return "", err
	}
	pub, err := priv.PublicKey.ECDH()
	if err != nil {
		return "", fmt.Errorf("hub: push: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(pub.Bytes()), nil
}

// authorization is the Authorization header of a push request to endpoint:
// "vapid t=<ES256 JWT for the endpoint's origin>, k=<public key>".
func (v *vapidKeys) authorization(endpoint *url.URL, subject string, now time.Time) (string, error) {
	priv, err := v.key()
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": endpoint.Scheme + "://" + endpoint.Host,
		"exp": now.Add(vapidTokenTTL).Unix(),
		"sub": subject,
	})
	if err != nil {
		return "", fmt.Errorf("hub: push: %w", err)
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", fmt.Errorf("hub: push: sign the VAPID token: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	pub, err := v.publicKey()
	if err != nil {
		return "", err
	}
	return "vapid t=" + signing + "." + base64.RawURLEncoding.EncodeToString(sig) + ", k=" + pub, nil
}
