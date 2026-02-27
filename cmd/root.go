package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/triiltz/sensei/internal/ai"
	"github.com/triiltz/sensei/internal/config"
	"github.com/triiltz/sensei/internal/executor"
)

var (
	forceFlag bool
	openFlag  bool
)

var rootCmd = &cobra.Command{
	Use:   "sensei [question]",
	Short: "SENSEI — AI assistant in your terminal",
	Long: `SENSEI brings the power of LLMs straight to your terminal.

Examples:
  sensei "how to untar a tar.gz?"
  cat script.sh | sensei "what does this do?"
  sensei -f "create a new git branch called feature/login"
  sensei -o`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          run,
}

func init() {
	rootCmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Execute the suggested command without confirmation")
	rootCmd.Flags().BoolVarP(&openFlag, "open", "o", false, "Open AI chat in the default browser")
}

// Execute is called by main.go to start the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// --open flag: open the browser and exit.
	if openFlag {
		return openBrowser("https://chat.openai.com")
	}

	// Read piped content if available.
	pipeContent := readPipe()

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
	response, err := client.Ask(userPrompt, pipeContent)
	if err != nil {
		return fmt.Errorf("AI request failed: %w", err)
	}

	// Parse the response and optionally execute the command.
	return executor.Run(response, forceFlag)
}

// readPipe checks if stdin has piped data and reads it.
func readPipe() string {
	info, err := os.Stdin.Stat()
	if err != nil {
		return ""
	}

	// Check if data is being piped (not a terminal).
	if (info.Mode() & os.ModeCharDevice) != 0 {
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
	case "windows":
		cmdName = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported platform")
	}

	return exec.Command(cmdName, args...).Start()
}
