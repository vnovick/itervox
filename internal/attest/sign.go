package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// KeyFileName and PublicKeyFileName are the signing key pair's files in the
// daemon's project state directory (.itervox/ next to WORKFLOW.md). The
// private key is in agent.IsolationProjectDeny, so a sandboxed agent cannot
// read it; the public key is meant to be shared.
const (
	KeyFileName       = "attest.key"
	PublicKeyFileName = "attest.pub"
)

// signingDomain prefixes every signed message, so an attest signature can
// never be replayed as a signature over anything else.
const signingDomain = "itervox-attest-v1\n"

// ErrBadSignature is returned by [VerifySignature] for a record whose
// signature does not match its content or key.
var ErrBadSignature = errors.New("attest: signature does not verify")

// ErrStaleTree is returned by [Verify] for a record whose tree is not the
// tree it is checked against.
var ErrStaleTree = errors.New("attest: record is for a different tree")

// Signer signs records with an ed25519 key.
type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	id   string
}

// NewSigner returns a Signer for priv.
func NewSigner(priv ed25519.PrivateKey) *Signer {
	pub, _ := priv.Public().(ed25519.PublicKey)
	return &Signer{priv: priv, pub: pub, id: KeyID(pub)}
}

// PublicKey is the key records signed by s verify with.
func (s *Signer) PublicKey() ed25519.PublicKey { return s.pub }

// KeyID names a public key: the first 16 hex digits of its SHA-256.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// Sign sets r's KeyID and signs every other field.
func (s *Signer) Sign(r *Record) error {
	r.KeyID = s.id
	msg, err := signedMessage(*r)
	if err != nil {
		return err
	}
	r.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, msg))
	return nil
}

// signedMessage is what a record's signature covers: the domain prefix and
// the record's JSON with Sig empty. encoding/json writes struct fields in
// declaration order, so a decoded record re-encodes to the same bytes.
func signedMessage(r Record) ([]byte, error) {
	r.Sig = ""
	body, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("attest: encode record: %w", err)
	}
	return append([]byte(signingDomain), body...), nil
}

// VerifySignature checks r's signature against pub.
func VerifySignature(r Record, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("attest: no valid public key")
	}
	if r.Version != RecordVersion {
		return fmt.Errorf("attest: unsupported record version %d", r.Version)
	}
	if r.KeyID != KeyID(pub) {
		return fmt.Errorf("%w: signed by key %q, not %q", ErrBadSignature, r.KeyID, KeyID(pub))
	}
	// Only the canonical encoding Sign writes: base64 decoding ignores the
	// unused low bits of the last character and embedded newlines, so
	// otherwise several Sig strings would verify as one signature.
	sig, err := base64.StdEncoding.DecodeString(r.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sig) != r.Sig {
		return fmt.Errorf("%w: malformed signature", ErrBadSignature)
	}
	msg, err := signedMessage(r)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, msg, sig) {
		return ErrBadSignature
	}
	return nil
}

// Verify checks r's signature against pub and that r ran on the tree
// currentTreeHash. A record with no tree hash never matches.
func Verify(r Record, pub ed25519.PublicKey, currentTreeHash string) error {
	if err := VerifySignature(r, pub); err != nil {
		return err
	}
	if r.TreeHash == "" {
		return fmt.Errorf("%w: the record has no tree hash", ErrStaleTree)
	}
	if r.TreeHash != currentTreeHash {
		return fmt.Errorf("%w: record tree %s, current tree %s", ErrStaleTree, r.TreeHash, currentTreeHash)
	}
	return nil
}

// LoadOrCreateKey reads the private key at path, or generates one and
// writes it (mode 0600, directory 0700 when created) together with the
// public key next to it (PublicKeyFileName, mode 0644).
func LoadOrCreateKey(path string) (*Signer, error) {
	if raw, err := os.ReadFile(path); err == nil {
		s, err := parsePrivateKey(raw)
		if err != nil {
			return nil, err
		}
		pubPath := filepath.Join(filepath.Dir(path), PublicKeyFileName)
		if _, statErr := os.Stat(pubPath); errors.Is(statErr, os.ErrNotExist) {
			if err := WritePublicKey(pubPath, s.pub); err != nil {
				return nil, err
			}
		}
		return s, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("attest: read key: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("attest: generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("attest: encode key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("attest: key dir: %w", err)
	}
	if err := atomicfs.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, fmt.Errorf("attest: write key: %w", err)
	}
	s := NewSigner(priv)
	if err := WritePublicKey(filepath.Join(filepath.Dir(path), PublicKeyFileName), s.pub); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadKey reads the PEM private key at path. Unlike [LoadOrCreateKey] it
// never creates one: a missing key is an error.
func LoadKey(path string) (*Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("attest: read key: %w", err)
	}
	return parsePrivateKey(raw)
}

// WritePublicKey writes pub as a PEM "PUBLIC KEY" file.
func WritePublicKey(path string, pub ed25519.PublicKey) error {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return fmt.Errorf("attest: encode public key: %w", err)
	}
	if err := atomicfs.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		return fmt.Errorf("attest: write public key: %w", err)
	}
	return nil
}

// ReadPublicKey reads a PEM "PUBLIC KEY" file, or derives the public key
// from a PEM "PRIVATE KEY" file.
func ReadPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("attest: read public key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("attest: %s is not a PEM key", path)
	}
	if block.Type == "PRIVATE KEY" {
		s, err := parsePrivateKey(raw)
		if err != nil {
			return nil, err
		}
		return s.pub, nil
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("attest: parse public key: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("attest: public key is not ed25519")
	}
	return pub, nil
}

func parsePrivateKey(raw []byte) (*Signer, error) {
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("attest: key file is not a PEM private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("attest: parse key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("attest: key is not ed25519")
	}
	return NewSigner(priv), nil
}
