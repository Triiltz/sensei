package executor

import (
	"bufio"
	"fmt"
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

// ParseCommand checks if the AI response contains a COMMAND= line and returns
// the response text (without the command line) and the extracted command.
func ParseCommand(response string) (text string, command string) {
	var textLines []string
	var commands []string
	lines := strings.Split(response, "\n")

	// First pass: count how many commands there are
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "COMMAND=") {
			commands = append(commands, strings.TrimPrefix(trimmed, "COMMAND="))
		}
	}

	// Second pass: build the text and extract the command (if only one)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "COMMAND=") {
			if len(commands) == 1 && !placeholderRE.MatchString(commands[0]) {
				command = commands[0] // Extract to execute
			} else {
				// If multiple, show them as text and avoid execution
				cmd := strings.TrimPrefix(trimmed, "COMMAND=")
				textLines = append(textLines, "  $ "+cmd)
			}
		} else {
			textLines = append(textLines, line)
		}
	}

	text = strings.TrimSpace(strings.Join(textLines, "\n"))
	return text, command
}

// Run prints the response text, and if a command was detected, prompts for
// execution (unless force is true).
func Run(response string, force bool) error {
	text, command := ParseCommand(response)
	text = normalizePlainText(text)

	// Always print the explanation text.
	if text != "" {
		fmt.Println(text)
	}

	// No command detected — nothing else to do.
	if command == "" {
		return nil
	}

	fmt.Printf("\nSuggested command: %s\n", command)

	if !force {
		// stdin may have been consumed by pipe mode, or simply not be a
		// terminal — either way there is nobody to answer the prompt.
		if !isTerminal(os.Stdin) {
			fmt.Println("Not running it: stdin is not a terminal, so the command cannot be confirmed.")
			return nil
		}

		fmt.Print("   Run it? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	fmt.Println()
	return execute(command)
}

// isTerminal reports whether the given file is attached to a terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
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
func execute(command string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, "-c", command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}
