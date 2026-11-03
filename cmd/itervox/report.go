package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/vnovick/itervox/internal/orchestrator"
)

// `itervox report costs` (#136) prints the spend ledger the daemon keeps in
// <logs-dir>/issue_spend.json: tokens and estimated cost per issue and per
// day.
//
//	itervox report costs [--workflow WORKFLOW.md] [--logs-dir DIR] [--days N] [--json]

const reportUsage = "usage: itervox report costs [--workflow WORKFLOW.md] [--logs-dir DIR] [--days N] [--json]"

func runReport(args []string) {
	fatalExit(report(args, os.Stdout, os.Stderr))
}

// report runs `itervox report` and returns the exit code.
func report(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] != "costs" {
		_, _ = fmt.Fprintln(errOut, reportUsage)
		return 2
	}
	fs := flag.NewFlagSet("report costs", flag.ContinueOnError)
	fs.SetOutput(errOut)
	workflow := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	logsDir := fs.String("logs-dir", "", "the daemon's logs directory (default: the per-project dir under ~/.itervox/logs)")
	days := fs.Int("days", 14, "days to list per day (0: all kept, up to 90)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		_, _ = fmt.Fprintln(errOut, reportUsage)
		return 2
	}
	dir := *logsDir
	if dir == "" {
		dir = defaultLogsDir(*workflow)
	}
	ledger, err := orchestrator.ReadSpendFile(filepath.Join(dir, "issue_spend.json"))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox report costs: %v\n", err)
		return 1
	}
	rep := buildCostReport(ledger, *days)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return 1
		}
		return 0
	}
	writeCostReport(out, rep)
	return 0
}

// costRow is one line of the cost report.
type costRow struct {
	Key          string   `json:"key"` // an identifier, or a day
	InputTokens  int      `json:"inputTokens"`
	OutputTokens int      `json:"outputTokens"`
	CostUSD      *float64 `json:"costUsd"` // null when no run reported a cost
}

type costReport struct {
	Issues []costRow `json:"issues"`
	Days   []costRow `json:"days"`
	Total  costRow   `json:"total"`
}

func toCostRow(key string, s orchestrator.Spend) costRow {
	r := costRow{Key: key, InputTokens: s.InputTokens, OutputTokens: s.OutputTokens}
	if s.CostKnown {
		c := s.CostUSD
		r.CostUSD = &c
	}
	return r
}

// buildCostReport sums the ledger per issue (most tokens first) and per day
// (newest first, the last `days` days that have spend).
func buildCostReport(l orchestrator.SpendLedger, days int) costReport {
	var rep costReport
	var total orchestrator.Spend
	for ident, s := range l.Issues {
		rep.Issues = append(rep.Issues, toCostRow(ident, s))
		total.Add(s)
	}
	sort.Slice(rep.Issues, func(i, j int) bool {
		a, b := rep.Issues[i], rep.Issues[j]
		if ta, tb := a.InputTokens+a.OutputTokens, b.InputTokens+b.OutputTokens; ta != tb {
			return ta > tb
		}
		return a.Key < b.Key
	})
	keys := make([]string, 0, len(l.Days))
	for d := range l.Days {
		keys = append(keys, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	if days > 0 && len(keys) > days {
		keys = keys[:days]
	}
	for _, d := range keys {
		var s orchestrator.Spend
		for _, is := range l.Days[d] {
			s.Add(is)
		}
		rep.Days = append(rep.Days, toCostRow(d, s))
	}
	rep.Total = toCostRow("total", total)
	if rep.Issues == nil {
		rep.Issues = []costRow{}
	}
	if rep.Days == nil {
		rep.Days = []costRow{}
	}
	return rep
}

func formatCost(c *float64) string {
	if c == nil {
		return "—"
	}
	return fmt.Sprintf("$%.2f", *c)
}

func writeCostReport(out io.Writer, rep costReport) {
	if len(rep.Issues) == 0 {
		_, _ = fmt.Fprintln(out, "No spend recorded yet (the daemon records it as agents run).")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', tabwriter.AlignRight)
	section := func(title string, rows []costRow) {
		_, _ = fmt.Fprintf(tw, "%s\tinput tokens\toutput tokens\tcost (est.)\t\n", title)
		for _, r := range rows {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t\n", r.Key, r.InputTokens, r.OutputTokens, formatCost(r.CostUSD))
		}
		_, _ = fmt.Fprintln(tw, "\t\t\t\t")
	}
	section("ISSUE", rep.Issues)
	section("DAY (UTC)", rep.Days)
	_, _ = fmt.Fprintf(tw, "TOTAL\t%d\t%d\t%s\t\n", rep.Total.InputTokens, rep.Total.OutputTokens, formatCost(rep.Total.CostUSD))
	_ = tw.Flush()
	_, _ = fmt.Fprintf(out, "Cost is estimated from Claude runs; Codex reports none. Generated %s.\n", time.Now().UTC().Format(time.RFC3339))
}
