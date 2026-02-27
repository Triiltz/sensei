package executor

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ParseCommand checks if the AI response contains a COMMAND= line and returns
// the response text (without the command line) and the extracted command.
func ParseCommand(response string) (text string, command string) {
	var textLines []string

	for _, line := range strings.Split(response, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "COMMAND=") {
			command = strings.TrimPrefix(trimmed, "COMMAND=")
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
