package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// QueuePersistenceSchemaVersion is the version stamp written into the
// on-disk queue file envelope (todolist4 A.2). The reader rejects mismatched
// envelopes into the quarantine bucket instead of silently dropping them.
const QueuePersistenceSchemaVersion = 2

// IsQueueEnvelopeShape reports whether data looks like the v2 envelope by
// peeking at the top-level keys. Used by the loader to distinguish a v2 file
// (try DecodeQueueEnvelope) from a legacy v1 raw payload (parse directly into
// automationQueueStateDisk). Tolerates whitespace around the opening brace.
func IsQueueEnvelopeShape(data []byte) bool {
	var peek struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return false
	}
	return peek.SchemaVersion > 0
}

// QueuePersistenceEnvelope is the on-disk envelope wrapping
// AutomationQueueStateDisk so the schema can evolve safely across daemon
// upgrades. Versioned, instance-tagged, and integrity-checked.
type QueuePersistenceEnvelope struct {
	SchemaVersion    int             `json:"schema_version"`
	DaemonInstanceID string          `json:"daemon_instance_id"`
	Payload          json.RawMessage `json:"payload"`
	PayloadSHA256    string          `json:"payload_sha256"`
}

// EncodeQueueEnvelope wraps a raw payload (the marshaled
// automationQueueStateDisk) in the v2 envelope. The sha256 is computed
// before marshaling the envelope so the reader can verify integrity.
func EncodeQueueEnvelope(payload []byte, daemonInstanceID string) ([]byte, error) {
	sum := sha256.Sum256(payload)
	env := QueuePersistenceEnvelope{
		SchemaVersion:    QueuePersistenceSchemaVersion,
		DaemonInstanceID: daemonInstanceID,
		Payload:          json.RawMessage(payload),
		PayloadSHA256:    hex.EncodeToString(sum[:]),
	}
	return json.Marshal(env)
}

// ErrQueueEnvelopeQuarantined reports that a parsed envelope is structurally
// valid but fails integrity, version, or instance checks. Callers should
// move the offending file to a quarantine bucket so the next daemon boot
// does not consume the bad state, while preserving it for forensics.
var ErrQueueEnvelopeQuarantined = errors.New("queue envelope quarantined")

// DecodeQueueEnvelope parses the envelope and returns the inner payload
// bytes only when:
//   - schema_version matches QueuePersistenceSchemaVersion, AND
//   - the sha256 of the payload matches the envelope's payload_sha256.
//
// Mismatch returns ErrQueueEnvelopeQuarantined wrapped with the reason
// detail. The caller decides whether to fail or quarantine.
func DecodeQueueEnvelope(data []byte, expectedInstanceID string) ([]byte, string, error) {
	var env QueuePersistenceEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, "", fmt.Errorf("parse envelope: %w", err)
	}
	if env.SchemaVersion != QueuePersistenceSchemaVersion {
		return nil, "schema_version_mismatch", fmt.Errorf("%w: schema_version %d (want %d)",
			ErrQueueEnvelopeQuarantined, env.SchemaVersion, QueuePersistenceSchemaVersion)
	}
	sum := sha256.Sum256(env.Payload)
	if hex.EncodeToString(sum[:]) != env.PayloadSHA256 {
		return nil, "checksum_mismatch", fmt.Errorf("%w: sha256 mismatch", ErrQueueEnvelopeQuarantined)
	}
	if expectedInstanceID != "" && env.DaemonInstanceID != "" &&
		env.DaemonInstanceID != expectedInstanceID {
		// Daemon-instance mismatch is a warning, not a fatal quarantine — the
		// queue is still recoverable but the operator should be aware that
		// the on-disk state came from a different daemon process.
		return env.Payload, "daemon_instance_mismatch", nil
	}
	return env.Payload, "", nil
}
