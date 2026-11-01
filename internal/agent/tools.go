// Package agent builds requests for the model and interprets its tool calls.
package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/triiltz/sensei/internal/llm"
)

// RunCommandName is the name of the only tool exposed to the model.
const RunCommandName = "run_command"

// RunCommandTool lets the model ask to run a shell command.
var RunCommandTool = llm.Tool{
	Name: RunCommandName,
	Description: "Run a shell command on the user's machine and return its stdout, stderr and exit code. " +
		"Use read-only commands to investigate before proposing changes.",
	Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The shell command to run."},
    "reason": {"type": "string", "description": "One short sentence explaining why this command is needed."}
  },
  "required": ["command", "reason"]
}`),
}

// RunCommandArgs are the arguments of a run_command call.
type RunCommandArgs struct {
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// ParseRunCommand decodes and validates a run_command call.
func ParseRunCommand(tc llm.ToolCall) (RunCommandArgs, error) {
	if tc.Name != RunCommandName {
		return RunCommandArgs{}, fmt.Errorf("unknown tool %q", tc.Name)
	}
	var args RunCommandArgs
	if err := json.Unmarshal(tc.Arguments, &args); err != nil {
		return RunCommandArgs{}, fmt.Errorf("invalid run_command arguments: %w", err)
	}
	args.Command = strings.TrimSpace(args.Command)
	args.Reason = strings.TrimSpace(args.Reason)
	if args.Command == "" {
		return RunCommandArgs{}, fmt.Errorf("run_command called without a command")
	}
	return args, nil
}
