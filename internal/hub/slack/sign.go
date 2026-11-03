package slack

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Signed action values. A message's buttons and reply box carry a value
// the hub signs with its own key, never a raw project or issue name: Slack
// echoes the value back verbatim, and anyone who can post to the Socket
// Mode connection's app (or replay an old payload) controls it. Verifying
// it binds the action to the project, member, issue and run it was issued
// for, and to the kind of request (a reply or an approval).

// keyLen is the signing key's size.
const keyLen = 32

// macLen is the truncated HMAC-SHA256 tag length (128 bits).
const macLen = 16

// valuePrefix versions the value format.
const valuePrefix = "iv1."

// Action kinds a signed value carries.
const (
	kindReply    = "r"
	kindApproval = "a"
)

// action is a verified signed value.
type action struct {
	Kind       string
	Project    string
	MemberKey  string
	Identifier string
	RunID      string
	Expires    time.Time
}

var errBadValue = errors.New("slack: the action value is not one this hub signed, or it expired")

// sign returns the signed value of a.
func (c *Channel) sign(a action) (string, error) {
	fields := []string{a.Kind, a.Project, a.MemberKey, a.Identifier, a.RunID, strconv.FormatInt(a.Expires.Unix(), 36)}
	for _, f := range fields {
		if strings.ContainsAny(f, "\n\r") {
			return "", errors.New("slack: an action field has a line break")
		}
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, "\n")))
	return valuePrefix + payload + "." + base64.RawURLEncoding.EncodeToString(c.mac(payload)), nil
}

func (c *Channel) mac(payload string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(valuePrefix + payload))
	return m.Sum(nil)[:macLen]
}

// verify checks a value's signature and expiry and returns its action.
func (c *Channel) verify(value string) (action, error) {
	rest, ok := strings.CutPrefix(value, valuePrefix)
	if !ok || len(value) > 2000 {
		return action{}, errBadValue
	}
	payload, tag, ok := strings.Cut(rest, ".")
	if !ok {
		return action{}, errBadValue
	}
	got, err := base64.RawURLEncoding.DecodeString(tag)
	if err != nil || !hmac.Equal(got, c.mac(payload)) {
		return action{}, errBadValue
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return action{}, errBadValue
	}
	f := strings.Split(string(raw), "\n")
	if len(f) != 6 {
		return action{}, errBadValue
	}
	exp, err := strconv.ParseInt(f[5], 36, 64)
	if err != nil {
		return action{}, errBadValue
	}
	a := action{Kind: f[0], Project: f[1], MemberKey: f[2], Identifier: f[3], RunID: f[4], Expires: time.Unix(exp, 0)}
	if (a.Kind != kindReply && a.Kind != kindApproval) || a.Project == "" || a.Identifier == "" || !c.now().Before(a.Expires) {
		return action{}, errBadValue
	}
	return a, nil
}

// loadKey reads the signing key at path, creating it (0600, in a 0700
// directory) on first use. A key of the wrong size is refused rather than
// replaced, so a damaged file never silently invalidates every message.
func loadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if derr != nil || len(key) != keyLen {
			return nil, fmt.Errorf("slack: the signing key %s is damaged; remove it to create a new one (open Slack messages stop working)", path)
		}
		return key, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("slack: read the signing key: %w", err)
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("slack: create the signing key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("slack: create the signing key: %w", err)
	}
	if err := atomicfs.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("slack: create the signing key: %w", err)
	}
	return key, nil
}
