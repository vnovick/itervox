package guardrails

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A small tokenizer for JS/TS/JSON config files read whole (jest.config.*,
// vitest.config.*, vite.config.*, package.json): enough to find the string
// literals of the arrays a test runner reads its test paths from, with
// their line numbers, wherever in the file the array was opened. It is not
// a JavaScript parser: it knows strings, template literals (with nested
// ${…}), comments, regex literals, brackets and object keys, and refuses a
// file it cannot read (an unterminated string, comment, template or regex,
// unbalanced brackets), so the caller falls back to the hunk heuristic.

type jsTokKind uint8

const (
	jsPunct jsTokKind = iota
	jsString
	jsIdent // identifiers, keywords and numbers
)

type jsTok struct {
	kind jsTokKind
	text string // a string's value, a punctuator, an identifier
	line int    // 1-based
}

var errJSUnterminated = errors.New("unterminated string, template, comment or regex")

// regexAfterWord are the keywords after which a / starts a regex literal.
var regexAfterWord = map[string]bool{
	"return": true, "typeof": true, "case": true, "do": true, "else": true, "in": true,
	"of": true, "new": true, "delete": true, "void": true, "throw": true, "yield": true, "await": true,
}

func isJSWordByte(c byte) bool { return isWordByte(c) || c >= utf8.RuneSelf }

// tokenizeJS splits src into tokens, dropping whitespace and comments.
func tokenizeJS(src string) ([]jsTok, error) {
	var toks []jsTok
	line := 1
	regexOK := func() bool {
		if len(toks) == 0 {
			return true
		}
		t := toks[len(toks)-1]
		switch t.kind {
		case jsPunct:
			return t.text != ")" && t.text != "]" && t.text != "}"
		case jsIdent:
			return regexAfterWord[t.text]
		}
		return false
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, errJSUnterminated
			}
			line += strings.Count(src[i:i+2+end], "\n")
			i += 2 + end + 2
		case c == '\'' || c == '"':
			start := line
			end, err := scanJSQuoted(src, i, &line)
			if err != nil {
				return nil, err
			}
			toks = append(toks, jsTok{jsString, unescapeJS(src[i+1 : end-1]), start})
			i = end
		case c == '`':
			start := line
			end, err := scanJSTemplate(src, i, &line)
			if err != nil {
				return nil, err
			}
			toks = append(toks, jsTok{jsString, src[i+1 : end-1], start})
			i = end
		case c == '/' && regexOK():
			end, err := scanJSRegex(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, jsTok{jsIdent, src[i:end], line})
			i = end
		case isJSWordByte(c):
			j := i + 1
			for j < len(src) && (isJSWordByte(src[j]) || c >= '0' && c <= '9' && src[j] == '.') {
				j++
			}
			toks = append(toks, jsTok{jsIdent, src[i:j], line})
			i = j
		case strings.HasPrefix(src[i:], "..."):
			toks = append(toks, jsTok{jsPunct, "...", line})
			i += 3
		default:
			toks = append(toks, jsTok{jsPunct, src[i : i+1], line})
			i++
		}
	}
	return toks, nil
}

// scanJSQuoted returns the index just past the '…' or "…" string at i.
func scanJSQuoted(src string, i int, line *int) (int, error) {
	q := src[i]
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
			if j < len(src) && src[j] == '\n' {
				*line++ // a line continuation
			}
		case '\n':
			return 0, errJSUnterminated
		case q:
			return j + 1, nil
		}
	}
	return 0, errJSUnterminated
}

// scanJSTemplate returns the index just past the template literal at i,
// skipping its ${…} substitutions (which may hold strings and templates).
func scanJSTemplate(src string, i int, line *int) (int, error) {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
			if j < len(src) && src[j] == '\n' {
				*line++
			}
		case '\n':
			*line++
		case '`':
			return j + 1, nil
		case '$':
			if j+1 < len(src) && src[j+1] == '{' {
				end, err := skipJSExpr(src, j+2, line)
				if err != nil {
					return 0, err
				}
				j = end - 1
			}
		}
	}
	return 0, errJSUnterminated
}

// skipJSExpr skips a ${…} substitution's expression from i to just past
// its closing brace.
func skipJSExpr(src string, i int, line *int) (int, error) {
	depth := 1
	for i < len(src) {
		var err error
		switch c := src[i]; {
		case c == '\n':
			*line++
			i++
		case c == '\'' || c == '"':
			i, err = scanJSQuoted(src, i, line)
		case c == '`':
			i, err = scanJSTemplate(src, i, line)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return 0, errJSUnterminated
			}
			*line += strings.Count(src[i:i+2+end], "\n")
			i += 2 + end + 2
		case c == '{':
			depth++
			i++
		case c == '}':
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
	return 0, errJSUnterminated
}

// scanJSRegex returns the index just past the regex literal (and its
// flags) at i.
func scanJSRegex(src string, i int) (int, error) {
	inClass := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '\n':
			return 0, errJSUnterminated
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				j++
				for j < len(src) && isWordByte(src[j]) {
					j++
				}
				return j, nil
			}
		}
	}
	return 0, errJSUnterminated
}

