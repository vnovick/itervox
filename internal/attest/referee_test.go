package attest

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestRefereeRecordLeavesOtherRecordsUnchanged: a record without a referee
// section encodes with no "referee" key, so records signed before the
// field existed still verify.
func TestRefereeRecordLeavesOtherRecordsUnchanged(t *testing.T) {
	for _, r := range fuzzRecords(t)[:3] {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(line, []byte(`"referee"`)) {
			t.Fatalf("a %s record encodes a referee key: %s", r.Kind, line)
		}
	}
}

// TestVerifyRunChecksRefereeRecords: a referee record verifies by its
// signature alone (it has no tree); a tampered one, a referee kind with no
// referee section and a check record carrying one all fail.
func TestVerifyRunChecksRefereeRecords(t *testing.T) {
	s := fuzzSigner()
	recs := fuzzRecords(t)
	ref := recs[3]
	tampered := ref
	tamperedRef := *ref.Referee
	tamperedRef.Lane = "auto"
	tampered.Referee = &tamperedRef

	noSection := Record{Version: RecordVersion, Kind: KindReferee}
	smuggled := Record{Version: RecordVersion, Kind: KindCheck, Name: "x", TreeHash: recs[2].TreeHash, Referee: &Referee{Command: "lanes"}}
	for _, r := range []*Record{&noSection, &smuggled} {
		if err := s.Sign(r); err != nil {
			t.Fatal(err)
		}
	}
	run := RunFile{Records: append(append([]Record(nil), recs...), tampered, noSection, smuggled)}
	res := VerifyRun(run, s.PublicKey())
	for i := range 4 {
		if !res[i].Verified {
			t.Fatalf("record %d (%s) does not verify: %s", i, recs[i].Kind, res[i].Reason)
		}
	}
	for i, want := range map[int]string{4: "signature does not verify", 5: "without its referee section", 6: "check record with a referee section"} {
		if res[i].Verified || !strings.Contains(res[i].Reason, want) {
			t.Fatalf("record %d: verified=%v reason %q, want a failure with %q", i, res[i].Verified, res[i].Reason, want)
		}
	}

	var out bytes.Buffer
	code := PrintVerificationWith(context.Background(), &out, &out, "test", RunFile{Records: recs}, s.PublicKey(), VerifyOptions{
		Check: func(i int, r Record) string {
			if r.Kind == KindReferee {
				return "content check failed"
			}
			return ""
		},
	})
	if code != 1 || !strings.Contains(out.String(), "FAIL referee lanes") || !strings.Contains(out.String(), "content check failed") {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
}
