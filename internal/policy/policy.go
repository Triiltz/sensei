// Package policy classifies shell commands into safety tiers before they run.
//
// Every command's output is sent to the LLM provider, so "safe" means
// harmless to run and harmless to share — not just read-only. The parser is
// deliberately conservative: when in doubt it escalates, never downgrades.
package policy

import (
	"fmt"
	"path"
	"strings"
)

// Tier is how much scrutiny a command needs before it runs.
type Tier int

const (
	// Safe commands run without asking in agent mode.
	Safe Tier = iota
	// Confirm commands ask [y/N]. This is the default for anything unknown.
	Confirm
	// Dangerous commands ask with a warning; -f never bypasses them.
	Dangerous
)

func (t Tier) String() string {
	switch t {
	case Safe:
		return "safe"
	case Confirm:
		return "confirm"
	default:
		return "dangerous"
	}
}

// Decision is a tier and the reason it was chosen.
type Decision struct {
	Tier   Tier
	Reason string

	// Sensitive marks commands that may expose secrets (sensitive paths, raw
	// devices, environment variables). Their output would reach the
	// provider, so -f does not skip the confirmation even in the confirm tier.
	Sensitive bool
}

// NeedsConfirmation reports whether the user must be asked before running
// the command. force skips the question for the confirm tier only.
func (d Decision) NeedsConfirmation(force bool) bool {
	switch d.Tier {
	case Safe:
		return false
	case Confirm:
		return !force || d.Sensitive
	default:
		return true
	}
}

func sensitive(format string, args ...any) Decision {
	d := decide(Confirm, format, args...)
	d.Sensitive = true
	return d
}

func decide(t Tier, format string, args ...any) Decision {
	return Decision{Tier: t, Reason: fmt.Sprintf(format, args...)}
}

// stricter keeps the stricter decision; on a tie, the first reason wins.
// Sensitivity is never lost when two decisions are combined.
func stricter(a, b Decision) Decision {
	out := a
	if b.Tier > a.Tier {
		out = b
	}
	out.Sensitive = a.Sensitive || b.Sensitive
	return out
}

// maxDepth bounds nested sh -c / eval / $(...) analysis.
const maxDepth = 4

// Classify returns the tier for a shell command line.
func Classify(command string) Decision {
	return classify(command, 0)
}

func classify(command string, depth int) Decision {
	if depth > maxDepth {
		return decide(Dangerous, "too many nested shells to analyse")
	}
	if strings.TrimSpace(command) == "" {
		return decide(Confirm, "empty command")
	}

	p := lex(command)
	d := decide(Safe, "read-only")

	if p.funcDef {
		return decide(Dangerous, "defines a shell function")
	}
	if p.malformed {
		d = stricter(d, decide(Confirm, "could not fully parse the command"))
	}
	for _, sub := range p.substitutions {
		inner := classify(sub, depth+1)
		d = stricter(d, decide(Confirm, "uses command substitution"))
		if inner.Tier == Dangerous {
			d = stricter(d, inner)
		}
	}
	for _, seg := range p.segments {
		d = stricter(d, classifySegment(seg, depth))
	}
	return d
}

func classifySegment(seg segment, depth int) Decision {
	d := decide(Safe, "read-only")

	for _, r := range seg.redirects {
		d = stricter(d, classifyRedirect(r))
	}

	words := seg.words
	// Leading VAR=value assignments can change how the command behaves
	// (PATH, LD_PRELOAD, ...).
	assigned := false
	for len(words) > 0 && isAssignment(words[0].text) {
		assigned = true
		words = words[1:]
	}
	if assigned && len(words) > 0 {
		d = stricter(d, decide(Confirm, "sets environment variables for the command"))
	}
	if len(words) == 0 {
		return d
	}

	return stricter(d, classifyWords(words, seg.piped, depth))
}

func classifyRedirect(r redirect) Decision {
	switch {
	case r.fdDup:
		return decide(Safe, "read-only")
	case r.op == "<" || r.op == "<<" || r.op == "<<<":
		if sensitivePath(r.target) {
			return sensitive("reads a sensitive file (%s)", r.target)
		}
		return decide(Confirm, "uses input redirection")
	case isDevNull(r.target):
		return decide(Safe, "read-only")
	case systemPath(r.target) || startupFile(r.target):
		return decide(Dangerous, "writes to %s", r.target)
	default:
		return decide(Confirm, "writes to a file (%s)", r.target)
	}
}

