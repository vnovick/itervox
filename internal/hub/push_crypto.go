package hub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Web Push message encryption (#231): RFC 8291 over the aes128gcm content
// coding of RFC 8188, on the standard library only (crypto/ecdh,
// crypto/hkdf, AES-GCM). One record: a push message is far below the
// record size, so the payload is the plaintext, the 0x02 last-record
// delimiter and no padding.

// pushRecordSize is the rs of the aes128gcm header (RFC 8291 uses 4096).
const pushRecordSize = 4096

// Lengths fixed by RFC 8291.
const (
	pushAuthSecretLen = 16
	pushSaltLen       = 16
	pushP256PointLen  = 65 // uncompressed point
)

// maxPushPlaintext keeps one record (rs minus the delimiter and the GCM tag).
const maxPushPlaintext = pushRecordSize - 1 - 16

var errPushKeys = errors.New("hub: push: the subscription keys are invalid")

// encryptPush encrypts plaintext for a user agent's public key (p256dh) and
// auth secret, with a fresh ephemeral key and salt.
func encryptPush(plaintext, uaPublic, authSecret []byte) ([]byte, error) {
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("hub: push: %w", err)
	}
	salt := make([]byte, pushSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("hub: push: %w", err)
	}
	return encryptPushWith(plaintext, uaPublic, authSecret, asPriv, salt)
}

// encryptPushWith is encryptPush with a given ephemeral key and salt (the
// RFC 8291 Appendix A test vector fixes both).
func encryptPushWith(plaintext, uaPublic, authSecret []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(authSecret) != pushAuthSecretLen || len(uaPublic) != pushP256PointLen || len(salt) != pushSaltLen {
		return nil, errPushKeys
	}
	if len(plaintext) > maxPushPlaintext {
		return nil, errors.New("hub: push: the payload is too large for one record")
	}
	uaKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, errPushKeys
	}
	ecdhSecret, err := asPriv.ECDH(uaKey)
	if err != nil {
		return nil, errPushKeys
	}
	asPublic := asPriv.PublicKey().Bytes()
	cek, nonce, err := pushContentKeys(ecdhSecret, authSecret, uaPublic, asPublic, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("hub: push: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("hub: push: %w", err)
	}
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02)
	header := make([]byte, 0, pushSaltLen+4+1+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, pushRecordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return gcm.Seal(header, nonce, record, nil), nil
}

// pushContentKeys derives the content encryption key and nonce (RFC 8291
// §3.3-3.4, RFC 8188 §2.2-2.3).
func pushContentKeys(ecdhSecret, authSecret, uaPublic, asPublic, salt []byte) (cek, nonce []byte, err error) {
	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("hub: push: %w", err)
	}
	keyInfo := "WebPush: info\x00" + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("hub: push: %w", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, fmt.Errorf("hub: push: %w", err)
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, fmt.Errorf("hub: push: %w", err)
	}
	if nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12); err != nil {
		return nil, nil, fmt.Errorf("hub: push: %w", err)
	}
	return cek, nonce, nil
}
