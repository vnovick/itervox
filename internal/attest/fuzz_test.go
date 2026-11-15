package attest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Fuzzing the record parser and verifier. A record's signature covers its
// decoded content (signedMessage re-encodes the struct), not the bytes of
// its JSONL line, so the properties are:
//
//   - arbitrary bytes read as a record file never verify, unless they
//     decode to exactly a record the key signed;
//   - a signed line with one byte changed either fails to read, fails to
//     verify, or decodes to the identical record. The last case is real:
//     encoding/json matches field names case-insensitively and ignores
//     unknown names, so `"Kind"` for `"kind"`, or renaming a key whose
//     value is the zero value anyway, changes no field the signature
//     covers (TestRecordByteFlipsNeverForgeContent counts them).

// fuzzSigner is a fixed test key, derived from a public label so fuzz
// workers and runs agree on it. It signs nothing outside this file.
func fuzzSigner() *Signer {
	seed := sha256.Sum256([]byte("itervox attest fuzz test key (not a secret)"))
	return NewSigner(ed25519.NewKeyFromSeed(seed[:]))
}

// fuzzRecords are the records fuzzSigner signs: a passing check, a
// failing one with output and excludes, and the run's seal.
func fuzzRecords(t testing.TB) []Record {
	s := fuzzSigner()
	start := time.Date(2026, 11, 14, 10, 0, 0, 123456789, time.UTC)
	recs := []Record{
		{Version: RecordVersion, Kind: KindCheck, Issue: "ENG-1", RunID: "20261114T100000Z-abc", Profile: "coder",
			Name: "test", Command: "make test", Argv: []string{"/bin/bash", "-lc", "set -o pipefail\nmake test"},
			Shell: true, Pipefail: true, ExitCode: 0, StartedAt: start, DurationMs: 1234,
			OutputSHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", OutputTail: "ok  \tpkg\t0.1s\n",
			TreeHash: "4b825dc642cb6eb9a060e54bf8d69288fbee4904", Head: "0123456789abcdef0123456789abcdef01234567"},
		{Version: RecordVersion, Kind: KindCheck, Issue: "ENG-1", RunID: "20261114T100000Z-abc", Name: "lint",
			Argv: []string{"golangci-lint", "run"}, ExitCode: 1, StartedAt: start.Add(time.Minute), DurationMs: 0,
			OutputTail: "<main.go:1> \"bad\" & é\n", TreeHash: "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
			TreeExcludes: []string{".itervox/attestations"}},
		{Version: RecordVersion, Kind: KindSeal, Issue: "ENG-1", RunID: "20261114T100000Z-abc",
			StartedAt: start.Add(2 * time.Minute), TreeHash: "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
			TreeExcludes: []string{".itervox/attestations"}},
	}
	for i := range recs {
		if err := s.Sign(&recs[i]); err != nil {
			t.Fatal(err)
		}
	}
	return recs
}

// fuzzLines are fuzzRecords as Store.Append writes them.
func fuzzLines(t testing.TB) [][]byte {
	var out [][]byte
	for _, r := range fuzzRecords(t) {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return out
}

// sameContent reports whether a and b are the same signed record: the
// bytes the signature covers and the signature itself.
func sameContent(t testing.TB, a, b Record) bool {
	ma, err := signedMessage(a)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := signedMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(ma, mb) && a.Sig == b.Sig
}

// readVerified writes data as a record file, reads it back with ReadFile
// (the store's parser) and returns the records whose signature verifies.
func readVerified(t testing.TB, path string, data []byte, pub ed25519.PublicKey) []Record {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadFile(path)
	if err != nil {
		return nil
	}
	var ok []Record
	for _, r := range recs {
		if VerifySignature(r, pub) == nil {
			ok = append(ok, r)
		}
	}
	return ok
}

func FuzzRecordVerify(f *testing.F) {
	lines := fuzzLines(f)
	signed := fuzzRecords(f)
	for _, l := range lines {
		f.Add(l)
		f.Add(append(append([]byte(nil), l...), '\n'))
		f.Add(bytes.Replace(l, []byte(`"exit_code":0`), []byte(`"exit_code":1`), 1))
		f.Add(bytes.Replace(l, []byte(`"kind":"check"`), []byte(`"kind":"seal"`), 1))
	}
	f.Add(bytes.Join(lines, []byte("\n")))
	f.Add([]byte(`{"v":1,"kind":"check","exit_code":0,"started_at":"2026-11-14T10:00:00Z","duration_ms":0,"key_id":"0000000000000000","sig":""}`))
	f.Add([]byte("{}"))
	f.Add([]byte("not json"))
	f.Add([]byte{})
	pub := fuzzSigner().PublicKey()
	path := filepath.Join(f.TempDir(), "run.jsonl")
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, r := range readVerified(t, path, data, pub) {
			found := false
			for _, s := range signed {
				found = found || sameContent(t, r, s)
			}
			if !found {
				t.Fatalf("a record the key never signed verifies: %+v\nfrom %q", r, data)
			}
		}
	})
}

