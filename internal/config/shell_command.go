package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// AgentCommandShape says where a runner puts its own arguments in an agent
// command (agent.command or a profile's command), which runs through a
// login shell (`bash -lc`) or the SSH worker's remote bash.
//
// A simple command (`claude --model x`) takes them at its end, as it always
// did. A command that is not a simple command — a brace group
// `{ source ~/.nvm/nvm.sh; claude; }`, a subshell `(cd sub && claude)`, an
// `&&` / `;` list — takes them after the last word of its final simple
// command, before whatever closes it, so `{ a; claude; }` becomes
// `{ a; claude <flags>; }` rather than `{ a; claude; } <flags>`.
type AgentCommandShape struct {
	// Compound is true when the command is more than one simple command.
	Compound bool
	// Program is the program word of the final simple command, as written:
	// env assignments, an `env [-flags] [VAR=val…]` prefix and `exec` /
	// `command` are skipped.
	Program string
	// ProgramEnd is the byte offset just after Program (where OpenCode's
	// `run` subcommand goes).
	ProgramEnd int
	// ArgsAt is the byte offset where the runner's arguments go: after the
	// last word of the final simple command.
	ArgsAt int
}

// errAgentCommandHint ends every refusal: the forms that always work.
const errAgentCommandHint = `; put the setup in a wrapper script that ends in exec claude "$@" (or exec codex "$@", …) and use the script as the command, or write it as a brace group or && list whose last command is the agent CLI`

// ValidateAgentCommand refuses an agent command whose runner arguments
// would have no predictable place (ParseAgentCommand). An empty command is
// accepted: it falls back to the backend's binary.
func ValidateAgentCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	_, err := ParseAgentCommand(command)
	return err
}

// ValidateAgentCommands is ValidateAgentCommand for agent.command and every
// profile's command, with the field named in the error.
func ValidateAgentCommands(defaultCommand string, profiles map[string]AgentProfile) error {
	if err := ValidateAgentCommand(defaultCommand); err != nil {
		return fmt.Errorf("agent.command: %w", err)
	}
	for name, profile := range profiles {
		if err := ValidateAgentCommand(profile.Command); err != nil {
			return fmt.Errorf("invalid profile %q: %w", name, err)
		}
	}
	return nil
}

// SpliceAgentArgs returns command with subcommand (when non-empty) inserted
// after the final simple command's program and tail appended after its last
// word. tail is the runner's argument string with its leading space
// (" --output-format stream-json … -p < file"). It errors when the command
// has no predictable place for them (ParseAgentCommand).
func SpliceAgentArgs(command, subcommand, tail string) (string, error) {
	shape, err := ParseAgentCommand(command)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(len(command) + len(subcommand) + len(tail) + 1)
	if subcommand != "" {
		b.WriteString(command[:shape.ProgramEnd])
		b.WriteString(" " + subcommand)
		b.WriteString(command[shape.ProgramEnd:shape.ArgsAt])
	} else {
		b.WriteString(command[:shape.ArgsAt])
	}
	b.WriteString(tail)
	b.WriteString(command[shape.ArgsAt:])
	return strings.TrimRight(b.String(), " \t\n"), nil
}

