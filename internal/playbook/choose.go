package playbook

import (
	"strings"
	"unicode"

	"github.com/vnovick/itervox/internal/config"
)

// How a playbook was chosen for an issue.
const (
	SourceOverride = "override" // the operator's per-issue choice
	SourceLabel    = "label"    // an issue label in agent.playbooks.labels
	SourceKeyword  = "keyword"  // the keyword classifier
)

// Keyword classifier weights and threshold: a keyword found in the title
// scores titleWeight, one found only in the description bodyWeight. The
// best playbook must reach minConfidentScore and strictly beat the
// runner-up; otherwise no playbook is attached.
const (
	titleWeight       = 2
	bodyWeight        = 1
	minConfidentScore = 2
)

// Input is what Choose knows about an issue.
type Input struct {
	// Override is the operator's per-issue choice: a playbook name, None,
	// or "" for automatic.
	Override    string
	Labels      []string
	Title       string
	Description string
}

// Choice is the playbook chosen for an issue. A zero Playbook.Name means
// none; Source then says whether the operator chose none (SourceOverride)
// or nothing matched ("").
type Choice struct {
	Playbook Playbook
	Source   string
	// Matched is the label, or the keywords, that chose it.
	Matched string
}

// Name is the chosen playbook's name, "" for none.
func (c Choice) Name() string { return c.Playbook.Name }

// Choose picks the issue's playbook from set: the override when it names an
// existing playbook (or None), else the first issue label cfg.Labels maps to
// an existing playbook, else — when cfg.Classify — a confident keyword
// match, else none. An override naming a playbook that no longer exists
// falls through to the automatic choice.
func Choose(set Set, cfg config.PlaybooksConfig, in Input) Choice {
	if !cfg.Enabled {
		return Choice{}
	}
	switch ov := strings.TrimSpace(in.Override); {
	case ov == None:
		return Choice{Source: SourceOverride}
	case ov != "":
		if p, ok := set.Get(ov); ok {
			return Choice{Playbook: p, Source: SourceOverride}
		}
	}
	if len(set.Playbooks) == 0 {
		return Choice{}
	}
	for _, label := range in.Labels {
		name, ok := cfg.Labels[strings.ToLower(strings.TrimSpace(label))]
		if !ok {
			continue
		}
		if p, ok := set.Get(name); ok {
			return Choice{Playbook: p, Source: SourceLabel, Matched: label}
		}
	}
	if cfg.Classify {
		if p, hits, ok := Classify(set, in.Title, in.Description); ok {
			return Choice{Playbook: p, Source: SourceKeyword, Matched: strings.Join(hits, ", ")}
		}
	}
	return Choice{}
}

// Classify scores title and description against each playbook's keywords
// (whole words or phrases, case-insensitive): titleWeight per keyword in
// the title, bodyWeight per keyword only in the description. It returns
// the best playbook and the keywords it matched when the best score is at
// least minConfidentScore and strictly higher than every other playbook's.
func Classify(set Set, title, description string) (Playbook, []string, bool) {
	t, d := " "+normalizeText(title)+" ", " "+normalizeText(description)+" "
	best, second := -1, 0
	bestScore := 0
	var bestHits []string
	for i, p := range set.Playbooks {
		score := 0
		var hits []string
		for _, k := range p.Keywords {
			needle := " " + k + " "
			switch {
			case strings.Contains(t, needle):
				score += titleWeight
			case strings.Contains(d, needle):
				score += bodyWeight
			default:
				continue
			}
			hits = append(hits, k)
		}
		switch {
		case score > bestScore:
			second, bestScore, best, bestHits = bestScore, score, i, hits
		case score > second:
			second = score
		}
	}
	if best < 0 || bestScore < minConfidentScore || bestScore == second {
		return Playbook{}, nil, false
	}
	return set.Playbooks[best], bestHits, true
}

// normalizeText lower-cases s and turns every run of characters that are
// not letters or digits into one space, so keywords match whole words.
func normalizeText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
			continue
		}
		space = true
	}
	return b.String()
}
