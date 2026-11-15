package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStore lays out a project (WORKFLOW.md under root/proj) with the
// private file redirected through ITERVOX_USER_MEMORY.
func testStore(t *testing.T) (Store, string, string) {
	t.Helper()
	root := t.TempDir()
	wf := filepath.Join(root, "proj", "WORKFLOW.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(wf), 0o755))
	require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))
	user := filepath.Join(root, "home", ".itervox", "memory.json")
	t.Setenv(UserMemoryEnv, user)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return Store{WorkflowPath: wf, Now: func() time.Time { return now }}, user, ProjectPath(wf)
}

const (
	ghToken    = "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB"
	anthropicK = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
)

// TestMemoryNeverStoresSecrets (#202): every save path (create, update,
// promote, restore) redacts secrets before anything reaches either memory
// file, and a secret typed into a file by hand never reaches a prompt.
func TestMemoryNeverStoresSecrets(t *testing.T) {
	s, user, project := testStore(t)

	e, err := s.Create(Entry{
		Text:   "Deploy with GITHUB_TOKEN=" + ghToken + " and the key " + anthropicK,
		Tags:   []string{"deploy", ghToken},
		Source: Source{Links: []string{"https://ci.example.com/run?token=0123456789abcdef0123456789abcdef"}},
	})
	require.NoError(t, err)
	assert.NotContains(t, e.Text, ghToken, "the returned entry is the redacted one")

	other, err := s.Create(Entry{Text: "plain"})
	require.NoError(t, err)
	text := "Use password=hunter2-hunter2 against staging"
	_, err = s.Update(other.ID, Patch{Text: &text})
	require.NoError(t, err)
	_, err = s.Promote(other.ID)
	require.NoError(t, err)

	for _, path := range []string{user, project} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, secret := range []string{ghToken, anthropicK, "hunter2-hunter2", "0123456789abcdef0123456789abcdef"} {
			assert.NotContains(t, string(raw), secret, "%s holds a secret", path)
		}
	}
	raw, _ := os.ReadFile(user)
	assert.Contains(t, string(raw), "Deploy with GITHUB_TOKEN=", "the rest of the text is kept")

	// A file edited by hand: the secret is in the file, but not in the block.
	hand := `{"memories":[{"id":"hand","type":"fact","status":"active","text":"token ` + ghToken + `"}]}`
	require.NoError(t, os.WriteFile(project, []byte(hand), 0o644))
	sel, errs := Resolve(s.WorkflowPath, Target{}, 2000)
	require.Empty(t, errs)
	assert.Contains(t, sel.Block(), "(hand)")
	assert.NotContains(t, sel.Block(), ghToken)
}

// TestMemoryRejectsEntriesOverSizeCap (#202): an entry over a size cap is
// rejected with ErrTooLarge, never truncated, and nothing is written.
func TestMemoryRejectsEntriesOverSizeCap(t *testing.T) {
	s, user, _ := testStore(t)
	_, err := s.Create(Entry{Text: strings.Repeat("x", MaxTextBytes+1)})
	require.ErrorIs(t, err, ErrTooLarge)
	_, statErr := os.Stat(user)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "nothing was written")

	tags := make([]string, MaxListItems+1)
	for i := range tags {
		tags[i] = "t" + strings.Repeat("a", i+1)
	}
	_, err = s.Create(Entry{Text: "ok", Tags: tags})
	require.ErrorIs(t, err, ErrTooLarge)

	e, err := s.Create(Entry{Text: "ok"})
	require.NoError(t, err)
	big := strings.Repeat("y", MaxTextBytes+1)
	_, err = s.Update(e.ID, Patch{Text: &big})
	require.ErrorIs(t, err, ErrTooLarge)
}