func FuzzRecordByteFlip(f *testing.F) {
	lines := fuzzLines(f)
	signed := fuzzRecords(f)
	for i, l := range lines {
		for _, pos := range []int{0, 2, 5, 10, len(l) / 2, len(l) - 3, len(l) - 1} {
			f.Add(uint8(i), uint16(pos), byte(1))
			f.Add(uint8(i), uint16(pos), byte(0x20))
		}
	}
	pub := fuzzSigner().PublicKey()
	path := filepath.Join(f.TempDir(), "run.jsonl")
	f.Fuzz(func(t *testing.T, which uint8, pos uint16, mask byte) {
		if mask == 0 {
			mask = 1
		}
		i := int(which) % len(lines)
		line := append([]byte(nil), lines[i]...)
		line[int(pos)%len(line)] ^= mask
		for _, r := range readVerified(t, path, line, pub) {
			if !sameContent(t, r, signed[i]) {
				t.Fatalf("flipping byte %d of record %d by %#x forged a verifying record: %+v", int(pos)%len(line), i, mask, r)
			}
			if err := Verify(r, pub, signed[i].TreeHash); err != nil {
				t.Fatalf("a content-identical record fails Verify: %v", err)
			}
		}
	})
}

// TestRecordByteFlipsNeverForgeContent flips every bit of every byte of
// each signed line, and every letter's case: no flip forges a record with
// different content. Some flips do still verify (a key's case, a renamed
// key holding a zero value); each of those decodes to the original record.
func TestRecordByteFlipsNeverForgeContent(t *testing.T) {
	lines := fuzzLines(t)
	signed := fuzzRecords(t)
	pub := fuzzSigner().PublicKey()
	path := filepath.Join(t.TempDir(), "run.jsonl")
	tried, verified := 0, 0
	for i, l := range lines {
		for pos := range l {
			for bit := range 8 {
				line := append([]byte(nil), l...)
				line[pos] ^= 1 << bit
				tried++
				for _, r := range readVerified(t, path, line, pub) {
					verified++
					if !sameContent(t, r, signed[i]) {
						t.Fatalf("flipping bit %d of byte %d of record %d forged %+v", bit, pos, i, r)
					}
				}
			}
		}
	}
	if verified == 0 {
		t.Fatal("expected the case-insensitive field names to let some flips verify; the doc comment above is stale")
	}
	t.Logf("%d single-bit flips: %d still verify, all decoding to the original record", tried, verified)
}

// TestVerifySignatureRejectsNonCanonicalSig: found by
// TestRecordByteFlipsNeverForgeContent. base64 decoding ignores the unused
// low bits of the last character before the padding, so a Sig whose last
// character was changed in those bits decoded to the same signature and
// verified: two different records on disk, one signature. Only the
// encoding Sign writes verifies now.
func TestVerifySignatureRejectsNonCanonicalSig(t *testing.T) {
	r := fuzzRecords(t)[0]
	pub := fuzzSigner().PublicKey()
	if err := VerifySignature(r, pub); err != nil {
		t.Fatalf("the signed record must verify: %v", err)
	}
	// An ed25519 signature is 64 bytes: 86 characters and "==", the last
	// character carrying 4 unused bits.
	last := len(r.Sig) - 3
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	idx := bytes.IndexByte([]byte(alphabet), r.Sig[last])
	for low := range 16 {
		c := alphabet[idx&^0xF|low]
		if c == r.Sig[last] {
			continue
		}
		forged := r
		forged.Sig = r.Sig[:last] + string(c) + r.Sig[last+1:]
		if err := VerifySignature(forged, pub); err == nil {
			t.Fatalf("Sig %q (last character %q for %q) verifies", forged.Sig, c, r.Sig[last])
		}
	}
	for _, sig := range []string{r.Sig[:40] + "\n" + r.Sig[40:], r.Sig + "\r\n"} {
		forged := r
		forged.Sig = sig
		if err := VerifySignature(forged, pub); err == nil {
			t.Fatalf("Sig with an embedded newline verifies: %q", sig)
		}
	}
}
