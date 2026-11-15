package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// #239 — `itervox init --update` moves the layered front-matter keys
// (config.FrontMatterLayeredKeys: agent, automations, dependencies, hooks,
// hub, polling, server, tracker, workspace) out of WORKFLOW.md and into the
// project settings file, .itervox/settings.jsonc. WORKFLOW.md keeps its body
// (the prompt template), the schema marker, the sections that are not
// layered, and config.FrontMatterKeptKeys.
//
//   - Comments travel: a YAML comment above, beside or below a moved key is
//     written as a // comment at the same place in the JSONC file, and every
//     comment already in the settings file stays.
//   - Secrets stay out: a $VAR reference moves as written (never resolved);
//     a key holding an inline secret stays in WORKFLOW.md and is reported.
//   - Nothing changes in effect: a key the settings file already sets keeps
//     the file's value (it was already the one in effect, #239 precedence)
//     and its front-matter copy is dropped; the merged config before and
//     after the move is compared and the move is refused if it differs.
//   - WORKFLOW.md.bak holds the original before anything is written; a dry
//     run prints the result and writes nothing.

// settingsMove is a planned (or applied) move.
type settingsMove struct {
	// Moved are the keys written to the settings file; Dropped the keys the
	// settings file already set, whose front-matter copy is removed; Kept
	// "key: why" for the keys that stay in WORKFLOW.md.
	Moved, Dropped, Kept []string
	// Settings is the settings file after the move; Workflow the WORKFLOW.md
	// text. Both are nil when nothing moves.
	Settings, Workflow []byte
	SettingsPath       string
	BackupPath         string
}

func (m settingsMove) changed() bool { return m.Workflow != nil }

// moveFrontMatterToSettings applies (or, for dryRun, only plans) the move for
// the workflow at path, under the WORKFLOW.md edit lock. backup is what
// WORKFLOW.md.bak is written with before anything else (the workflow as the
// operator left it, before this run's earlier steps; nil: the file as it is
// now); backupDone says the schema step of this same run already wrote the
// backup, so it is not written again. An existing backup is overwritten only
// with force.
func moveFrontMatterToSettings(path string, force, dryRun, backupDone bool, backup []byte) (settingsMove, error) {
	if dryRun {
		return planFrontMatterMove(path)
	}
	var move settingsMove
	err := workflow.WithEditLock(path, func() error {
		var err error
		if move, err = planFrontMatterMove(path); err != nil || !move.changed() {
			return err
		}
		return applyFrontMatterMove(path, &move, force, backupDone, backup)
	})
	return move, err
}

