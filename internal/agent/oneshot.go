package agent

import (
	"context"
	"fmt"

	"github.com/triiltz/sensei/internal/executor"
	"github.com/triiltz/sensei/internal/llm"
)

// maxPipeContentLen is the maximum number of bytes allowed from piped input.
// Content beyond this limit is truncated to avoid exceeding model token limits.
const maxPipeContentLen = 100_000

// Answer is the model's reply in one-shot and pipe modes.
type Answer struct {
	Text     string
	Commands []executor.Command
	Usage    llm.Usage
}

// Ask sends a single request: the user prompt, with optional piped content.
//
// In one-shot mode the model may call run_command; the calls are returned
// for the caller to confirm and run. Pipe mode is analysis only, so no tools
// are offered: piped content is untrusted and must not be able to steer the
// model into running anything.
func Ask(ctx context.Context, p llm.Provider, userPrompt, pipeContent string, warnf func(format string, args ...any)) (Answer, error) {
	req := llm.Request{System: OneShotSystemPrompt, Tools: []llm.Tool{RunCommandTool}}

	if pipeContent != "" {
		if len(pipeContent) > maxPipeContentLen {
			warnf("Warning: piped input was truncated from %d to %d bytes.", len(pipeContent), maxPipeContentLen)
			pipeContent = pipeContent[:maxPipeContentLen] + "\n\n[... content truncated due to size limit ...]"
		}
		req.System = PipeSystemPrompt
		req.Tools = nil
		userPrompt = fmt.Sprintf("Content received via pipe:\n```\n%s\n```\n\nUser question: %s", pipeContent, userPrompt)
	}

	req.Messages = []llm.Message{{Role: llm.RoleUser, Content: userPrompt}}

	resp, err := p.Chat(ctx, req)
	if err != nil {
		return Answer{}, err
	}

	answer := Answer{Text: resp.Message.Content, Usage: resp.Usage}
	for _, tc := range resp.Message.ToolCalls {
		args, err := ParseRunCommand(tc)
		if err != nil {
			warnf("Warning: ignoring a malformed tool call: %v", err)
			continue
		}
		answer.Commands = append(answer.Commands, executor.Command{Command: args.Command, Reason: args.Reason})
	}
	return answer, nil
}
