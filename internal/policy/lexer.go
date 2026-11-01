package policy

import (
	"strings"
	"unicode"
)

// word is one shell word after quote removal.
type word struct {
	text   string
	expand bool // contains $VAR, $(...) or `...` outside single quotes
	glob   bool // contains an unquoted *, ? or [
}

// redirect is a redirection operator and its target.
type redirect struct {
	op     string // ">", ">>", ">|", "&>", "<", "<<", "<<<"
	target string
	fdDup  bool // 2>&1 and friends: no file involved
}

// segment is one simple command between separators (|, ||, &&, ;, &, newline).
type segment struct {
	words     []word
	redirects []redirect
	piped     bool // reads its stdin from the previous command's stdout
}

// parsed is the result of lexing a command line.
type parsed struct {
	segments      []segment
	substitutions []string // bodies of $(...), `...`, <(...) and >(...)
	funcDef       bool     // name() { ... } — never needed to investigate
	malformed     bool     // unterminated quote or substitution, dangling redirect
}

// lex splits a command line the way a POSIX shell would, closely enough to
// classify it. It does not need to be a full parser: anything it does not
// understand is reported as malformed, and malformed commands are escalated.
func lex(s string) parsed {
	l := &lexer{src: []rune(s)}
	l.run()
	return l.out
}

type lexer struct {
	src []rune
	pos int
	out parsed

	cur     segment
	buf     strings.Builder
	inWord  bool
	wExpand bool
	wGlob   bool

	pendingOp string // a redirection waiting for its target word
}

func (l *lexer) peek(off int) rune {
	if l.pos+off < len(l.src) {
		return l.src[l.pos+off]
	}
	return 0
}

func (l *lexer) run() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\'':
			l.inWord = true
			end := indexRune(l.src, l.pos+1, '\'')
			if end < 0 {
				l.out.malformed = true
				l.buf.WriteString(string(l.src[l.pos+1:]))
				l.pos = len(l.src)
				continue
			}
			l.buf.WriteString(string(l.src[l.pos+1 : end]))
			l.pos = end + 1

		case c == '"':
			l.inWord = true
			l.pos++
			l.doubleQuoted()

		case c == '\\':
			l.inWord = true
			if l.pos+1 < len(l.src) {
				if l.src[l.pos+1] != '\n' { // backslash-newline is a line continuation
					l.buf.WriteRune(l.src[l.pos+1])
				}
				l.pos += 2
			} else {
				l.pos++
			}

		case c == '$':
			l.dollar()

		case c == '`':
			l.backtick()

		case c == '#' && !l.inWord:
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}

		case c == '\n' || c == ';':
			l.endSegment(false)
			l.pos++

		case c == '|':
			if l.peek(1) == '|' {
				l.endSegment(false)
				l.pos += 2
			} else {
				l.endSegment(true)
				l.pos++
				if l.peek(0) == '&' { // |& pipes stderr too
					l.pos++
				}
			}

		case c == '&':
			switch l.peek(1) {
			case '&':
				l.endSegment(false)
				l.pos += 2
			case '>':
				l.startRedirect("&>", 2)
				if l.peek(0) == '>' {
					l.pos++
				}
			default:
				l.endSegment(false) // background
				l.pos++
			}

		case c == '(' || c == ')':
			if c == '(' && l.peek(1) == ')' {
				l.out.funcDef = true
			}
			l.endSegment(false)
			l.pos++

		case c == '<':
			l.lessThan()

		case c == '>':
			l.greaterThan()

		case unicode.IsSpace(c):
			l.endWord()
			l.pos++

		default:
			l.inWord = true
			if c == '*' || c == '?' || c == '[' {
				l.wGlob = true
			}
			l.buf.WriteRune(c)
			l.pos++
		}
	}
	l.endSegment(false)
	if l.pendingOp != "" {
		l.out.malformed = true
	}
}

func (l *lexer) doubleQuoted() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.pos++
			return
		case '\\':
			if next := l.peek(1); strings.ContainsRune("$`\"\\\n", next) && next != 0 {
				l.buf.WriteRune(next)
				l.pos += 2
				continue
			}
			l.buf.WriteRune(c)
			l.pos++
		case '$':
			l.dollar()
		case '`':
			l.backtick()
		default:
			l.buf.WriteRune(c)
			l.pos++
		}
	}
	l.out.malformed = true // unterminated double quote
}