// classifyWords classifies a simple command: its name and arguments.
func classifyWords(words []word, piped bool, depth int) Decision {
	if words[0].expand {
		return decide(Confirm, "the command name is computed at run time")
	}
	name := path.Base(words[0].text)
	args := words[1:]

	d := classifyArgs(args)

	switch name {
	// Shell keywords: classify what follows.
	case "if", "then", "else", "elif", "do", "while", "until", "!", "{", "}", "time", "nohup", "builtin", "exec":
		if len(args) == 0 {
			return d
		}
		return stricter(d, classifyWords(args, piped, depth))
	case "fi", "done", "esac", ";;":
		return d
	case "for", "case", "select":
		return stricter(d, decide(Confirm, "uses a shell loop or case"))

	case "sudo", "doas", "su", "pkexec":
		return decide(Dangerous, "runs with elevated privileges (%s)", name)
	case "nice", "timeout", "stdbuf", "ionice", "watch":
		if rest := skipOptions(args, 1); len(rest) > 0 {
			return stricter(d, classifyWords(rest, piped, depth))
		}
		return stricter(d, decide(Confirm, "%s without a command", name))
	case "command":
		if len(args) > 0 && (args[0].text == "-v" || args[0].text == "-V") {
			return d
		}
		if len(args) > 0 {
			return stricter(d, classifyWords(args, piped, depth))
		}
		return d
	case "env":
		rest := args
		for len(rest) > 0 && (strings.HasPrefix(rest[0].text, "-") || isAssignment(rest[0].text)) {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return sensitive("env prints environment variables, which often hold secrets")
		}
		return stricter(stricter(d, decide(Confirm, "changes the environment of the command")), classifyWords(rest, piped, depth))

	case "sh", "bash", "zsh", "dash", "ksh", "fish", "ash":
		return stricter(d, classifyShell(name, args, piped, depth))
	case "eval":
		inner := classify(joinWords(args), depth+1)
		return stricter(d, wrapped("eval", inner))
	case "source", ".":
		return stricter(d, decide(Confirm, "runs a script file"))
	case "xargs":
		return stricter(d, classifyXargs(args, depth))
	case "python", "python3", "perl", "ruby", "node", "php", "lua", "deno":
		if piped && !hasScriptArg(args) {
			return decide(Dangerous, "pipes data into %s to be executed", name)
		}
		return stricter(d, decide(Confirm, "runs %s code", name))

	case "rm":
		if hasFlag(args, "r", "R", "f") || hasLong(args, "--recursive", "--force") {
			return decide(Dangerous, "rm -r/-f deletes files irreversibly")
		}
		return stricter(d, decide(Confirm, "deletes files"))
	case "dd", "shred", "wipefs", "fdisk", "sfdisk", "parted", "mkswap", "shutdown", "reboot", "halt", "poweroff", "kill", "pkill", "killall", "truncate":
		return decide(Dangerous, "%s is destructive", name)
	case "chmod", "chown", "chgrp":
		if hasFlag(args, "R") || hasLong(args, "--recursive") {
			return decide(Dangerous, "%s -R changes permissions recursively", name)
		}
		return stricter(stricter(d, systemArgs(name, args)), decide(Confirm, "changes file permissions"))
	case "cp", "mv", "ln", "install", "rsync", "tee", "touch", "mkdir", "rmdir":
		return stricter(stricter(d, systemArgs(name, args)), decide(Confirm, "%s modifies files", name))
	case "crontab":
		if hasFlag(args, "r") {
			return decide(Dangerous, "crontab -r deletes all scheduled jobs")
		}
		return stricter(d, decide(Confirm, "changes scheduled jobs"))

	case "git":
		return stricter(d, classifyGit(args))
	case "docker", "podman":
		return stricter(d, classifyDocker(args))
	case "systemctl":
		return stricter(d, classifySystemctl(args))
	case "find":
		return stricter(d, classifyFind(args, depth))
	case "sed":
		return stricter(d, classifySed(args))
	case "grep", "egrep", "fgrep", "zgrep":
		if hasFlag(args, "r", "R") || hasLong(args, "--recursive", "--dereference-recursive") {
			return stricter(d, sensitive("recursive grep may read secrets such as .env files"))
		}
		return d
	case "rg":
		if hasFlag(args, "u") || hasLong(args, "--hidden", "--no-ignore", "--unrestricted") {
			return stricter(d, sensitive("rg on hidden or ignored files may read secrets"))
		}
		return d
	case "journalctl":
		for _, a := range args {
			if strings.HasPrefix(a.text, "--vacuum") || a.text == "--rotate" || a.text == "--flush" {
				return decide(Dangerous, "journalctl %s deletes or rewrites logs", a.text)
			}
		}
		return d
	case "printenv", "history":
		return sensitive("%s may print secrets", name)
	}

	if mkfs(name) {
		return decide(Dangerous, "%s formats a filesystem", name)
	}
	if writesOutput(name, args) {
		return stricter(stricter(d, systemArgs(name, args)), decide(Confirm, "%s writes its output to a file", name))
	}
	if safeCommands[name] {
		return d
	}
	return stricter(d, decide(Confirm, "%s is not on the read-only list", name))
}

