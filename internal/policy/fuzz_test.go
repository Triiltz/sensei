package policy

import (
	"strings"
	"testing"
)

// FuzzClassify checks two properties on arbitrary input:
//   - the lexer and classifier never panic;
//   - no prefix can hide a dangerous command that follows it: appending
//     "; rm -rf ~" or "&& sudo x" always yields the dangerous tier.
//
// Run it with: go test ./internal/policy -fuzz=FuzzClassify -fuzztime=60s
func FuzzClassify(f *testing.F) {
	for _, seed := range []string{
		"", "ls", "ls -la", "cat app.log | grep ERROR", "echo 'unterminated", `echo "a | b"`,
		"echo $(date)", "echo `whoami`", "cat <<EOF", "a=1 b=2", "( ls", "ls )", "$(", "`",
		"echo \\", "find . -exec", "xargs", "sh -c", "bash -c 'ls", "git -c", "#comment",
		"2>&1", ">", "|", "&&", ";;", "{ ls; }", "for f in *; do", ":(){ :|:& };:",
		"echo '#'; ls", "ls # trailing", "cat < ", "echo hi >| out", "diff <(ls) >(cat)",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		// Must not panic, and input the lexer cannot parse is never safe.
		if d := Classify(s); lex(s).malformed && d.Tier == Safe {
			t.Errorf("Classify(%q) is malformed but classified safe", s)
		}

		for _, suffix := range []string{"; rm -rf ~", " && sudo x"} {
			cmd := s + suffix
			d := Classify(cmd)
			if d.Tier == Dangerous || hidesSuffix(s) {
				continue
			}
			t.Errorf("Classify(%q) = %s (%s), want dangerous", cmd, d.Tier, d.Reason)
		}
	})
}

// hidesSuffix reports whether s legitimately turns what follows it into
// inert text, so the shell would not run the appended command either: an
// unterminated quote or substitution (a syntax error, and malformed input is
// never classified safe), a trailing backslash escaping the separator, or an
// open comment. A heredoc is not an exception: its body starts on the next
// line, so "cat <<EOF; rm -rf ~" does run rm.
func hidesSuffix(s string) bool {
	return lex(s).malformed || endsEscaped(s) || commentAtEnd(s)
}

// commentAtEnd reports whether text appended to s would land in a comment.
func commentAtEnd(s string) bool {
	const marker = "SENSEIMARKER"
	p := lex(s + " " + marker)
	for _, seg := range p.segments {
		for _, w := range seg.words {
			if strings.Contains(w.text, marker) {
				return false
			}
		}
		for _, r := range seg.redirects {
			if strings.Contains(r.target, marker) {
				return false
			}
		}
	}
	return true
}

func endsEscaped(s string) bool {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}
