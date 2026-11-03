package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCostReportSumsPerIssueAndPerDay (#136): the report sums the ledger's
// tokens and cost per issue and per day.
func TestCostReportSumsPerIssueAndPerDay(t *testing.T) {
	dir := t.TempDir()
	ledger := `{"version":1,
	  "issues":{"ENG-1":{"input_tokens":300,"output_tokens":100,"cost_usd":1.5,"cost_known":true},
	            "ENG-2":{"input_tokens":50,"output_tokens":10}},
	  "days":{"2026-10-10":{"ENG-1":{"input_tokens":100,"output_tokens":40,"cost_usd":0.5,"cost_known":true}},
	          "2026-10-11":{"ENG-1":{"input_tokens":200,"output_tokens":60,"cost_usd":1.0,"cost_known":true},
	                        "ENG-2":{"input_tokens":50,"output_tokens":10}}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "issue_spend.json"), []byte(ledger), 0o644))

	var out, errOut bytes.Buffer
	require.Equal(t, 0, report([]string{"costs", "--logs-dir", dir, "--json"}, &out, &errOut), errOut.String())
	var rep costReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &rep))
	require.Len(t, rep.Issues, 2)
	assert.Equal(t, "ENG-1", rep.Issues[0].Key, "most tokens first")
	assert.Equal(t, 300, rep.Issues[0].InputTokens)
	require.NotNil(t, rep.Issues[0].CostUSD)
	assert.InDelta(t, 1.5, *rep.Issues[0].CostUSD, 1e-9)
	assert.Nil(t, rep.Issues[1].CostUSD, "no cost reported: null, not 0")
	require.Len(t, rep.Days, 2)
	assert.Equal(t, "2026-10-11", rep.Days[0].Key, "newest day first")
	assert.Equal(t, 250, rep.Days[0].InputTokens, "the day sums its issues")
	assert.Equal(t, 70, rep.Days[0].OutputTokens)
	assert.InDelta(t, 1.0, *rep.Days[0].CostUSD, 1e-9)
	assert.Equal(t, 350, rep.Total.InputTokens)
	assert.InDelta(t, 1.5, *rep.Total.CostUSD, 1e-9)

	out.Reset()
	require.Equal(t, 0, report([]string{"costs", "--logs-dir", dir, "--days", "1"}, &out, &errOut))
	text := out.String()
	assert.Contains(t, text, "ENG-1")
	assert.Contains(t, text, "$1.50")
	assert.Contains(t, text, "2026-10-11")
	assert.NotContains(t, text, "2026-10-10", "--days limits the per-day list")
}

func TestCostReportWithNoLedger(t *testing.T) {
	var out, errOut bytes.Buffer
	require.Equal(t, 0, report([]string{"costs", "--logs-dir", t.TempDir()}, &out, &errOut))
	assert.Contains(t, out.String(), "No spend recorded yet")
	assert.Equal(t, 2, report([]string{"spend"}, &out, &errOut))
}