// ParseAgentCommand finds where a runner's arguments go in command (see
// AgentCommandShape). It reads the command the way bash does — quotes,
// backslashes, $(…), ${…}, backticks, comments, redirections, `;` `&&`
// `||` `|` `&` lists, `{ …; }` groups and `( … )` subshells — without
// running anything, and refuses the shapes where the place is ambiguous:
//
//   - the final command is in a pipeline (`claude | tee log`), runs in the
//     background (`claude &`), or runs only when something fails
//     (`setup || claude`);
//   - a known agent CLI (claude, codex, opencode, cursor-agent) runs, but
//     not last (`claude; echo done`);
//   - if/for/while/case/function and other compound commands, here-documents,
//     a group closed without `;` (`{ claude }`), unbalanced quotes or
//     brackets.
func ParseAgentCommand(command string) (AgentCommandShape, error) {
	toks, err := lexShell(command)
	if err != nil {
		return AgentCommandShape{}, agentCommandError(command, err.Error())
	}
	cmds, compound, err := parseShellList(toks)
	if err != nil {
		return AgentCommandShape{}, agentCommandError(command, err.Error())
	}
	if len(cmds) == 0 {
		return AgentCommandShape{}, agentCommandError(command, "it runs no program")
	}
	final := cmds[len(cmds)-1]
	prog, ok := final.program()
	if !ok {
		return AgentCommandShape{}, agentCommandError(command, "its last command runs no program")
	}
	if final.after == "&" || final.trailingAmp {
		return AgentCommandShape{}, agentCommandError(command, "its last command runs in the background (&)")
	}
	for _, op := range append([]string{final.before}, final.enclosing...) {
		switch op {
		case "|", "|&":
			return AgentCommandShape{}, agentCommandError(command, "its last command is part of a pipeline (|), so the agent's output would not reach itervox")
		case "||":
			return AgentCommandShape{}, agentCommandError(command, "its last command runs only when an earlier one fails (||)")
		}
	}
	if final.pipedOut {
		return AgentCommandShape{}, agentCommandError(command, "its last command is part of a pipeline (|), so the agent's output would not reach itervox")
	}
	if BackendForBinary(filepath.Base(prog.text)) == "" {
		for _, c := range cmds[:len(cmds)-1] {
			if p, ok := c.program(); ok && BackendForBinary(filepath.Base(p.text)) != "" {
				return AgentCommandShape{}, agentCommandError(command, fmt.Sprintf("the agent CLI %s is not its last command", p.text))
			}
		}
	}
	last := final.words[len(final.words)-1]
	return AgentCommandShape{
		Compound:   compound,
		Program:    prog.text,
		ProgramEnd: prog.end,
		ArgsAt:     last.end,
	}, nil
}

func agentCommandError(command, reason string) error {
	return fmt.Errorf("command %q: the runner's flags have no predictable place: %s%s", command, reason, errAgentCommandHint)
}

type shTokKind int

const (
	shWord shTokKind = iota
	shOp
	shRedir
)

type shTok struct {
	kind       shTokKind
	text       string
	start, end int
}

// shMeta are the characters that end an unquoted word.
const shMeta = " \t\n;&|()<>"

// lexShell splits a command line into words, control operators and
// redirection operators. Quoted text, $(…), ${…} and backticks stay inside
// their word.
func lexShell(s string) ([]shTok, error) {
	var toks []shTok
	i := 0
	for i < len(s) {
		switch c := s[i]; c {
		case ' ', '\t', '\r':
			i++
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '\n':
			toks = append(toks, shTok{shOp, "\n", i, i + 1})
			i++
		case ';', '&', '|', '(', ')':
			op := shOperator(s[i:])
			kind := shOp
			if op == "&>" || op == "&>>" {
				kind = shRedir
			}
			toks = append(toks, shTok{kind, op, i, i + len(op)})
			i += len(op)
		case '<', '>':
			op := shRedirOperator(s[i:])
			toks = append(toks, shTok{shRedir, op, i, i + len(op)})
			i += len(op)
		default:
			end, err := scanShellWord(s, i)
			if err != nil {
				return nil, err
			}
			word := s[i:end]
			if end < len(s) && (s[end] == '<' || s[end] == '>') && isDigits(word) {
				op := shRedirOperator(s[end:])
				toks = append(toks, shTok{shRedir, word + op, i, end + len(op)})
				i = end + len(op)
				continue
			}
			toks = append(toks, shTok{shWord, word, i, end})
			i = end
		}
	}
	return toks, nil
}

func shOperator(s string) string {
	for _, op := range []string{"&&", "||", ";;", "|&", "&>>", "&>"} {
		if strings.HasPrefix(s, op) {
			return op
		}
	}
	return s[:1]
}

