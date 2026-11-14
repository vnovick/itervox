package attest

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirName is the store's directory under the daemon's project state
// directory (.itervox/attestations).
const DirName = "attestations"

// maxLineBytes bounds one JSONL line when reading (a record's tail is
// TailBytes, so a real line is far smaller).
const maxLineBytes = 1 << 20

// Store keeps one JSONL file of records per issue run:
// <Dir>/<issue>/<run id>.jsonl, in the order they were appended.
type Store struct {
	Dir string
}

// RunFile is one issue run's records, as stored.
type RunFile struct {
	ID      string
	Path    string
	Records []Record
}

// Seal is the run's last seal record, if any.
func (r RunFile) Seal() (Record, bool) {
	for i := len(r.Records) - 1; i >= 0; i-- {
		if r.Records[i].Kind == KindSeal {
			return r.Records[i], true
		}
	}
	return Record{}, false
}

// RunPath is the file for issue's run runID.
func (s Store) RunPath(issue, runID string) string {
	return filepath.Join(s.Dir, safeName(issue), safeName(runID)+".jsonl")
}

// Append writes recs to the end of issue's run file, creating it.
func (s Store) Append(issue, runID string, recs ...Record) error {
	if s.Dir == "" {
		return errors.New("attest: store has no directory")
	}
	path := s.RunPath(issue, runID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("attest: store dir: %w", err)
	}
	var buf bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("attest: encode record: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("attest: open %s: %w", path, err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return fmt.Errorf("attest: write %s: %w", path, err)
	}
	return f.Close()
}

// Runs returns issue's runs, oldest first (run ids start with the run's
// UTC timestamp). No runs is not an error.
func (s Store) Runs(issue string) ([]RunFile, error) {
	dir := filepath.Join(s.Dir, safeName(issue))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attest: read %s: %w", dir, err)
	}
	var runs []RunFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := filepath.Join(dir, name)
		recs, err := ReadFile(path)
		if err != nil {
			return nil, err
		}
		runs = append(runs, RunFile{ID: strings.TrimSuffix(name, ".jsonl"), Path: path, Records: recs})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, nil
}

// ReadFile reads a JSONL record file. Blank lines are skipped; a line that
// is not a record is an error naming the line.
func ReadFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("attest: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	var recs []Record
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("attest: %s line %d: %w", path, n, err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("attest: read %s: %w", path, err)
	}
	return recs, nil
}

// CheckResult is one record's verification against its run.
type CheckResult struct {
	Record   Record
	Verified bool
	// Reason says why Verified is false ("" when it is true).
	Reason string
}

// VerifyRun checks every record of run with pub: a seal must carry a valid
// signature, and a check record is verified when its signature is valid and
// its tree is the run's final tree (the tree its seal records).
func VerifyRun(run RunFile, pub ed25519.PublicKey) []CheckResult {
	seal, hasSeal := run.Seal()
	sealErr := errors.New("attest: the run has no seal")
	if hasSeal {
		sealErr = VerifySignature(seal, pub)
	}
	out := make([]CheckResult, 0, len(run.Records))
	for _, r := range run.Records {
		var err error
		switch {
		case r.Kind == KindSeal:
			err = VerifySignature(r, pub)
		case sealErr != nil:
			if err = VerifySignature(r, pub); err == nil {
				err = fmt.Errorf("attest: the run's final tree is unknown (%v)", sealErr)
			}
		default:
			err = Verify(r, pub, seal.TreeHash)
		}
		res := CheckResult{Record: r, Verified: err == nil}
		if err != nil {
			res.Reason = err.Error()
		}
		out = append(out, res)
	}
	return out
}

// safeName makes s one path component: anything but letters, digits, '.',
// '-' and '_' becomes '_', and a name of only dots is replaced.
func safeName(s string) string {
	b := []byte(s)
	for i, c := range b {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_'
		if !ok {
			b[i] = '_'
		}
	}
	out := string(b)
	if strings.Trim(out, ".") == "" {
		return "_"
	}
	return out
}