// safeCommands only read or report and cannot run other commands.
var safeCommands = set(
	"ls", "cat", "head", "tail", "wc", "pwd", "which", "whereis", "type", "ps", "df", "du",
	"uname", "echo", "printf", "date", "whoami", "id", "hostname", "uptime", "free", "file",
	"stat", "tree", "sort", "uniq", "cut", "tr", "nl", "basename", "dirname", "realpath",
	"readlink", "lsof", "ss", "netstat", "jq", "diff", "cmp", "md5sum", "sha1sum", "sha256sum",
	"column", "strings", "test", "[", "true", "false", "sleep", "seq", "lsblk", "dmesg",
	"less", "more", "zcat", "xxd", "hexdump", "od", "nproc", "arch", "groups", "last", "w",
	"vmstat", "iostat",
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// classifyArgs applies the checks every command shares: secrets in the
// arguments and values only known at run time.
func classifyArgs(args []word) Decision {
	d := decide(Safe, "read-only")
	for _, a := range args {
		switch {
		case sensitivePath(a.text) || hiddenGlob(a):
			return sensitive("touches a sensitive path (%s)", a.text)
		case rawDevice(a.text):
			return sensitive("reads a raw device (%s)", a.text)
		case a.expand:
			// $AWS_SECRET_ACCESS_KEY and friends: the value is unknown.
			d = stricter(d, sensitive("expands a variable or substitution (%s)", a.text))
		}
	}
	return d
}

func classifyShell(name string, args []word, piped bool, depth int) Decision {
	for i, a := range args {
		if a.text == "-c" || (strings.HasPrefix(a.text, "-") && strings.HasSuffix(a.text, "c") && !strings.HasPrefix(a.text, "--")) {
			if i+1 >= len(args) {
				return decide(Dangerous, "%s -c without a command", name)
			}
			return wrapped(name+" -c", classify(args[i+1].text, depth+1))
		}
	}
	if piped && !hasScriptArg(args) {
		return decide(Dangerous, "pipes data into %s to be executed", name)
	}
	return decide(Confirm, "runs a shell script")
}

// wrapped classifies a command run through sh -c, eval or similar: a safe
// command stays safe, anything else is treated as dangerous because the
// wrapper hides it from review.
func wrapped(wrapper string, inner Decision) Decision {
	if inner.Tier == Safe {
		return inner
	}
	return decide(Dangerous, "%s wraps a command that is not safe (%s)", wrapper, inner.Reason)
}

func classifyXargs(args []word, depth int) Decision {
	// Options that take a value.
	withValue := set("-I", "-i", "-n", "-P", "-L", "-l", "-d", "-E", "-e", "-s", "-a", "--max-args", "--max-procs", "--delimiter", "--arg-file", "--replace")
	i := 0
	for i < len(args) && strings.HasPrefix(args[i].text, "-") {
		if withValue[args[i].text] {
			i++
		}
		i++
	}
	if i >= len(args) {
		return decide(Confirm, "xargs runs echo on its input")
	}
	inner := classifyWords(args[i:], false, depth+1)
	if inner.Tier == Safe {
		// The arguments come from the input, so paths cannot be checked.
		return decide(Confirm, "xargs passes unknown arguments to %s", args[i].text)
	}
	return decide(Dangerous, "xargs runs a command that is not safe on every input (%s)", inner.Reason)
}

func classifyGit(args []word) Decision {
	// Skip global options: -C dir, --no-pager, --git-dir=... A -c override
	// can make git run arbitrary programs (core.pager, core.sshCommand).
	i := 0
	for i < len(args) && strings.HasPrefix(args[i].text, "-") {
		if args[i].text == "-c" || strings.HasPrefix(args[i].text, "--config-env") {
			return decide(Confirm, "git -c overrides configuration, which can run programs")
		}
		if args[i].text == "-C" {
			i++
		}
		i++
	}
	if i >= len(args) {
		return decide(Safe, "read-only")
	}
	sub, rest := args[i].text, args[i+1:]
	if hasLong(rest, "--output", "--exec", "--ext-diff", "--upload-pack", "--receive-pack") {
		return decide(Confirm, "git %s with an option that writes files or runs programs", sub)
	}

	switch sub {
	case "status", "log", "diff", "show", "blame", "rev-parse", "ls-files", "describe", "shortlog", "reflog", "grep", "whatchanged", "ls-tree", "cat-file", "count-objects":
		return decide(Safe, "read-only")
	case "branch", "tag", "remote", "stash":
		if len(rest) == 0 || (len(rest) == 1 && (rest[0].text == "-a" || rest[0].text == "-v" || rest[0].text == "-vv" || rest[0].text == "-r" || rest[0].text == "list")) {
			return decide(Safe, "read-only")
		}
		if hasFlag(rest, "D") || hasLong(rest, "--delete") || (sub == "stash" && (rest[0].text == "drop" || rest[0].text == "clear")) {
			return decide(Dangerous, "git %s deletes data", sub)
		}
		return decide(Confirm, "git %s changes the repository", sub)
	case "push":
		if hasFlag(rest, "f") || hasLong(rest, "--force", "--force-with-lease", "--delete", "--mirror") || anyPrefix(rest, "+") {
			return decide(Dangerous, "git push --force rewrites remote history")
		}
		return decide(Confirm, "git push publishes commits")
	case "reset":
		if hasLong(rest, "--hard", "--merge", "--keep") {
			return decide(Dangerous, "git reset --hard discards changes")
		}
		return decide(Confirm, "git reset moves the branch")
	case "clean":
		if hasFlag(rest, "f") || hasLong(rest, "--force") {
			return decide(Dangerous, "git clean -f deletes untracked files")
		}
		return decide(Confirm, "git clean")
	case "checkout", "restore", "switch":
		if hasLong(rest, "--force", "--") || hasFlag(rest, "f") || (len(rest) > 0 && rest[len(rest)-1].text == ".") {
			return decide(Dangerous, "git %s can discard uncommitted changes", sub)
		}
		return decide(Confirm, "git %s changes the working tree", sub)
	case "filter-branch", "filter-repo", "gc", "prune", "update-ref":
		return decide(Dangerous, "git %s rewrites or deletes history", sub)
	case "config":
		if hasLong(rest, "--get", "--list", "--get-all") || hasFlag(rest, "l") {
			return sensitive("git config can print credentials")
		}
		return decide(Confirm, "git config changes settings")
	}
	return decide(Confirm, "git %s changes the repository", sub)
}

func classifyDocker(args []word) Decision {
	rest := skipOptions(args, 0)
	if len(rest) == 0 {
		return decide(Safe, "read-only")
	}
	sub := rest[0].text
	if sub == "compose" || sub == "container" || sub == "image" {
		if len(rest) < 2 {
			return decide(Safe, "read-only")
		}
		sub = rest[1].text
	}
	switch sub {
	case "ps", "logs", "inspect", "images", "version", "info", "top", "port", "stats", "ls", "events", "history":
		return decide(Safe, "read-only")
	case "rm", "rmi", "prune", "kill", "down":
		return decide(Dangerous, "docker %s deletes or stops containers or images", sub)
	case "system", "volume", "network":
		for _, a := range rest {
			if a.text == "prune" || a.text == "rm" {
				return decide(Dangerous, "docker %s %s deletes data", sub, a.text)
			}
		}
	}
	return decide(Confirm, "docker %s changes containers or runs code", sub)
}

func classifySystemctl(args []word) Decision {
	rest := skipOptions(args, 0)
	if len(rest) == 0 {
		return decide(Safe, "read-only")
	}
	switch rest[0].text {
	case "status", "show", "list-units", "list-unit-files", "list-timers", "is-active", "is-enabled", "is-failed", "cat":
		return decide(Safe, "read-only")
	case "stop", "disable", "mask", "kill", "poweroff", "reboot", "halt", "isolate":
		return decide(Dangerous, "systemctl %s stops services or the machine", rest[0].text)
	}
	return decide(Confirm, "systemctl %s changes services", rest[0].text)
}

func classifyFind(args []word, depth int) Decision {
	for i, a := range args {
		switch a.text {
		case "-delete":
			return decide(Dangerous, "find -delete deletes files")
		case "-exec", "-execdir", "-ok", "-okdir":
			end := i + 1
			for end < len(args) && args[end].text != ";" && args[end].text != "+" {
				end++
			}
			if end == i+1 {
				return decide(Confirm, "find %s without a command", a.text)
			}
			inner := classifyWords(args[i+1:end], false, depth+1)
			if inner.Tier == Dangerous || path.Base(args[i+1].text) == "rm" {
				return decide(Dangerous, "find %s runs a destructive command (%s)", a.text, inner.Reason)
			}
			return decide(Confirm, "find %s runs a command on every match", a.text)
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			return decide(Confirm, "find %s writes to a file", a.text)
		}
	}
	return decide(Safe, "read-only")
}

// classifySed allows only print-style scripts: sed can also write files (w)
// and run commands (e).
func classifySed(args []word) Decision {
	scripts := 0
	for _, a := range args {
		t := a.text
		switch {
		case t == "-i" || strings.HasPrefix(t, "-i") || strings.HasPrefix(t, "--in-place"):
			return decide(Confirm, "sed -i edits files in place")
		case t == "-n" || t == "-E" || t == "-r" || t == "--quiet":
		case strings.HasPrefix(t, "-"):
			return decide(Confirm, "sed with %s", t)
		case scripts == 0:
			scripts++
			if !printScript(t) {
				return decide(Confirm, "sed script is not a plain print")
			}
		}
	}
	return decide(Safe, "read-only")
}

// printScript matches "10p", "1,20p", "$p", "/re/p" and plain substitutions
// like "s/a/b/g" (without the w or e flags).
func printScript(s string) bool {
	if strings.HasSuffix(s, "p") && !strings.ContainsAny(s, ";\n{}") {
		body := strings.TrimSuffix(s, "p")
		if strings.Trim(body, "0123456789,$") == "" {
			return true
		}
		if strings.HasPrefix(body, "/") && strings.HasSuffix(body, "/") && strings.Count(body, "/") == 2 {
			return true
		}
	}
	if strings.HasPrefix(s, "s/") && strings.Count(s, "/") == 3 && !strings.ContainsAny(s, ";\n{}") {
		flags := s[strings.LastIndex(s, "/")+1:]
		return strings.Trim(flags, "gI0123456789") == ""
	}
	return false
}

func systemArgs(name string, args []word) Decision {
	for _, a := range args {
		if systemPath(a.text) || startupFile(a.text) {
			return decide(Dangerous, "%s touches %s", name, a.text)
		}
	}
	return decide(Safe, "read-only")
}

// writesOutput catches the read-only tools that can also write a file.
func writesOutput(name string, args []word) bool {
	switch name {
	case "sort":
		return hasFlag(args, "o") || hasLong(args, "--output")
	case "tree":
		return hasFlag(args, "o")
	case "uniq":
		positional := 0
		for _, a := range args {
			if !strings.HasPrefix(a.text, "-") {
				positional++
			}
		}
		return positional > 1 // uniq IN OUT
	}
	return false
}

func mkfs(name string) bool {
	return name == "mkfs" || strings.HasPrefix(name, "mkfs.") || name == "newfs"
}

// hasFlag reports whether any short option cluster contains one of letters
// (so -rf matches "r" and "f").
func hasFlag(args []word, letters ...string) bool {
	for _, a := range args {
		t := a.text
		if t == "--" {
			return false
		}
		if len(t) < 2 || t[0] != '-' || t[1] == '-' {
			continue
		}
		for _, l := range letters {
			if strings.Contains(t[1:], l) {
				return true
			}
		}
	}
	return false
}

func hasLong(args []word, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a.text == f || strings.HasPrefix(a.text, f+"=") {
				return true
			}
		}
	}
	return false
}

func anyPrefix(args []word, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a.text, prefix) {
			return true
		}
	}
	return false
}

// skipOptions drops leading options. When positional > 0, that many leading
// non-option words are skipped too (e.g. the duration of timeout).
func skipOptions(args []word, positional int) []word {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i].text, "-") {
		i++
	}
	for positional > 0 && i < len(args) {
		i++
		positional--
	}
	return args[i:]
}

func hasScriptArg(args []word) bool {
	for _, a := range args {
		if !strings.HasPrefix(a.text, "-") {
			return true
		}
	}
	return false
}

func isAssignment(s string) bool {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return false
	}
	for i, c := range s[:eq] {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func joinWords(ws []word) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.text
	}
	return strings.Join(parts, " ")
}
