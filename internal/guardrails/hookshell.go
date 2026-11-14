package guardrails

import (
	"strings"
)

// A PreToolUse hook (itervox hook) sees an agent's shell command as the
// string the agent wrote, before the shell expands it, unlike the
// exec-time shims. This file splits that string into the simple commands
// the shell would run, well enough to apply the guardrail rules to each:
// quotes and escapes are resolved, `;`, `&&`, `||`, `|`, `&`, newlines and
// subshell parentheses separate commands, redirections and heredoc bodies
// are dropped, and the bodies of `$(…)`, backticks, `<(…)`, `bash -c '…'`
// and `eval` are checked as commands of their own. Anything the shell
// would compute at run time (`$VAR`, `$(…)`) is marked dynamic. It is a
// best-effort reading, not a shell: the shims are the enforcement.

// shellWord is one word of a command. Dynamic: part of it is expanded at
// run time, so Text is what the agent wrote there, not what will run.
type shellWord struct {
	Text    string
	Dynamic bool
}

// shellCommand is one simple command: its words, in order (leading
// NAME=value assignments included).
type shellCommand struct {
	Words []shellWord
}

// shellParse is a command line read: its simple commands and the command
// strings nested in it (substitutions), to be read the same way.
type shellParse struct {
	Commands []shellCommand
	Nested   []string
}

type shellLexer struct {
	s   string
	i   int
	out shellParse

	cur      strings.Builder
	inWord   bool
	dynamic  bool
	words    []shellWord
	skipNext bool     // the next word is a redirection target
	heredocs []string // delimiters whose bodies start after the next newline
	heredoc  bool     // the word being read is a heredoc delimiter
}

// parseShellLine splits a command line into simple commands.
func parseShellLine(s string) shellParse {
	l := &shellLexer{s: s}
	l.run()
	return l.out
}

func (l *shellLexer) endWord() {
	if !l.inWord {
		return
	}
	w := shellWord{Text: l.cur.String(), Dynamic: l.dynamic}
	l.cur.Reset()
	l.inWord, l.dynamic = false, false
	switch {
	case l.heredoc:
		l.heredoc = false
		l.heredocs = append(l.heredocs, w.Text)
	case l.skipNext:
		l.skipNext = false
	default:
		l.words = append(l.words, w)
	}
}

func (l *shellLexer) endCommand() {
	l.endWord()
	if len(l.words) > 0 {
		l.out.Commands = append(l.out.Commands, shellCommand{Words: l.words})
	}
	l.words = nil
}

func (l *shellLexer) add(b byte) {
	l.inWord = true
	l.cur.WriteByte(b)
}

func (l *shellLexer) peek(off int) byte {
	if l.i+off < len(l.s) {
		return l.s[l.i+off]
	}
	return 0
}

func (l *shellLexer) run() {
	for l.i < len(l.s) {
		c := l.s[l.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			l.endWord()
			l.i++
		case c == '\n':
			l.endCommand()
			l.i++
			l.skipHeredocBodies()
		case c == ';' || c == '|' || c == '(' || c == ')':
			l.endCommand()
			l.i++
		case c == '&':
			if l.peek(1) == '>' { // &> file
				l.endWord()
				l.i += 2
				if l.peek(0) == '>' {
					l.i++
				}
				l.skipNext = true
				continue
			}
			l.endCommand()
			l.i++
		case c == '<' || c == '>':
			l.redirect()
		case c == '#' && !l.inWord:
			for l.i < len(l.s) && l.s[l.i] != '\n' {
				l.i++
			}
		case c == '\\':
			if l.peek(1) == '\n' {
				l.i += 2
				continue
			}
			if l.i+1 < len(l.s) {
				l.add(l.s[l.i+1])
			}
			l.i += 2
		case c == '\'':
			l.inWord = true
			end := strings.IndexByte(l.s[l.i+1:], '\'')
			if end < 0 {
				l.cur.WriteString(l.s[l.i+1:])
				l.i = len(l.s)
				continue
			}
			l.cur.WriteString(l.s[l.i+1 : l.i+1+end])
			l.i += end + 2
		case c == '"':
			l.doubleQuoted()
		case c == '$':
			l.dollar()
		case c == '`':
			l.backtick()
		default:
			l.add(c)
			l.i++
		}
	}
	l.endCommand()
}

// redirect drops a redirection and its target: `> f`, `2>&1`, `<<EOF`
// (whose body is skipped), `<<< word`, and reads `<(…)` / `>(…)` as
// nested commands.
func (l *shellLexer) redirect() {
	c := l.s[l.i]
	if l.peek(1) == '(' { // process substitution
		l.inWord = true
		l.dynamic = true
		inner, end := scanBalanced(l.s, l.i+2)
		l.out.Nested = append(l.out.Nested, inner)
		l.cur.WriteString("<(…)")
		l.i = end
		return
	}
	// A word made only of digits right before the operator is its fd.
	if l.inWord && !l.dynamic && isDigits(l.cur.String()) {
		l.cur.Reset()
		l.inWord = false
	}
	l.endWord()
	l.i++
	if c == '<' && l.peek(0) == '<' {
		l.i++
		if l.peek(0) == '<' { // here-string
			l.i++
			l.skipNext = true
			return
		}
		if l.peek(0) == '-' {
			l.i++
		}
		l.heredoc = true
		return
	}
	for l.i < len(l.s) && strings.IndexByte(">&|", l.s[l.i]) >= 0 {
		l.i++
	}
	l.skipNext = true
}

