// Package tasktemplate owns saved task templates (#251): Markdown files in
// .itervox/tasks/<name>.md that pre-fill the New task form and
// `itervox task --template NAME`. They are committable, like playbooks.
//
// A template is YAML frontmatter for the fields plus a Markdown body for the
// custom instructions:
//
//	---
//	title: Upgrade {{package}} to {{version}}
//	description: Bump one dependency and fix what breaks.
//	profile: implementer
//	playbook: dependency-upgrade
//	backend: claude
//	base: main
//	---
//	Upgrade {{package}} to {{version}}. Run the full test suite.
//
// `{{var}}` placeholders (letters, digits, '_' and '-') in the title and the
// instructions are filled from the form or `--var key=value`. A placeholder
// with no value is reported, never sent to an agent as literal braces.
package tasktemplate

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

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Bounds on what is read and written.
const (
	MaxFileBytes      = 32 * 1024
	MaxTemplates      = 100
	maxTitleBytes     = 300
	maxDescBytes      = 300
	maxFieldBytes     = 128
	maxInstructionLen = MaxFileBytes - 2*1024
)

// RelDir is the templates directory relative to WORKFLOW.md's directory.
const RelDir = ".itervox/tasks"

// nameRe is a template name: the file name without .md.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// varRe is a `{{var}}` placeholder (spaces inside the braces allowed).
var varRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]{1,64})\s*\}\}`)

// ValidName reports whether s can name a template file.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// ErrNotFound: no template of that name.
var ErrNotFound = errors.New("tasktemplate: not found")

// ErrExists: a create found a template of that name already.
var ErrExists = errors.New("tasktemplate: already exists")

// ErrInvalidName: the name is not lower-case letters, digits, '-' or '_'.
var ErrInvalidName = errors.New("tasktemplate: names are lower-case letters, digits, '-' or '_' (at most 64)")

// Template is one task template.
type Template struct {
	Name        string `yaml:"-"`
	Title       string `yaml:"title"`
	Description string `yaml:"description,omitempty"`
	Profile     string `yaml:"profile,omitempty"`
	Playbook    string `yaml:"playbook,omitempty"`
	Backend     string `yaml:"backend,omitempty"`
	BaseBranch  string `yaml:"base,omitempty"`
	// Instructions is the Markdown body.
	Instructions string `yaml:"-"`
}

// Vars lists the template's placeholders in order of first appearance
// (title first, then instructions).
func (t Template) Vars() []string {
	var out []string
	for _, m := range varRe.FindAllStringSubmatch(t.Title+"\n"+t.Instructions, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// Fill replaces every `{{var}}` in s with vars[var]. missing lists the
// placeholders vars has no value for, in order; they are left in place.
func Fill(s string, vars map[string]string) (string, []string) {
	var missing []string
	out := varRe.ReplaceAllStringFunc(s, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		if !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
		return m
	})
	return out, missing
}

// Render fills the template's title and instructions from vars. missing
// lists the placeholders without a value (an error for the caller).
func (t Template) Render(vars map[string]string) (title, instructions string, missing []string) {
	title, m1 := Fill(t.Title, vars)
	instructions, m2 := Fill(t.Instructions, vars)
	for _, m := range m2 {
		if !slices.Contains(m1, m) {
			m1 = append(m1, m)
		}
	}
	return title, instructions, m1
}

// FileError is a template file that could not be used.
type FileError struct {
	Path    string
	Message string
}

// Dir is the templates directory of the project whose WORKFLOW.md is at
// workflowPath.
func Dir(workflowPath string) string {
	base := "."
	if workflowPath != "" {
		base = filepath.Dir(workflowPath)
	}
	return filepath.Join(base, RelDir)
}

// Load reads every <name>.md in dir, sorted by name. A missing directory is
// no templates; a file that cannot be used is skipped and reported.
func Load(dir string) ([]Template, []FileError) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []FileError{{Path: dir, Message: err.Error()}}
	}
	var out []Template
	var errs []FileError
	for _, e := range entries {
		name, isMD := strings.CutSuffix(e.Name(), ".md")
		if !isMD || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !ValidName(name) {
			errs = append(errs, FileError{Path: path, Message: ErrInvalidName.Error()})
			continue
		}
		if len(out) == MaxTemplates {
			errs = append(errs, FileError{Path: path, Message: fmt.Sprintf("more than %d templates; ignored", MaxTemplates)})
			continue
		}
		t, err := readFile(path, name)
		if err != nil {
			errs = append(errs, FileError{Path: path, Message: err.Error()})
			continue
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out, errs
}

// Read reads the named template.
func Read(dir, name string) (Template, error) {
	if !ValidName(name) {
		return Template{}, ErrInvalidName
	}
	t, err := readFile(filepath.Join(dir, name+".md"), name)
	if errors.Is(err, os.ErrNotExist) {
		return Template{}, ErrNotFound
	}
	return t, err
}

func readFile(path, name string) (Template, error) {
	f, err := os.Open(path)
	if err != nil {
		return Template{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Template{}, err
	}
	if len(data) > MaxFileBytes {
		return Template{}, fmt.Errorf("tasktemplate: larger than %d bytes", MaxFileBytes)
	}
	return Parse(name, data)
}

// utf8BOM is stripped from the start of a file.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parse reads one template file's content.
func Parse(name string, data []byte) (Template, error) {
	text := strings.ReplaceAll(string(bytes.TrimPrefix(data, utf8BOM)), "\r\n", "\n")
	t := Template{Name: name}
	body := text
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		head, tail, found := strings.Cut(rest, "\n---")
		if !found {
			return Template{}, errors.New("tasktemplate: frontmatter is not closed with ---")
		}
		if err := yaml.Unmarshal([]byte(head), &t); err != nil {
			return Template{}, fmt.Errorf("tasktemplate: frontmatter: %w", err)
		}
		_, body, _ = strings.Cut(tail, "\n")
	}
	t.Name = name
	t.Instructions = strings.TrimSpace(body)
	t.normalize()
	if t.Title == "" {
		return Template{}, errors.New("tasktemplate: frontmatter needs a title")
	}
	return t, nil
}

// normalize trims every field.
func (t *Template) normalize() {
	t.Title = strings.TrimSpace(t.Title)
	t.Description = strings.TrimSpace(t.Description)
	t.Profile = strings.TrimSpace(t.Profile)
	t.Playbook = strings.TrimSpace(t.Playbook)
	t.Backend = strings.TrimSpace(t.Backend)
	t.BaseBranch = strings.TrimSpace(t.BaseBranch)
	t.Instructions = strings.TrimSpace(t.Instructions)
}

// InvalidError is a template that cannot be saved; Message says why.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return "tasktemplate: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...)}
}

// Validate checks a template before it is written: ErrInvalidName, or an
// *InvalidError.
func (t Template) Validate() error {
	switch {
	case !ValidName(t.Name):
		return ErrInvalidName
	case t.Title == "":
		return invalid("a template needs a title")
	case len(t.Title) > maxTitleBytes:
		return invalid("the title is longer than %d bytes", maxTitleBytes)
	case len(t.Description) > maxDescBytes:
		return invalid("the description is longer than %d bytes", maxDescBytes)
	case len(t.Instructions) > maxInstructionLen:
		return invalid("the instructions are longer than %d bytes", maxInstructionLen)
	}
	for _, f := range []string{t.Profile, t.Playbook, t.Backend, t.BaseBranch} {
		if len(f) > maxFieldBytes || strings.ContainsAny(f, "\n\r") {
			return invalid("%q is not a valid field value", f)
		}
	}
	return nil
}

// Marshal serializes t as a template file.
func (t Template) Marshal() ([]byte, error) {
	t.normalize()
	head, err := yaml.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("tasktemplate: %w", err)
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n")
	if t.Instructions != "" {
		b.WriteString("\n")
		b.WriteString(t.Instructions)
		b.WriteString("\n")
	}
	return b.Bytes(), nil
}

// Write saves t in dir. replace false refuses an existing template
// (ErrExists); true creates or replaces it.
func Write(dir string, t Template, replace bool) error {
	t.normalize()
	if err := t.Validate(); err != nil {
		return err
	}
	data, err := t.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tasktemplate: %w", err)
	}
	path := filepath.Join(dir, t.Name+".md")
	if !replace {
		if _, err := os.Stat(path); err == nil {
			return ErrExists
		}
	}
	if err := atomicfs.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("tasktemplate: write %s: %w", path, err)
	}
	return nil
}

// Delete removes the named template (ErrNotFound when there is none).
func Delete(dir, name string) error {
	if !ValidName(name) {
		return ErrInvalidName
	}
	err := os.Remove(filepath.Join(dir, name+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return err
}