func shRedirOperator(s string) string {
	for _, op := range []string{"<<<", "<<-", "<<", "<>", "<&", ">>", ">&", ">|"} {
		if strings.HasPrefix(s, op) {
			return op
		}
	}
	return s[:1]
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

var errUnterminated = errors.New("it has an unterminated quote, $( or backtick")

// scanShellWord returns the end of the word starting at i.
func scanShellWord(s string, i int) (int, error) {
	for i < len(s) && !strings.ContainsRune(shMeta, rune(s[i])) {
		var err error
		switch s[i] {
		case '\\':
			if i+1 >= len(s) {
				return 0, errors.New("it ends in a backslash")
			}
			i += 2
		case '\'':
			i, err = skipSingleQuoted(s, i)
		case '"':
			i, err = skipDoubleQuoted(s, i)
		case '`':
			i, err = skipBacktick(s, i)
		case '$':
			i, err = skipDollar(s, i)
		default:
			i++
		}
		if err != nil {
			return 0, err
		}
	}
	return i, nil
}

func skipSingleQuoted(s string, i int) (int, error) {
	j := strings.IndexByte(s[i+1:], '\'')
	if j < 0 {
		return 0, errUnterminated
	}
	return i + 1 + j + 1, nil
}

func skipDoubleQuoted(s string, i int) (int, error) {
	i++
	for i < len(s) {
		var err error
		switch s[i] {
		case '\\':
			i += 2
		case '"':
			return i + 1, nil
		case '`':
			i, err = skipBacktick(s, i)
		case '$':
			i, err = skipDollar(s, i)
		default:
			i++
		}
		if err != nil {
			return 0, err
		}
	}
	return 0, errUnterminated
}

func skipBacktick(s string, i int) (int, error) {
	i++
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
		case '`':
			return i + 1, nil
		default:
			i++
		}
	}
	return 0, errUnterminated
}

// skipDollar skips $(…), $((…)) and ${…} (balanced, quote-aware); a plain
// $ is one character.
func skipDollar(s string, i int) (int, error) {
	if i+1 >= len(s) || (s[i+1] != '(' && s[i+1] != '{') {
		return i + 1, nil
	}
	open, closer := s[i+1], byte(')')
	if open == '{' {
		closer = '}'
	}
	depth := 1
	i += 2
	for i < len(s) {
		var err error
		switch c := s[i]; c {
		case '\\':
			i += 2
		case '\'':
			i, err = skipSingleQuoted(s, i)
		case '"':
			i, err = skipDoubleQuoted(s, i)
		case '`':
			i, err = skipBacktick(s, i)
		case '$':
			i, err = skipDollar(s, i)
		case open:
			depth++
			i++
		case closer:
			depth--
			i++
			if depth == 0 {
				return i, nil
			}
		default:
			i++
		}
		if err != nil {
			return 0, err
		}
	}
	return 0, errUnterminated
}

// shSimple is one simple command of a list.
type shSimple struct {
	words []shTok
	// before / after are the control operators around it ("" at the
	// start or end; "{" / "(" when it opens a group).
	before, after string
	// enclosing are the operators in front of each group around it.
	enclosing []string
	// trailingAmp: a group that ends with this command is backgrounded.
	trailingAmp bool
	// pipedOut: a group that ends with this command is piped onward.
	pipedOut bool
}

// program is the program word: env assignments, `env [-flags]
// [VAR=val…]`, and `exec` / `command` with their flags are skipped.
func (c shSimple) program() (shTok, bool) {
	skipFlags := false
	for _, w := range c.words {
		switch {
		case skipFlags && (strings.HasPrefix(w.text, "-") || IsEnvAssignment(w.text)):
			continue
		case IsEnvAssignment(w.text):
			continue
		case filepath.Base(w.text) == "env" || w.text == "exec" || w.text == "command":
			skipFlags = true
			continue
		}
		return w, true
	}
	return shTok{}, false
}

// shReserved are reserved words (and the [[ / time keywords) that start a
// compound command this parser does not take apart.
var shReserved = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "select": true, "function": true,
	"in": true, "[[": true, "]]": true, "time": true, "!": true, "coproc": true,
}

type shGroup struct {
	open   string // "{" or "("
	before string // the operator in front of it
	fn     bool   // a function body: defined here, not run here
}

