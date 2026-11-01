package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	boldAsteriskRE   = regexp.MustCompile(`\*\*(.*?)\*\*`)
	boldUnderscoreRE = regexp.MustCompile(`__(.*?)__`)
	codeSpanRE       = regexp.MustCompile("`([^`]+)`")
	headingRE        = regexp.MustCompile(`^#{1,6}\s+`)

	// placeholderRE matches angle-bracket placeholders such as <branch-name>.
	// It deliberately requires an identifier-like body so that shell
	// redirections (`sort < in.txt > out.txt`) are not mistaken for one.
	placeholderRE = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9_-]*>`)
)

// Seams replaced by tests.
var (
	stdout io.Writer = os.Stdout

	// openTTY opens the controlling terminal for the confirmation prompt, so
	// it works even when stdin is redirected or was consumed by a pipe.
	openTTY = func() (io.ReadCloser, error) { return os.Open("/dev/tty") }

	runShell = execute
)

// Command is a shell command suggested by the model.
type Command struct {
	Command string
	Reason  string
}

// Run prints the model's answer and, when it suggested exactly one runnable
// command, asks for confirmation (unless force is true) and runs it.
//
// Several commands, or a command with placeholders, are shown but never run:
// there is no single safe thing to confirm.
func Run(ctx context.Context, text string, cmds []Command, force bool) error {
	text = normalizePlainText(text)
	if text == "" && len(cmds) == 1 {
		text = cmds[0].Reason
	}
	if text != "" {
		fmt.Fprintln(stdout, text)
	}

	if len(cmds) == 0 {
		return nil
	}
	if len(cmds) > 1 || placeholderRE.MatchString(cmds[0].Command) {
		for _, c := range cmds {
			fmt.Fprintf(stdout, "  $ %s\n", c.Command)
		}
		return nil
	}

	command := cmds[0].Command
	fmt.Fprintf(stdout, "\nSuggested command: %s\n", command)

	if !force {
		ok, err := confirm(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	fmt.Fprintln(stdout)
	return runShell(ctx, command)
}

// confirm asks "Run it? [y/N]" on the terminal and reports the answer.
func confirm(ctx context.Context) (bool, error) {
	tty, err := openTTY()
	if err != nil {
		fmt.Fprintln(stdout, "Not running it: there is no terminal to confirm the command on.")
		return false, nil
	}
	defer tty.Close()

	fmt.Fprint(stdout, "   Run it? [y/N]: ")

	// Read in the background so Ctrl+C (which cancels ctx) is not stuck
	// behind a blocking read.
	answers := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(tty).ReadString('\n')
		answers <- line
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(stdout)
		return false, ctx.Err()
	case answer := <-answers:
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Aborted.")
			return false, nil
		}
		return true, nil
	}
}

// normalizePlainText removes common Markdown markers so output stays terminal-friendly.
//
// It is deliberately conservative: only real Markdown constructs are stripped,
// so shebangs, shell comments and backticks used for command substitution
// survive intact — those show up constantly when analysing piped scripts.
func normalizePlainText(text string) string {
	if text == "" {
		return text
	}

	lines := strings.Split(text, "\n")
	inCodeFence := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeFence = !inCodeFence
			lines[i] = ""
			continue
		}

		if inCodeFence {
			continue
		}

		leading := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		content := strings.TrimLeft(line, " \t")

		// Only "# " through "###### " are headings; a bare "#" starts a shell
		// comment and "#!" a shebang, both of which must be preserved.
		content = headingRE.ReplaceAllString(content, "")

		if strings.HasPrefix(content, "* ") || strings.HasPrefix(content, "+ ") {
			content = "- " + strings.TrimSpace(content[2:])
		}

		content = boldAsteriskRE.ReplaceAllString(content, "$1")
		content = boldUnderscoreRE.ReplaceAllString(content, "$1")
		// Unwrap paired code spans only, leaving unmatched backticks alone.
		content = codeSpanRE.ReplaceAllString(content, "$1")

		lines[i] = leading + content
	}

	clean := strings.Join(lines, "\n")
	clean = strings.TrimSpace(clean)

	return clean
}

// execute runs the command through the user's shell and streams output.
func execute(ctx context.Context, command string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}
