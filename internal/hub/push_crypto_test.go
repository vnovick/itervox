package hub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func b64u(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	require.NoError(t, err)
	return b
}

// TestPushEncryptionRFC8291Vector encrypts RFC 8291 Appendix A's message
// with its keys and salt and must produce its exact ciphertext; decrypting
// it as the user agent returns the message.
func TestPushEncryptionRFC8291Vector(t *testing.T) {
	plaintext := []byte("When I grow up, I want to be a watermelon")
	asPriv, err := ecdh.P256().NewPrivateKey(b64u(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	require.NoError(t, err)
	require.Equal(t, b64u(t, "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"),
		asPriv.PublicKey().Bytes(), "the vector's application server key pair")
	uaPublic := b64u(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := b64u(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := b64u(t, "DGv6ra1nlYgDCS1FRnbzlw")

	got, err := encryptPushWith(plaintext, uaPublic, auth, asPriv, salt)
	require.NoError(t, err)
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	require.Equal(t, want, base64.RawURLEncoding.EncodeToString(got))

	uaPriv, err := ecdh.P256().NewPrivateKey(b64u(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	require.NoError(t, err)
	require.Equal(t, plaintext, decryptPushForTest(t, got, uaPriv, auth))
}

// decryptPushForTest is the user agent's side of RFC 8291 (one record).
func decryptPushForTest(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	require.Greater(t, len(body), pushSaltLen+5)
	salt := body[:pushSaltLen]
	require.Equal(t, uint32(pushRecordSize), binary.BigEndian.Uint32(body[pushSaltLen:pushSaltLen+4]))
	idLen := int(body[pushSaltLen+4])
	asPublic := body[pushSaltLen+5 : pushSaltLen+5+idLen]
	ciphertext := body[pushSaltLen+5+idLen:]
	asKey, err := ecdh.P256().NewPublicKey(asPublic)
	require.NoError(t, err)
	secret, err := uaPriv.ECDH(asKey)
	require.NoError(t, err)
	cek, nonce, err := pushContentKeys(secret, auth, uaPriv.PublicKey().Bytes(), asPublic, salt)
	require.NoError(t, err)
	block, err := aes.NewCipher(cek)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	record, err := gcm.Open(nil, nonce, ciphertext, nil)
	require.NoError(t, err)
	require.NotEmpty(t, record)
	require.Equal(t, byte(0x02), record[len(record)-1], "a single, last record")
	return record[:len(record)-1]
}