// dollar handles $VAR, ${VAR}, $(cmd) and $((arithmetic)).
func (l *lexer) dollar() {
	l.inWord = true
	l.wExpand = true
	if l.peek(1) == '(' && l.peek(2) != '(' {
		body, end, ok := matchParen(l.src, l.pos+2)
		if !ok {
			l.out.malformed = true
			l.pos = len(l.src)
			return
		}
		l.out.substitutions = append(l.out.substitutions, body)
		l.buf.WriteString("$(...)")
		l.pos = end + 1
		return
	}
	l.buf.WriteRune('$')
	l.pos++
}

func (l *lexer) backtick() {
	l.inWord = true
	l.wExpand = true
	end := indexRune(l.src, l.pos+1, '`')
	if end < 0 {
		l.out.malformed = true
		l.pos = len(l.src)
		return
	}
	l.out.substitutions = append(l.out.substitutions, string(l.src[l.pos+1:end]))
	l.buf.WriteString("$(...)")
	l.pos = end + 1
}

func (l *lexer) lessThan() {
	switch {
	case l.peek(1) == '(': // process substitution <(cmd)
		body, end, ok := matchParen(l.src, l.pos+2)
		if !ok {
			l.out.malformed = true
			l.pos = len(l.src)
			return
		}
		l.out.substitutions = append(l.out.substitutions, body)
		l.inWord, l.wExpand = true, true
		l.buf.WriteString("$(...)")
		l.pos = end + 1
	case l.peek(1) == '<' && l.peek(2) == '<':
		l.startRedirect("<<<", 3)
	case l.peek(1) == '<':
		l.startRedirect("<<", 2)
		if l.peek(0) == '-' {
			l.pos++
		}
	default:
		l.startRedirect("<", 1)
	}
}

func (l *lexer) greaterThan() {
	if l.peek(1) == '(' { // process substitution >(cmd)
		body, end, ok := matchParen(l.src, l.pos+2)
		if !ok {
			l.out.malformed = true
			l.pos = len(l.src)
			return
		}
		l.out.substitutions = append(l.out.substitutions, body)
		l.inWord, l.wExpand = true, true
		l.buf.WriteString("$(...)")
		l.pos = end + 1
		return
	}
	switch l.peek(1) {
	case '>':
		l.startRedirect(">>", 2)
	case '|':
		l.startRedirect(">|", 2)
	case '&':
		l.startRedirect(">&", 2)
	default:
		l.startRedirect(">", 1)
	}
}

// startRedirect consumes an operator of length n. A word made only of digits
// right before it (as in 2>) is a file descriptor, not an argument.
func (l *lexer) startRedirect(op string, n int) {
	if l.inWord && isDigits(l.buf.String()) {
		l.resetWord()
	} else {
		l.endWord()
	}
	if l.pendingOp != "" {
		l.out.malformed = true
	}
	l.pendingOp = op
	l.pos += n
}

func (l *lexer) endWord() {
	if !l.inWord {
		return
	}
	w := word{text: l.buf.String(), expand: l.wExpand, glob: l.wGlob}
	l.resetWord()

	if l.pendingOp != "" {
		r := redirect{op: l.pendingOp, target: w.text}
		if (r.op == ">&" || r.op == "<&") && (isDigits(w.text) || w.text == "-") {
			r.fdDup = true
		}
		if r.op == ">&" && !r.fdDup {
			r.op = ">"
		}
		l.cur.redirects = append(l.cur.redirects, r)
		l.pendingOp = ""
		return
	}
	l.cur.words = append(l.cur.words, w)
}

func (l *lexer) resetWord() {
	l.buf.Reset()
	l.inWord, l.wExpand, l.wGlob = false, false, false
}

func (l *lexer) endSegment(pipeNext bool) {
	l.endWord()
	if l.pendingOp != "" {
		l.out.malformed = true
		l.pendingOp = ""
	}
	if len(l.cur.words) > 0 || len(l.cur.redirects) > 0 {
		l.out.segments = append(l.out.segments, l.cur)
	}
	l.cur = segment{piped: pipeNext}
}

// matchParen returns the text up to the parenthesis closing one opened just
// before start, honouring nesting and quotes.
func matchParen(src []rune, start int) (string, int, bool) {
	depth := 1
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '\'':
			end := indexRune(src, i+1, '\'')
			if end < 0 {
				return "", 0, false
			}
			i = end
		case '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return string(src[start:i]), i, true
			}
		}
	}
	return "", 0, false
}

func indexRune(src []rune, from int, r rune) int {
	for i := from; i < len(src); i++ {
		if src[i] == r {
			return i
		}
	}
	return -1
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