func applyFrontMatterMove(path string, move *settingsMove, force, backupDone bool, original []byte) error {
	if original == nil {
		var err error
		if original, err = os.ReadFile(path); err != nil {
			return err
		}
	}
	backup := path + ".bak"
	if !backupDone {
		if !force && !fileMissing(backup) {
			return fmt.Errorf("%s already exists; rerun with --force to overwrite or move/delete the existing backup first", backup)
		}
		if err := atomicfs.WriteFile(backup, original, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", backup, err)
		}
	}
	move.BackupPath = backup
	// The settings file first, then WORKFLOW.md: between the two writes
	// both hold the moved keys with the same values, so a failure in
	// between changes nothing in effect; the settings file is still put
	// back.
	before, readErr := os.ReadFile(move.SettingsPath)
	existed := readErr == nil
	_, statErr := os.Stat(filepath.Dir(move.SettingsPath))
	if err := config.WriteJSONCFile(move.SettingsPath, move.Settings, func(b []byte) error {
		_, err := config.ParseSettings(b)
		return err
	}); err != nil {
		return err
	}
	if err := atomicfs.WriteFile(path, move.Workflow, 0o644); err != nil {
		if rbErr := restoreProjectSettings(move.SettingsPath, before, existed, statErr == nil); rbErr != nil {
			return fmt.Errorf("write %s: %w; restoring %s also failed: %w", path, err, move.SettingsPath, rbErr)
		}
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// planFrontMatterMove computes the move without writing anything.
func planFrontMatterMove(path string) (settingsMove, error) {
	move := settingsMove{SettingsPath: config.ProjectSettingsPath(path)}
	original, err := os.ReadFile(path)
	if err != nil {
		return move, err
	}
	front, body, ok := splitWorkflowFrontMatter(string(original))
	if !ok {
		return move, fmt.Errorf("%s has no YAML front matter", path)
	}
	docNode, root, err := parseFrontMatterNode(front)
	if err != nil {
		return move, fmt.Errorf("parse %s: %w", path, err)
	}
	var doc map[string]any
	if err := root.Decode(&doc); err != nil {
		return move, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	existing, err := os.ReadFile(move.SettingsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return move, err
	}
	project := map[string]any{}
	if err == nil {
		if project, err = config.ParseSettings(existing); err != nil {
			return move, fmt.Errorf("%s: %w (fix it, then run the move again)", move.SettingsPath, err)
		}
	} else {
		existing = config.NewSettingsFileText()
	}

	for _, key := range config.FrontMatterLayeredKeys(doc) {
		if config.KeptInFrontMatter(key) {
			continue // the profile editor and `itervox models refresh` write these to WORKFLOW.md
		}
		raw, _ := lookupSetting(doc, key)
		value := settingsValueFor(key, raw)
		if at, found := inlineSecretAt(key, value); found {
			move.Kept = append(move.Kept, key+": "+at+" holds an inline secret; put the value in .itervox/.env, set the key to a $VAR reference, and run `itervox init --update` again")
			continue
		}
		if _, registered := config.SettingFieldFor(key); registered {
			if err := config.ValidateSettingValue(key, value); err != nil {
				move.Kept = append(move.Kept, fmt.Sprintf("%s: %v", key, err))
				continue
			}
		}
		if config.SettingsDataSets(project, key) {
			move.Dropped = append(move.Dropped, key)
			continue
		}
		move.Moved = append(move.Moved, key)
	}
	// A layered section left with no keys ("hooks:" above commented-out
	// examples) sets nothing; it goes too, its comments with the moved keys.
	var empty []string
	for _, section := range config.LayeredSections {
		if v, ok := doc[section]; ok && v == nil {
			empty = append(empty, section)
		}
	}
	if len(move.Moved) == 0 && len(move.Dropped) == 0 {
		return move, nil
	}

	settings, err := mergeIntoSettingsText(existing, root, move.Moved, empty)
	if err != nil {
		return move, fmt.Errorf("build %s: %w", move.SettingsPath, err)
	}
	newProject, err := config.ParseSettings(settings)
	if err != nil {
		return move, fmt.Errorf("build %s: %w", move.SettingsPath, err)
	}
	for _, key := range slices.Concat(move.Moved, move.Dropped, empty) {
		removeYAMLKey(root, strings.Split(key, "."))
	}
	encoded, err := encodeFrontMatterNode(docNode, detectFrontMatterIndent(front))
	if err != nil {
		return move, fmt.Errorf("encode %s: %w", path, err)
	}
	var newDoc map[string]any
	if err := yaml.Unmarshal(encoded, &newDoc); err != nil {
		return move, fmt.Errorf("encode %s: %w", path, err)
	}
	if newDoc == nil {
		newDoc = map[string]any{}
	}
	if diff := effectiveDiff(config.MergeFrontMatterAndSettings(doc, project), config.MergeFrontMatterAndSettings(newDoc, newProject)); diff != "" {
		return move, fmt.Errorf("moving the settings would change %s; nothing was written (please report this)", diff)
	}

	var out bytes.Buffer
	out.WriteString("---\n")
	if strings.TrimSpace(string(encoded)) != "{}" {
		out.WriteString(restoreTopLevelBlankLines(front, string(encoded)))
	}
	out.WriteString("---\n")
	out.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		out.WriteByte('\n')
	}
	move.Settings, move.Workflow = settings, out.Bytes()
	return move, nil
}

// settingsValueFor is a front-matter value in the shapes a settings file
// holds (and its parser accepts).
func settingsValueFor(key string, raw any) any {
	v := normalizeYAMLValue(raw)
	if key == "tracker.filter" {
		v = filterAsList(v)
	}
	return v
}

// filterAsList writes a single-group tracker.filter (a map) as the list of
// one group the settings schema describes; the parser reads both alike.
func filterAsList(v any) any {
	if m, ok := asMap(v); ok {
		return []any{normalizeYAMLValue(m)}
	}
	return v
}

// inlineSecretAt finds a secret written inline under key: a secret key
// (api_key, token, secret, password) whose value is not a $VAR reference, or
// a URL carrying a password. It returns the key path, never the value.
func inlineSecretAt(key string, v any) (string, bool) {
	switch t := v.(type) {
	case string:
		if config.IsSecretKey(key) && t != "" && !config.IsEnvReference(t) {
			return key, true
		}
		if strings.Contains(t, "://") {
			if u, err := url.Parse(t); err == nil && u.User != nil {
				if _, hasPassword := u.User.Password(); hasPassword {
					return key, true
				}
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if at, ok := inlineSecretAt(key+"."+k, t[k]); ok {
				return at, true
			}
		}
	case []any:
		for _, e := range t {
			if at, ok := inlineSecretAt(key, e); ok {
				return at, true
			}
		}
	}
	return "", false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// effectiveDiff names the first top-level key whose merged value differs,
// "" when none does. tracker.filter is compared in its list form.
func effectiveDiff(before, after map[string]any) string {
	norm := func(m map[string]any) map[string]any {
		out := normalizeYAMLValue(m).(map[string]any)
		if tr, ok := out["tracker"].(map[string]any); ok {
			if f, set := tr["filter"]; set {
				tr["filter"] = filterAsList(f)
			}
		}
		return out
	}
	b, a := norm(before), norm(after)
	keys := sortedKeys(b)
	for _, k := range sortedKeys(a) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		jb, _ := json.Marshal(b[k])
		ja, _ := json.Marshal(a[k])
		if !bytes.Equal(jb, ja) {
			return k
		}
	}
	return ""
}

// removeYAMLKey deletes the key at parts from a mapping node, and every
// mapping the deletion leaves empty on the way up.
func removeYAMLKey(m *yaml.Node, parts []string) bool {
	i := yamlNodeIndex(m, parts[0])
	if i < 0 {
		return false
	}
	if len(parts) > 1 {
		child := m.Content[i+1]
		if !removeYAMLKey(child, parts[1:]) || len(child.Content) > 0 {
			return true
		}
	}
	m.Content = append(m.Content[:i], m.Content[i+2:]...)
	return true
}

// mergeIntoSettingsText adds the moved keys, with their YAML comments, to the
// settings file text existing. A key the file already holds is never
// replaced (the caller moves only keys the file does not set).
func mergeIntoSettingsText(existing []byte, root *yaml.Node, moved, commentsOnly []string) ([]byte, error) {
	var gen strings.Builder
	gen.WriteString("{\n")
	writeYAMLMembers(&gen, root, "", moved, false, commentsOnly)
	gen.WriteString("}\n")
	src, err := hujson.Parse([]byte(gen.String()))
	if err != nil {
		return nil, fmt.Errorf("convert the front matter: %w", err)
	}
	dst, err := hujson.Parse(existing)
	if err != nil {
		return nil, err
	}
	dstObj, ok := dst.Value.(*hujson.Object)
	if !ok {
		return nil, errors.New("the settings file is not a JSON object")
	}
	mergeJSONCObject(dstObj, src.Value.(*hujson.Object))
	dst.Format()
	return dst.Pack(), nil
}

// mergeJSONCObject appends src's members to dst, recursing into an object
// both hold. Comments ride along: a member's leading comments are in its
// name's BeforeExtra, and a closing comment in the object's AfterExtra.
func mergeJSONCObject(dst, src *hujson.Object) {
	for _, m := range src.Members {
		name := m.Name.Value.(hujson.Literal).String()
		i := slices.IndexFunc(dst.Members, func(d hujson.ObjectMember) bool {
			return d.Name.Value.(hujson.Literal).String() == name
		})
		if i < 0 {
			dst.Members = append(dst.Members, m)
			continue
		}
		dObj, dOK := dst.Members[i].Value.Value.(*hujson.Object)
		sObj, sOK := m.Value.Value.(*hujson.Object)
		if !dOK || !sOK {
			continue // the file's own value stays (never reached: see the caller)
		}
		if len(sObj.Members) > 0 && hasComment(m.Name.BeforeExtra) {
			// The section's own comment leads its first moved member.
			sObj.Members[0].Name.BeforeExtra = append(bytes.Clone(m.Name.BeforeExtra), sObj.Members[0].Name.BeforeExtra...)
		}
		mergeJSONCObject(dObj, sObj)
	}
	if hasComment(src.AfterExtra) {
		dst.AfterExtra = append(dst.AfterExtra, src.AfterExtra...)
	}
}

func hasComment(extra hujson.Extra) bool { return bytes.Contains(extra, []byte("//")) }

// writeYAMLMembers writes the members of mapping node m whose key paths are in
// moved (or lead to one), or every member when all is set, as JSONC members;
// for a key in commentsOnly, only its comments.
func writeYAMLMembers(b *strings.Builder, m *yaml.Node, prefix string, moved []string, all bool, commentsOnly []string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		key := k.Value
		if prefix != "" {
			key = prefix + "." + k.Value
		}
		if slices.Contains(commentsOnly, key) {
			for _, c := range []string{k.HeadComment, v.HeadComment, k.LineComment, v.LineComment, k.FootComment, v.FootComment} {
				writeComments(b, c)
			}
			continue
		}
		whole := all || slices.Contains(moved, key)
		under := !whole && slices.ContainsFunc(moved, func(p string) bool { return strings.HasPrefix(p, key+".") })
		if !whole && !under {
			continue
		}
		writeComments(b, k.HeadComment)
		writeComments(b, v.HeadComment)
		name, _ := json.Marshal(k.Value)
		b.Write(name)
		b.WriteString(": ")
		switch {
		case under && v.Kind == yaml.MappingNode:
			b.WriteString("{\n")
			writeYAMLMembers(b, v, key, moved, false, nil)
			b.WriteString("}")
		default:
			writeYAMLValue(b, key, v)
		}
		b.WriteString(",")
		writeLineComment(b, k.LineComment, v.LineComment)
		b.WriteString("\n")
		writeComments(b, k.FootComment)
		writeComments(b, v.FootComment)
	}
}

// writeYAMLValue writes one value node as JSON, keeping the comments inside
// a map or a list. A value with an alias or a merge key, and one the settings
// file holds in another shape (tracker.filter), is written from its decoded
// form without them.
func writeYAMLValue(b *strings.Builder, key string, v *yaml.Node) {
	plain := func() {
		var decoded any
		_ = v.Decode(&decoded)
		out, err := json.Marshal(settingsValueFor(key, decoded))
		if err != nil {
			out = []byte("null")
		}
		b.Write(out)
	}
	if key == "tracker.filter" || !commentableYAML(v) {
		plain()
		return
	}
	switch v.Kind {
	case yaml.MappingNode:
		b.WriteString("{\n")
		writeYAMLMembers(b, v, key, nil, true, nil)
		b.WriteString("}")
	case yaml.SequenceNode:
		b.WriteString("[\n")
		for _, e := range v.Content {
			writeComments(b, e.HeadComment)
			writeYAMLValue(b, key, e)
			b.WriteString(",")
			writeLineComment(b, e.LineComment)
			b.WriteString("\n")
			writeComments(b, e.FootComment)
		}
		b.WriteString("]")
	default:
		plain()
	}
}

// commentableYAML reports whether v (and everything in it) can be written
// node by node: no alias and no merge key, which only the decoder resolves.
func commentableYAML(v *yaml.Node) bool {
	if v.Kind == yaml.AliasNode || v.Anchor != "" {
		return false
	}
	for i, c := range v.Content {
		if v.Kind == yaml.MappingNode && i%2 == 0 && c.Value == "<<" {
			return false
		}
		if !commentableYAML(c) {
			return false
		}
	}
	return true
}

// writeComments writes a YAML comment block ("# a\n# b") as // lines.
func writeComments(b *strings.Builder, comment string) {
	for _, line := range strings.Split(comment, "\n") {
		if text, ok := commentText(line); ok {
			b.WriteString("// " + text + "\n")
		}
	}
}

func writeLineComment(b *strings.Builder, comments ...string) {
	for _, c := range comments {
		if text, ok := commentText(c); ok {
			b.WriteString(" // " + text)
		}
	}
}

func commentText(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	// One "#" and one space go; the rest of the line (its alignment, a
	// nested "#") is the comment as written.
	text := strings.TrimPrefix(strings.TrimPrefix(line, "#"), " ")
	return strings.TrimRight(text, " \t"), true
}

// printSettingsMove reports a move (or, dryRun, what a move would do).
func printSettingsMove(out io.Writer, path string, move settingsMove, dryRun bool) {
	verb := map[bool]string{false: "moved", true: "would move"}[dryRun]
	if !move.changed() {
		_, _ = fmt.Fprintf(out, "itervox init --update: no layered settings left in %s's front matter\n", path)
	}
	if len(move.Moved) > 0 {
		_, _ = fmt.Fprintf(out, "itervox init --update: %s %d setting(s) from %s to %s: %s\n", verb, len(move.Moved), path, move.SettingsPath, strings.Join(move.Moved, ", "))
	}
	if len(move.Dropped) > 0 {
		_, _ = fmt.Fprintf(out, "itervox init --update: %s already sets %s; its value (the one in effect) is kept and the WORKFLOW.md copy %s\n",
			move.SettingsPath, strings.Join(move.Dropped, ", "), map[bool]string{false: "was removed", true: "would be removed"}[dryRun])
	}
	for _, k := range move.Kept {
		_, _ = fmt.Fprintf(out, "itervox init --update: kept in %s: %s\n", path, k)
	}
	if !dryRun || !move.changed() {
		return
	}
	if settingsTextHasInlineSecret(move.Settings) {
		// The file already held an inline secret; never print it.
		_, _ = fmt.Fprintf(out, "itervox init --update: --dry-run, nothing was written. %s holds an inline secret, so its new text is not printed\n", move.SettingsPath)
	} else {
		_, _ = fmt.Fprintf(out, "itervox init --update: --dry-run, nothing was written. %s would be:\n", move.SettingsPath)
		_, _ = out.Write(move.Settings)
		if !bytes.HasSuffix(move.Settings, []byte("\n")) {
			_, _ = io.WriteString(out, "\n")
		}
	}
	_, _ = fmt.Fprintf(out, "itervox init --update: %s would keep its prompt template (the body) unchanged, with a backup in %s.bak\n", path, path)
}

// settingsTextHasInlineSecret reports whether a settings file's text holds a
// secret inline (see inlineSecretAt).
func settingsTextHasInlineSecret(text []byte) bool {
	data, err := config.ParseSettings(text)
	if err != nil {
		return true // unknown content: do not print it
	}
	for _, section := range sortedKeys(data) {
		if _, found := inlineSecretAt(section, data[section]); found {
			return true
		}
	}
	return false
}
