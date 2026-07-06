package orchestrator

import (
	"errors"
	"strings"
	"testing"
)

func TestEncodeQueueEnvelope_RoundTripsPayload(t *testing.T) {
	payload := []byte(`{"entries":{},"order":[]}`)
	encoded, err := EncodeQueueEnvelope(payload, "daemon-A")
	if err != nil {
		t.Fatal(err)
	}
	out, reason, err := DecodeQueueEnvelope(encoded, "daemon-A")
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if reason != "" {
		t.Errorf("expected no quarantine reason on round-trip; got %q", reason)
	}
	if string(out) != string(payload) {
		t.Errorf("payload round-trip diverged:\n  got %s\n  want %s", string(out), string(payload))
	}
}

func TestDecodeQueueEnvelope_SchemaVersionMismatchQuarantines(t *testing.T) {
	bad := []byte(`{"schema_version":99,"daemon_instance_id":"x","payload":{},"payload_sha256":""}`)
	_, reason, err := DecodeQueueEnvelope(bad, "x")
	if !errors.Is(err, ErrQueueEnvelopeQuarantined) {
		t.Fatalf("expected ErrQueueEnvelopeQuarantined; got %v", err)
	}
	if reason != "schema_version_mismatch" {
		t.Errorf("reason = %q; want schema_version_mismatch", reason)
	}
}

func TestDecodeQueueEnvelope_PayloadChecksumMismatchQuarantines(t *testing.T) {
	encoded, err := EncodeQueueEnvelope([]byte(`{"a":1}`), "daemon-A")
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with the payload field after encoding.
	tampered := strings.Replace(string(encoded), `"a":1`, `"a":2`, 1)
	_, reason, decodeErr := DecodeQueueEnvelope([]byte(tampered), "daemon-A")
	if !errors.Is(decodeErr, ErrQueueEnvelopeQuarantined) {
		t.Fatalf("expected quarantine error; got %v", decodeErr)
	}
	if reason != "checksum_mismatch" {
		t.Errorf("reason = %q; want checksum_mismatch", reason)
	}
}

func TestDecodeQueueEnvelope_InstanceMismatchWarnsButReturnsPayload(t *testing.T) {
	encoded, err := EncodeQueueEnvelope([]byte(`{"x":1}`), "daemon-A")
	if err != nil {
		t.Fatal(err)
	}
	payload, reason, decodeErr := DecodeQueueEnvelope(encoded, "daemon-B")
	if decodeErr != nil {
		t.Fatalf("instance mismatch must not fail; got %v", decodeErr)
	}
	if reason != "daemon_instance_mismatch" {
		t.Errorf("reason = %q; want daemon_instance_mismatch", reason)
	}
	if string(payload) != `{"x":1}` {
		t.Errorf("payload should still be returned on instance mismatch; got %s", string(payload))
	}
}

func TestDecodeQueueEnvelope_MalformedEnvelopeReportsParseError(t *testing.T) {
	_, _, err := DecodeQueueEnvelope([]byte(`not json`), "daemon-A")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if errors.Is(err, ErrQueueEnvelopeQuarantined) {
		t.Error("malformed JSON should produce parse error, not quarantine sentinel")
	}
}
