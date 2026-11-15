package schedule

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// cronParityFixture is shared with the dashboard's cron input
// (web/src/lib/cron.ts, web/src/lib/__tests__/cron.test.ts): both sides run
// every case, so the editor never calls valid an expression this parser
// rejects, or the reverse (#211).
type cronParityFixture struct {
	Presets []string `json:"presets"`
	Cases   []struct {
		Expr  string  `json:"expr"`
		Valid bool    `json:"valid"`
		Field *string `json:"field"`
	} `json:"cases"`
}

func loadCronParityFixture(t *testing.T) cronParityFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/cron_parity.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx cronParityFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fx.Cases) == 0 || len(fx.Presets) == 0 {
		t.Fatal("fixture has no cases or presets")
	}
	return fx
}

// fieldPrefixes are the error prefixes Parse puts on a per-field failure.
var fieldPrefixes = []string{"minute: ", "hour: ", "day-of-month: ", "month: ", "day-of-week: "}

func TestParseMatchesDashboardCronParityFixture(t *testing.T) {
	fx := loadCronParityFixture(t)
	for _, tc := range fx.Cases {
		_, err := Parse(tc.Expr)
		if tc.Valid {
			if err != nil {
				t.Errorf("Parse(%q) = %v, fixture says valid", tc.Expr, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("Parse(%q) succeeded, fixture says invalid", tc.Expr)
			continue
		}
		got := ""
		for _, p := range fieldPrefixes {
			if strings.HasPrefix(err.Error(), p) {
				got = strings.TrimSuffix(p, ": ")
			}
		}
		want := ""
		if tc.Field != nil {
			want = *tc.Field
		}
		if got != want {
			t.Errorf("Parse(%q) failed on field %q (%v), fixture says %q", tc.Expr, got, err, want)
		}
	}
}

func TestParseAcceptsEveryDashboardCronPreset(t *testing.T) {
	fx := loadCronParityFixture(t)
	for _, expr := range fx.Presets {
		if _, err := Parse(expr); err != nil {
			t.Errorf("dashboard preset %q rejected: %v", expr, err)
		}
	}
}