// unescapeJS decodes a single- or double-quoted string's escapes (JSON's
// are a subset).
func unescapeJS(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '0':
			b.WriteByte(0)
		case '\n': // a line continuation
		case 'x', 'u':
			n := 2
			if c == 'u' {
				n = 4
			}
			hex := ""
			switch {
			case c == 'u' && i+1 < len(s) && s[i+1] == '{':
				if end := strings.IndexByte(s[i:], '}'); end > 0 {
					hex, i = s[i+2:i+end], i+end
				}
			case i+n < len(s):
				hex, i = s[i+1:i+1+n], i+n
			}
			if r, err := strconv.ParseUint(hex, 16, 32); err == nil {
				b.WriteRune(rune(r))
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// jsArrayString is a string literal inside an array of a config file.
type jsArrayString struct {
	value string
	line  int
	// keys are the keys of the brackets around it, outermost first, as
	// object member keys: "" for a bracket that is no member's value (an
	// array element, the argument of defineConfig(…)). An array is keyed by
	// the member whose value it is part of, so in
	// `exclude: [...a, ...(ci ? ['x'] : [])]` 'x' is in exclude.
	keys []string
	// opens are the brackets around it ('{', '['), outermost first,
	// parallel to keys; parentheses are left out.
	opens []byte
}

type cfgFrame struct {
	open   byte
	key    string // the member key this bracket is (part of) the value of
	member string // '{' only: the key of the member being read
	// spread: a '[' reached from an enclosing '[' through parentheses
	// only (`[...a, ...(ci ? ['x'] : [])]`): its elements are the outer
	// array's, so it is not a bracket of its own.
	spread bool
}

// jsConfigArrays reads every string literal that is an element of an
// array in src, wherever the array was opened.
func jsConfigArrays(src string) ([]jsArrayString, error) {
	toks, err := tokenizeJS(src)
	if err != nil {
		return nil, err
	}
	var (
		stack []cfgFrame
		out   []jsArrayString
	)
	closes := map[string]byte{"}": '{', "]": '[', ")": '('}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		top := func() *cfgFrame {
			if len(stack) == 0 {
				return nil
			}
			return &stack[len(stack)-1]
		}
		// An object member's key: a string or name right after { or , in
		// an object literal, followed by a colon.
		if f := top(); f != nil && f.open == '{' && t.kind != jsPunct && i+1 < len(toks) &&
			toks[i+1].kind == jsPunct && toks[i+1].text == ":" && i > 0 &&
			toks[i-1].kind == jsPunct && (toks[i-1].text == "{" || toks[i-1].text == ",") {
			f.member = t.text
			i++
			continue
		}
		switch {
		case t.kind == jsPunct && (t.text == "{" || t.text == "[" || t.text == "("):
			key := ""
			if f := top(); f != nil {
				switch {
				case f.open == '{':
					key = f.member
				case f.open == '(' || t.text == "(":
					key = f.key
				}
			}
			spread := false
			if t.text == "[" && len(stack) > 0 && stack[len(stack)-1].open == '(' {
				for j := len(stack) - 1; j >= 0; j-- {
					if stack[j].open != '(' {
						spread = stack[j].open == '['
						break
					}
				}
			}
			stack = append(stack, cfgFrame{open: t.text[0], key: key, spread: spread})
		case t.kind == jsPunct && closes[t.text] != 0:
			f := top()
			if f == nil || f.open != closes[t.text] {
				return nil, errors.New("unbalanced brackets")
			}
			stack = stack[:len(stack)-1]
		case t.kind == jsPunct && t.text == ",":
			if f := top(); f != nil && f.open == '{' {
				f.member = ""
			}
		case t.kind == jsString:
			if f := top(); f != nil && f.open == '[' {
				s := jsArrayString{value: t.text, line: t.line}
				for _, fr := range stack {
					if fr.open != '(' && !fr.spread {
						s.keys = append(s.keys, fr.key)
						s.opens = append(s.opens, fr.open)
					}
				}
				out = append(out, s)
			}
		}
	}
	if len(stack) > 0 {
		return nil, errors.New("unbalanced brackets")
	}
	return out, nil
}
