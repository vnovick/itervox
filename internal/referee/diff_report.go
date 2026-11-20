package referee

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// changedProbes counts the probes whose behaviour changed.
func changedProbes(rep diffReport) (changed, total int) {
	for _, t := range rep.Tools {
		for _, p := range t.Probes {
			total++
			if p.Changed {
				changed++
			}
		}
	}
	return changed, total
}

// runSummary is "exit 0", "exit 3", "timed out" or "waiting for input",
// for one side.
func runSummary(r probeRun) string {
	if r.TimedOut {
		return "timed out"
	}
	if r.WaitingForInput {
		return "waiting for input"
	}
	return fmt.Sprintf("exit %d", r.ExitCode)
}

func printDiff(w io.Writer, rep diffReport) {
	changed, total := changedProbes(rep)
	switch {
	case len(rep.Tools) == 0:
		_, _ = fmt.Fprintf(w, "diff: no tools: configured (policy: %s); nothing to compare\n", displayPath(rep.Policy))
	case changed == 0:
		_, _ = fmt.Fprintf(w, "diff: no behaviour change in %d probe(s) (policy: %s, base install: %s @ %s)\n",
			total, displayPath(rep.Policy), displayPath(rep.BaseDir), short(rep.BaseDirCommit))
	default:
		_, _ = fmt.Fprintf(w, "diff: %d of %d probe(s) changed behaviour (policy: %s, base install: %s @ %s)\n",
			changed, total, displayPath(rep.Policy), displayPath(rep.BaseDir), short(rep.BaseDirCommit))
	}
	for _, t := range rep.Tools {
		for _, p := range t.Probes {
			if !p.Changed {
				_, _ = fmt.Fprintf(w, "  [%s] %s: unchanged (%s)\n", t.Name, displayPath(p.Probe), runSummary(p.Head))
				continue
			}
			_, _ = fmt.Fprintf(w, "  [%s] %s: changed (%s)\n", t.Name, displayPath(p.Probe), strings.Join(p.Changes, ", "))
			_, _ = fmt.Fprintf(w, "      base %s, head %s\n", runSummary(p.Base), runSummary(p.Head))
			for _, q := range p.Prompts {
				_, _ = fmt.Fprintf(w, "      new prompt: %s\n", q)
			}
			for _, l := range p.Added {
				_, _ = fmt.Fprintf(w, "      + %s\n", l)
			}
			if more := p.AddedCount - len(p.Added); more > 0 {
				_, _ = fmt.Fprintf(w, "      + … %d more line(s)\n", more)
			}
			for _, l := range p.Removed {
				_, _ = fmt.Fprintf(w, "      - %s\n", l)
			}
			if more := p.RemovedCount - len(p.Removed); more > 0 {
				_, _ = fmt.Fprintf(w, "      - … %d more line(s)\n", more)
			}
			if p.AddedCount == 0 && p.RemovedCount == 0 && slices.Contains(p.Changes, "output") {
				_, _ = fmt.Fprintln(w, "      (the same lines in a different order)")
			}
		}
	}
	for _, n := range rep.Notes {
		_, _ = fmt.Fprintf(w, "note: %s\n", displayPath(n))
	}
}

func diffMarkdown(rep diffReport) string {
	var b strings.Builder
	b.WriteString("### referee diff\n\n")
	changed, total := changedProbes(rep)
	switch {
	case len(rep.Tools) == 0:
		fmt.Fprintf(&b, "No `tools:` in the policy (`%s`); nothing to compare.\n\n", mdCode(rep.Policy))
	case changed == 0:
		fmt.Fprintf(&b, "No behaviour change in %d probe(s) between the base install (`%s`) and the head.\n\n", total, short(rep.BaseDirCommit))
	default:
		fmt.Fprintf(&b, "**%d of %d probe(s) changed behaviour** between the base install (`%s`) and the head. A person should look:\n\n", changed, total, short(rep.BaseDirCommit))
	}
	for _, t := range rep.Tools {
		for _, p := range t.Probes {
			if !p.Changed {
				continue
			}
			fmt.Fprintf(&b, "- **%s** `%s`: %s (base %s, head %s)\n", mdCode(t.Name), mdCode(p.Probe),
				strings.Join(p.Changes, ", "), runSummary(p.Base), runSummary(p.Head))
			for _, q := range p.Prompts {
				fmt.Fprintf(&b, "  - new prompt: `%s`\n", mdCode(q))
			}
			if len(p.Added)+len(p.Removed) > 0 {
				b.WriteString("\n  ```diff\n")
				for _, l := range p.Added {
					fmt.Fprintf(&b, "  + %s\n", strings.ReplaceAll(l, "```", "'''"))
				}
				for _, l := range p.Removed {
					fmt.Fprintf(&b, "  - %s\n", strings.ReplaceAll(l, "```", "'''"))
				}
				b.WriteString("  ```\n")
			}
		}
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(&b, "\nNote: %s\n", mdCode(n))
	}
	b.WriteString("\n")
	return b.String()
}
