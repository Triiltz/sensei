package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/briandowns/spinner"
	"github.com/spf13/cobra"
	"github.com/triiltz/sensei/internal/ai"
	"github.com/triiltz/sensei/internal/config"
	"github.com/triiltz/sensei/internal/executor"
)

var (
	forceFlag  bool
	openAIFlag bool
	geminiFlag bool
)

// version is set at build time via -ldflags "-X github.com/triiltz/sensei/cmd.version=vX.Y.Z".
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "sensei [question]",
	Short: "SENSEI — AI assistant in your terminal",
	Long: `SENSEI brings the power of LLMs straight to your terminal.

Examples:
  sensei "how to untar a tar.gz?"
  cat script.sh | sensei "what does this do?"
  sensei -f "create a new git branch called feature/login"
  sensei -o    (open ChatGPT in browser)
  sensei -g    (open Gemini in browser)`,
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          run,
}

func init() {
	rootCmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Execute the suggested command without confirmation")
	rootCmd.Flags().BoolVarP(&openAIFlag, "openai", "o", false, "Open ChatGPT in the default browser")
	rootCmd.Flags().BoolVarP(&geminiFlag, "gemini", "g", false, "Open Google Gemini in the default browser")
	rootCmd.MarkFlagsMutuallyExclusive("openai", "gemini")
}

// Execute is called by main.go to start the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// Browser flags: open provider UI and exit.
	if openAIFlag || geminiFlag {
		if len(args) > 0 {
			fmt.Fprintln(os.Stderr, "Note: the browser flags ignore any question passed on the command line.")
		}
		if openAIFlag {
			return openBrowser("https://chat.openai.com")
		}
		return openBrowser("https://gemini.google.com")
	}

	// Read piped content if available.
	pipeContent := readPipe()

	// Piped content is untrusted: it could steer the model into emitting a
	// COMMAND= line, which -f would then run without any confirmation.
	if pipeContent != "" && forceFlag {
		return fmt.Errorf("-f cannot be used with piped input.\n\n" +
			"Piped content is untrusted and could make the model suggest an unintended command.\n" +
			"Run without -f and confirm the command yourself.")
	}

	// Build the user prompt from arguments.
	userPrompt := strings.Join(args, " ")

	if userPrompt == "" && pipeContent == "" {
		return fmt.Errorf("no question provided.\n\nUsage: sensei \"your question here\"\n       cat file.txt | sensei \"explain this\"")
	}

	// If only pipe content and no question, default to "explain this".
	if userPrompt == "" && pipeContent != "" {
		userPrompt = "Explain this."
	}

	// Load configuration (API key, provider, model).
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Call the AI.
	client := ai.NewClient(cfg)

	stopSpinner := startSpinner()
	response, err := client.Ask(userPrompt, pipeContent)
	stopSpinner()

	if err != nil {
		return fmt.Errorf("AI request failed: %w", err)
	}

	// Parse the response and optionally execute the command.
	return executor.Run(response, forceFlag)
}

// startSpinner shows a "Thinking..." spinner and returns a function that stops it.
//
// The spinner writes to stderr so that redirecting or piping stdout yields the
// response alone, and it is skipped entirely when stderr is not a terminal.
// A signal handler restores the cursor on Ctrl+C, which the spinner hides.
func startSpinner() (stop func()) {
	if !isTerminal(os.Stderr) {
		return func() {}
	}

	// WithWriterFile, not WithWriter: the latter pins the library's terminal
	// check to stdout, which would silently disable the spinner whenever
	// stdout is redirected.
	s := spinner.New(spinner.CharSets[11], 80*time.Millisecond, spinner.WithWriterFile(os.Stderr))
	s.Suffix = " Thinking..."
	s.Start()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			s.Stop()
			os.Exit(130)
		case <-done:
		}
	}()

	var once bool
	return func() {
		if once {
			return
		}
		once = true
		signal.Stop(sigCh)
		close(done)
		s.Stop()
	}
}

// isTerminal reports whether the given file is attached to a terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// readPipe checks if stdin has piped data and reads it.
func readPipe() string {
	// Data is being piped when stdin is not a terminal.
	if isTerminal(os.Stdin) {
		return ""
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

// openBrowser opens the given URL in the default system browser.
func openBrowser(url string) error {
	var cmdName string
	var args []string

	switch runtime.GOOS {
	case "linux":
		cmdName = "xdg-open"
		args = []string{url}
	case "darwin":
		cmdName = "open"
		args = []string{url}
	default:
		return fmt.Errorf("unsupported platform")
	}

	return exec.Command(cmdName, args...).Start()
}