// skipHeredocBodies skips the bodies of the heredocs opened on the line
// just ended: up to each delimiter line (leading tabs ignored, for <<-).
func (l *shellLexer) skipHeredocBodies() {
	for _, delim := range l.heredocs {
		for l.i < len(l.s) {
			end := strings.IndexByte(l.s[l.i:], '\n')
			line := l.s[l.i:]
			if end >= 0 {
				line = l.s[l.i : l.i+end]
				l.i += end + 1
			} else {
				l.i = len(l.s)
			}
			if strings.TrimLeft(line, "\t") == delim {
				break
			}
		}
	}
	l.heredocs = nil
}

func (l *shellLexer) doubleQuoted() {
	l.inWord = true
	l.i++
	for l.i < len(l.s) {
		c := l.s[l.i]
		switch {
		case c == '"':
			l.i++
			return
		case c == '\\' && l.i+1 < len(l.s) && strings.IndexByte("$`\"\\\n", l.s[l.i+1]) >= 0:
			if l.s[l.i+1] != '\n' {
				l.cur.WriteByte(l.s[l.i+1])
			}
			l.i += 2
		case c == '$':
			l.dollar()
		case c == '`':
			l.backtick()
		default:
			l.cur.WriteByte(c)
			l.i++
		}
	}
}

// dollar reads a `$` expansion at l.i: `$(…)` (a nested command), `$((…))`,
// `${…}`, `$NAME` and the special parameters are dynamic; `$'…'` is
// quoted text; a lone `$` is literal.
func (l *shellLexer) dollar() {
	l.inWord = true
	next := l.peek(1)
	switch {
	case next == '(' && l.peek(2) == '(':
		_, end := scanBalanced(l.s, l.i+2)
		l.cur.WriteString(l.s[l.i:min(end, len(l.s))])
		l.dynamic = true
		l.i = end
	case next == '(':
		inner, end := scanBalanced(l.s, l.i+2)
		l.out.Nested = append(l.out.Nested, inner)
		l.cur.WriteString("$(…)")
		l.dynamic = true
		l.i = end
	case next == '{':
		end := strings.IndexByte(l.s[l.i:], '}')
		if end < 0 {
			end = len(l.s) - l.i - 1
		}
		l.cur.WriteString(l.s[l.i : l.i+end+1])
		l.dynamic = true
		l.i += end + 1
	case next == '\'':
		end := strings.IndexByte(l.s[l.i+2:], '\'')
		if end < 0 {
			l.cur.WriteString(l.s[l.i+2:])
			l.i = len(l.s)
			return
		}
		l.cur.WriteString(l.s[l.i+2 : l.i+2+end])
		l.i += end + 3
	case isNameStart(next):
		j := l.i + 1
		for j < len(l.s) && isNameChar(l.s[j]) {
			j++
		}
		l.cur.WriteString(l.s[l.i:j])
		l.dynamic = true
		l.i = j
	case next != 0 && strings.IndexByte("?#@*!$-0123456789", next) >= 0:
		l.cur.WriteString(l.s[l.i : l.i+2])
		l.dynamic = true
		l.i += 2
	default:
		l.cur.WriteByte('$')
		l.i++
	}
}

func (l *shellLexer) backtick() {
	l.inWord = true
	l.dynamic = true
	var inner strings.Builder
	j := l.i + 1
	for j < len(l.s) && l.s[j] != '`' {
		if l.s[j] == '\\' && j+1 < len(l.s) {
			inner.WriteByte(l.s[j+1])
			j += 2
			continue
		}
		inner.WriteByte(l.s[j])
		j++
	}
	l.out.Nested = append(l.out.Nested, inner.String())
	l.cur.WriteString("`…`")
	l.i = min(j+1, len(l.s))
}

// scanBalanced returns the text from start up to the `)` that closes an
// already-open `(`, and the index just past it; quotes are honoured.
func scanBalanced(s string, start int) (string, int) {
	depth := 1
	for j := start; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '\'':
			if end := strings.IndexByte(s[j+1:], '\''); end >= 0 {
				j += end + 1
			} else {
				return s[start:], len(s)
			}
		case '"':
			for j++; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' {
					j++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[start:j], j + 1
			}
		}
	}
	return s[start:], len(s)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

// splitAssignment splits a NAME=value word; ok is false for any other word.
func splitAssignment(w string) (name, value string, ok bool) {
	name, value, found := strings.Cut(w, "=")
	if !found || name == "" || !isNameStart(name[0]) {
		return "", "", false
	}
	for i := 1; i < len(name); i++ {
		if !isNameChar(name[i]) {
			return "", "", false
		}
	}
	return name, value, true
}