// TestMemoryStoreCRUDAndRecordUse (#202): create, update, pin, delete and
// restore round-trip through the file, and RecordUse bumps lastUsed and
// useCount of the entries it names, skipping ids no longer present.
func TestMemoryStoreCRUDAndRecordUse(t *testing.T) {
	s, user, _ := testStore(t)
	e, err := s.Create(Entry{Text: "this repo uses pnpm", Type: TypeFact, Tags: []string{"tooling"}})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(e.ID, "m-"))
	assert.Equal(t, ScopePrivate, e.Scope)
	assert.Equal(t, StatusActive, e.Status)

	pinned, err := s.Pin(e.ID, true)
	require.NoError(t, err)
	assert.True(t, pinned.Pinned)

	archived := StatusArchived
	_, err = s.Update(e.ID, Patch{Status: &archived})
	require.NoError(t, err)
	_, err = s.Update("nope", Patch{Status: &archived})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = s.Create(Entry{ID: e.ID, Text: "dup"})
	require.ErrorIs(t, err, ErrConflict)

	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.RecordUse([]string{e.ID, "gone"}, at))
	require.NoError(t, s.RecordUse([]string{e.ID}, at.Add(time.Hour)))
	f := ReadFile(ScopePrivate, user)
	require.NoError(t, f.Err)
	require.Len(t, f.Entries, 1)
	assert.Equal(t, 2, f.Entries[0].UseCount)
	require.NotNil(t, f.Entries[0].LastUsed)
	assert.True(t, f.Entries[0].LastUsed.Equal(at.Add(time.Hour)))

	deleted, err := s.Delete(e.ID)
	require.NoError(t, err)
	assert.Empty(t, ReadFile(ScopePrivate, user).Entries)
	restored, err := s.Create(deleted)
	require.NoError(t, err)
	assert.Equal(t, deleted, restored, "undo restores the entry as it was")

	info, err := os.Stat(user)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the private file is the user's own")
}

// TestMemoryBrokenFileIsReportedAndNotOverwritten (#202): a file that does
// not parse contributes nothing, is reported, and is never rewritten.
func TestMemoryBrokenFileIsReportedAndNotOverwritten(t *testing.T) {
	s, _, project := testStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(project), 0o755))
	broken := `{"memories":[{"id":"a","type":"rumour","text":"x","status":"active"}]}`
	require.NoError(t, os.WriteFile(project, []byte(broken), 0o644))
	_, errs := Resolve(s.WorkflowPath, Target{}, 2000)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "memories[0].type")

	_, err := s.Create(Entry{Text: "x", Scope: ScopeProject})
	require.ErrorIs(t, err, ErrInvalid)
	raw, _ := os.ReadFile(project)
	assert.Equal(t, broken, string(raw))
}

// TestPublishedMemorySchemaMatchesParser (#202): the JSON Schema published
// at SchemaURL describes exactly what Parse accepts: the same fields,
// types, statuses, id pattern and size caps.
func TestPublishedMemorySchemaMatchesParser(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "site", "public", "schema", "memory.schema.json"))
	require.NoError(t, err)
	var schema struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       struct {
			Entry struct {
				Properties map[string]struct {
					Enum       []string                   `json:"enum"`
					Pattern    string                     `json:"pattern"`
					MaxLength  int                        `json:"maxLength"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"properties"`
			} `json:"entry"`
			Names struct {
				MaxItems int `json:"maxItems"`
				Items    struct {
					MaxLength int `json:"maxLength"`
				} `json:"items"`
			} `json:"names"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	assert.Equal(t, SchemaURL, schema.ID)
	assert.ElementsMatch(t, topKeys, keys(schema.Properties))
	p := schema.Defs.Entry.Properties
	assert.ElementsMatch(t, entryKeys, keys(p))
	assert.Equal(t, idRe.String(), p["id"].Pattern)
	assert.ElementsMatch(t, toStrings(Types), p["type"].Enum)
	assert.ElementsMatch(t, toStrings(Statuses), p["status"].Enum)
	assert.ElementsMatch(t, toStrings(Scopes), p["scope"].Enum)
	assert.Equal(t, MaxTextBytes, p["text"].MaxLength)
	assert.ElementsMatch(t, []string{"kind", "issue", "runIds", "links"}, keys(p["source"].Properties))
	assert.ElementsMatch(t, lessonKeys, keys(p["lesson"].Properties), "#203: the lesson record")
	assert.Equal(t, MaxListItems, schema.Defs.Names.MaxItems)
	assert.Equal(t, MaxItemBytes, schema.Defs.Names.Items.MaxLength)
}

func toStrings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
