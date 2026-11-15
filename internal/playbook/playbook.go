// Package playbook owns task-type playbooks (#218): Markdown files in
// .itervox/playbooks/<name>.md that tell a worker run how to approach its
// kind of work (a bug fix reproduces first, perf work measures a baseline
// first, a refactor preserves behaviour).
//
// A playbook file is optional YAML frontmatter plus a Markdown body:
//
//	---
//	description: Reproduce, fix the root cause, prove it with a test.
//	keywords: [bug, crash, error, regression]
//	require_evidence: [test]
//	---
//	1. Reproduce the problem first ...
//
// At dispatch the orchestrator reads the directory again (like the rules and
// memory files), chooses a playbook for the issue — the operator's per-issue
// override, else the first label agent.playbooks.labels maps to an existing
// playbook, else a confident keyword match over the title and description —
// and renders it as a `## Playbook: <name>` block after the profile's
// INSTRUCTIONS.md. The block is a prompt digest component (#186) only when a
// run has one, so runs without a playbook keep their digest. A playbook's
// require_evidence entries add to the profile's (#80): one evidence gate.
package playbook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/config"
)

// Bounds on what is read from disk: a file's size, how many playbooks a
// directory may hold, and a frontmatter list's length and entry size.
const (
	MaxFileBytes    = 32 * 1024
	MaxPlaybooks    = 50
	maxKeywords     = 64
	maxKeywordBytes = 64
	maxDescBytes    = 300
)

// None is the override value that attaches no playbook to an issue.
const None = "none"

// nameRe is a playbook name: the file name without .md.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidName reports whether s can name a playbook file.
func ValidName(s string) bool { return nameRe.MatchString(s) && s != None }

// Playbook is one parsed playbook file.
type Playbook struct {
	Name        string
	Description string
	// Keywords feed the keyword classifier (lower-cased, trimmed).
	Keywords []string
	// RequireEvidence are the evidence checks (#80) its runs must prove, in
	// addition to the profile's. Normalized; malformed names are dropped.
	RequireEvidence []string
	// Body is the Markdown after the frontmatter, trimmed.
	Body string
}

// Block is the playbook's prompt block: a `## Playbook: <name>` heading
// and its body. "" for a playbook with an empty body.
func (p Playbook) Block() string {
	if strings.TrimSpace(p.Body) == "" {
		return ""
	}
	return "## Playbook: " + p.Name + "\n\n" + p.Body
}

// Set is the playbooks of one directory, sorted by name.
type Set struct {
	Playbooks []Playbook
}

// Get returns the named playbook.
func (s Set) Get(name string) (Playbook, bool) {
	i := slices.IndexFunc(s.Playbooks, func(p Playbook) bool { return p.Name == name })
	if i < 0 {
		return Playbook{}, false
	}
	return s.Playbooks[i], true
}

// FileError is a playbook file that could not be used.
type FileError struct {
	Path    string
	Message string
}

// Dir is the playbooks directory of cfg: agent.playbooks.dir resolved
// against WORKFLOW.md's directory. "" when playbooks are off.
func Dir(cfg *config.Config) string {
	if cfg == nil || !cfg.Agent.Playbooks.Enabled {
		return ""
	}
	dir := cfg.Agent.Playbooks.Dir
	if dir == "" {
		dir = config.DefaultPlaybooksDir
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	base := "."
	if cfg.WorkflowPath != "" {
		base = filepath.Dir(cfg.WorkflowPath)
	}
	return filepath.Join(base, dir)
}

// Load reads every <name>.md in dir (not recursing), at most MaxPlaybooks.
// A missing directory is an empty set. A file that is too large, unreadable,
// misnamed or has malformed frontmatter is skipped and reported.
func Load(dir string) (Set, []FileError) {
	if dir == "" {
		return Set{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Set{}, nil
		}
		return Set{}, []FileError{{Path: dir, Message: err.Error()}}
	}
	var set Set
	var errs []FileError
	for _, e := range entries {
		name, isMD := strings.CutSuffix(e.Name(), ".md")
		if !isMD || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !ValidName(name) {
			errs = append(errs, FileError{Path: path, Message: "playbook names are lower-case letters, digits, '-' or '_'"})
			continue
		}
		if len(set.Playbooks) == MaxPlaybooks {
			errs = append(errs, FileError{Path: path, Message: fmt.Sprintf("more than %d playbooks; ignored", MaxPlaybooks)})
			continue
		}
		p, err := readFile(path, name)
		if err != nil {
			errs = append(errs, FileError{Path: path, Message: err.Error()})
			continue
		}
		set.Playbooks = append(set.Playbooks, p)
	}
	slices.SortFunc(set.Playbooks, func(a, b Playbook) int { return strings.Compare(a.Name, b.Name) })
	return set, errs
}

func readFile(path, name string) (Playbook, error) {
	f, err := os.Open(path)
	if err != nil {
		return Playbook{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Playbook{}, err
	}
	if len(data) > MaxFileBytes {
		return Playbook{}, fmt.Errorf("playbook: larger than %d bytes", MaxFileBytes)
	}
	return Parse(name, data)
}

// frontmatter is the YAML head of a playbook file.
type frontmatter struct {
	Description     string   `yaml:"description"`
	Keywords        []string `yaml:"keywords"`
	RequireEvidence []string `yaml:"require_evidence"`
}

// Parse reads one playbook file's content.
func Parse(name string, data []byte) (Playbook, error) {
	text := strings.ReplaceAll(string(bytes.TrimPrefix(data, []byte("\ufeff"))), "\r\n", "\n")
	var fm frontmatter
	body := text
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		head, tail, found := strings.Cut(rest, "\n---")
		if !found {
			return Playbook{}, errors.New("playbook: frontmatter is not closed with ---")
		}
		if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
			return Playbook{}, fmt.Errorf("playbook: frontmatter: %w", err)
		}
		_, body, _ = strings.Cut(tail, "\n")
	}
	p := Playbook{
		Name:        name,
		Description: bound(strings.TrimSpace(fm.Description), maxDescBytes),
		Body:        strings.TrimSpace(body),
	}
	for _, k := range fm.Keywords {
		k = normalizeText(k)
		if k != "" && len(k) <= maxKeywordBytes && !slices.Contains(p.Keywords, k) && len(p.Keywords) < maxKeywords {
			p.Keywords = append(p.Keywords, k)
		}
	}
	for _, c := range config.NormalizeEvidenceChecks(fm.RequireEvidence) {
		if config.ValidEvidenceCheckName(c) {
			p.RequireEvidence = append(p.RequireEvidence, c)
		}
	}
	return p, nil
}

// bound cuts s to at most n bytes on a rune boundary.
func bound(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