// parseShellList turns tokens into the list of simple commands, in order.
// compound is true for anything but one simple command.
func parseShellList(toks []shTok) (cmds []shSimple, compound bool, err error) {
	var (
		cur        *shSimple
		prevOp     string
		stack      []shGroup
		afterGroup bool // the last thing read closed a group
	)
	enclosing := func() []string {
		out := make([]string, 0, len(stack))
		for _, g := range stack {
			out = append(out, g.before)
		}
		return out
	}
	inFunction := func() bool {
		for _, g := range stack {
			if g.fn {
				return true
			}
		}
		return false
	}
	flush := func(op string) {
		if cur != nil && !inFunction() {
			cur.after = op
			cmds = append(cmds, *cur)
		}
		cur = nil
	}
	// afterClose applies an operator that follows a closed group to the
	// group's last command.
	afterClose := func(op string) {
		if len(cmds) == 0 {
			return
		}
		switch op {
		case "&":
			cmds[len(cmds)-1].trailingAmp = true
		case "|", "|&":
			cmds[len(cmds)-1].pipedOut = true
		}
	}
	for idx := 0; idx < len(toks); idx++ {
		t := toks[idx]
		switch t.kind {
		case shWord:
			if cur == nil {
				if afterGroup {
					return nil, false, fmt.Errorf("a word (%s) follows a closed group", t.text)
				}
				switch {
				case t.text == "{":
					compound = true
					stack = append(stack, shGroup{open: "{", before: prevOp})
					prevOp = "{"
					continue
				case t.text == "}":
					if len(stack) == 0 || stack[len(stack)-1].open != "{" {
						return nil, false, errors.New("it closes a group (}) that is not open")
					}
					stack = stack[:len(stack)-1]
					afterGroup = true
					continue
				case shReserved[t.text]:
					return nil, false, fmt.Errorf("it uses %s, a compound command this check does not take apart", t.text)
				}
				cur = &shSimple{before: prevOp, enclosing: enclosing()}
			}
			cur.words = append(cur.words, t)
		case shRedir:
			if strings.HasPrefix(strings.TrimLeft(t.text, "0123456789"), "<<") && !strings.HasSuffix(t.text, "<<<") {
				return nil, false, errors.New("it has a here-document (<<)")
			}
			if idx+1 >= len(toks) || toks[idx+1].kind != shWord {
				return nil, false, fmt.Errorf("its redirection %s has no target", t.text)
			}
			idx++ // the target belongs to the redirection, not the words
			if cur == nil && !afterGroup {
				cur = &shSimple{before: prevOp, enclosing: enclosing()}
			}
		case shOp:
			switch t.text {
			case "(":
				if cur != nil {
					// name() { …; } / name() ( … ): a function definition,
					// whose body runs only where a later command calls it.
					if len(cur.words) != 1 || idx+2 >= len(toks) || toks[idx+1].text != ")" {
						return nil, false, errors.New("it has ( inside a command")
					}
					body := toks[idx+2]
					if body.text != "{" && body.text != "(" {
						return nil, false, errors.New("its function definition has no { …; } or ( … ) body")
					}
					cur, compound = nil, true
					stack = append(stack, shGroup{open: body.text, before: prevOp, fn: true})
					prevOp = body.text
					idx += 2
					continue
				}
				if afterGroup {
					return nil, false, errors.New("( follows a closed group")
				}
				compound = true
				stack = append(stack, shGroup{open: "(", before: prevOp})
				prevOp = "("
			case ")":
				flush(")")
				if len(stack) == 0 || stack[len(stack)-1].open != "(" {
					return nil, false, errors.New("it closes a subshell ()) that is not open")
				}
				stack = stack[:len(stack)-1]
				afterGroup = true
			case ";;":
				return nil, false, errors.New("it has ;; outside a case")
			default: // ; \n && || | |& &
				compound = true
				if cur == nil && !afterGroup && (t.text == "&&" || t.text == "||" || t.text == "|" || t.text == "|&") {
					return nil, false, fmt.Errorf("%s has no command in front of it", t.text)
				}
				if cur == nil && afterGroup {
					afterClose(t.text)
				}
				flush(t.text)
				if t.text == "&&" || t.text == "||" || t.text == "|" || t.text == "|&" {
					if idx+1 >= len(toks) {
						return nil, false, fmt.Errorf("it ends in %s", t.text)
					}
				}
				prevOp = t.text
				afterGroup = false
			}
		}
	}
	flush("")
	if len(stack) > 0 {
		if stack[len(stack)-1].open == "{" {
			return nil, false, errors.New("its { group is not closed (write { …; } with a ; before the })")
		}
		return nil, false, errors.New("its ( subshell is not closed")
	}
	out := cmds[:0]
	for _, c := range cmds {
		if len(c.words) > 0 {
			out = append(out, c)
		}
	}
	return out, compound, nil
}
