package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/triiltz/sensei/internal/executor"
	"github.com/triiltz/sensei/internal/llm"
	"github.com/triiltz/sensei/internal/policy"
)

// declinedOutput is the tool result sent when the user says no.
const declinedOutput = "User declined to run this command."

// Config configures an agent run.
type Config struct {
	Provider   llm.Provider
	Dir        string // working directory shown to the model; "" means os.Getwd
	MaxSteps   int
	CmdTimeout time.Duration
	Force      bool // skip confirmation for the confirm tier (never for dangerous or sensitive)
	Verbose    bool // also print command output

	// Out receives the final answer; Log receives progress and prompts, so
	// redirecting stdout captures the answer alone.
	Out, Log io.Writer
	Color    bool // dim the progress lines

	// Hooks; nil means the real implementation. Approve is only called for
	// commands that need it (see policy.Decision.NeedsConfirmation).
	Approve  func(ctx context.Context, cmd RunCommandArgs, d policy.Decision) (bool, error)
	Exec     func(ctx context.Context, command string, timeout time.Duration) (executor.Result, error)
	Thinking func() (stop func()) // shown while waiting for the model
}

// Call records one run_command request and what happened to it.
type Call struct {
	Command  string
	Reason   string
	Tier     policy.Tier
	Approved bool
	ExitCode int
	TimedOut bool
	Error    string // malformed call or the command could not start
}

// Result summarises an agent run.
type Result struct {
	Answer      string
	Steps       int // requests sent to the model
	Calls       []Call
	Usage       llm.Usage
	Timing      llm.Timing
	HitMaxSteps bool
}

// Run answers question by letting the model run commands in a loop: plan,
// run, observe, repeat, until it answers or the step limit is reached.
//
// The returned Result is filled in even when an error is returned, so a
// caller can see how far the run got.
func Run(ctx context.Context, cfg Config, question string) (Result, error) {
	r := &runner{cfg: cfg}
	r.defaults()
	return r.run(ctx, question)
}

type runner struct {
	cfg Config
	res Result
}

func (r *runner) defaults() {
	c := &r.cfg
	if c.Out == nil {
		c.Out = os.Stdout
	}
	if c.Log == nil {
		c.Log = os.Stderr
	}
	if c.Approve == nil {
		c.Approve = func(ctx context.Context, _ RunCommandArgs, d policy.Decision) (bool, error) {
			if d.Tier == policy.Dangerous {
				fmt.Fprintln(c.Log, executor.Warning(d, c.Color))
			}
			return executor.Confirm(ctx, c.Log, "  Run it? [y/N]: ")
		}
	}
	if c.Exec == nil {
		c.Exec = executor.Capture
	}
	if c.Thinking == nil {
		c.Thinking = func() func() { return func() {} }
	}
}

func (r *runner) run(ctx context.Context, question string) (Result, error) {
	cwd := r.cfg.Dir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	req := llm.Request{
		System:   AgentSystemPrompt(runtime.GOOS, cwd),
		Messages: []llm.Message{{Role: llm.RoleUser, Content: question}},
		Tools:    []llm.Tool{RunCommandTool},
	}

	for step := 0; step < r.cfg.MaxSteps; step++ {
		resp, err := r.chat(ctx, req)
		if err != nil {
			return r.res, err
		}
		req.Messages = append(req.Messages, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			r.finish(resp.Message.Content)
			return r.res, nil
		}

		if note := strings.TrimSpace(resp.Message.Content); note != "" {
			r.logf("%s\n", r.dim(note))
		}
		for _, tc := range resp.Message.ToolCalls {
			output, err := r.handle(ctx, tc)
			if err != nil {
				return r.res, err
			}
			req.Messages = append(req.Messages, llm.Message{
				Role: llm.RoleTool, ToolCallID: tc.ID, ToolName: tc.Name, Content: output,
			})
		}
	}

	// Out of steps: ask for a final answer from what was gathered so far.
	r.res.HitMaxSteps = true
	r.logf("%s\n", r.dim(fmt.Sprintf("Step limit reached (%d). Asking for a summary of what was found.", r.cfg.MaxSteps)))
	req.System += stepLimitNote
	req.NoToolCalls = true // the note alone is not enough: models still call tools
	resp, err := r.chat(ctx, req)
	if err != nil {
		return r.res, err
	}
	answer := resp.Message.Content
	if strings.TrimSpace(answer) == "" {
		answer = fmt.Sprintf("Stopped after %d steps without reaching an answer. "+
			"Raise SENSEI_MAX_STEPS or ask a narrower question.", r.cfg.MaxSteps)
	}
	r.finish(answer)
	return r.res, nil
}

// chat sends one request and accumulates usage and timing.
func (r *runner) chat(ctx context.Context, req llm.Request) (llm.Response, error) {
	stop := r.cfg.Thinking()
	resp, err := r.cfg.Provider.Chat(ctx, req)
	stop()

	r.res.Steps++
	r.res.Usage.InputTokens += resp.Usage.InputTokens
	r.res.Usage.OutputTokens += resp.Usage.OutputTokens
	r.res.Timing.Latency += resp.Timing.Latency
	r.res.Timing.Backoff += resp.Timing.Backoff
	return resp, err
}

// handle runs one tool call and returns the tool result for the model. An
// error is returned only when the run must stop (the user pressed Ctrl+C).
func (r *runner) handle(ctx context.Context, tc llm.ToolCall) (string, error) {
	args, err := ParseRunCommand(tc)
	if err != nil {
		r.res.Calls = append(r.res.Calls, Call{Error: err.Error()})
		r.logf("%s\n", r.dim("Skipping a malformed tool call: "+err.Error()))
		return "Error: " + err.Error(), nil
	}

	decision := policy.Classify(args.Command)
	call := Call{Command: args.Command, Reason: args.Reason, Tier: decision.Tier}
	if args.Reason != "" {
		r.logf("%s\n", r.dim("→ "+args.Reason))
	}
	r.logf("%s\n", r.dim("  $ "+args.Command))

	approved := !decision.NeedsConfirmation(r.cfg.Force)
	if !approved {
		approved, err = r.cfg.Approve(ctx, args, decision)
		if err != nil {
			return "", err
		}
	}
	call.Approved = approved
	if !approved {
		r.res.Calls = append(r.res.Calls, call)
		return declinedOutput, nil
	}

	res, err := r.cfg.Exec(ctx, args.Command, r.cfg.CmdTimeout)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		call.Error = err.Error()
		r.res.Calls = append(r.res.Calls, call)
		r.logf("%s\n", r.dim("  "+err.Error()))
		return "Error: " + err.Error(), nil
	}

	call.ExitCode, call.TimedOut = res.ExitCode, res.TimedOut
	r.res.Calls = append(r.res.Calls, call)

	output := res.Format(r.cfg.CmdTimeout)
	if r.cfg.Verbose {
		r.logf("%s\n", r.dim(indent(output, "    ")))
	} else if res.TimedOut || res.ExitCode != 0 {
		r.logf("%s\n", r.dim("  "+strings.SplitN(output, "\n", 2)[0]))
	}
	return output, nil
}

func (r *runner) finish(answer string) {
	r.res.Answer = strings.TrimSpace(answer)
	text := executor.NormalizePlainText(r.res.Answer)
	if text == "" {
		return
	}
	if len(r.res.Calls) > 0 {
		fmt.Fprintln(r.cfg.Out) // separate the answer from the step log
	}
	fmt.Fprintln(r.cfg.Out, text)
}

func (r *runner) logf(format string, args ...any) {
	fmt.Fprintf(r.cfg.Log, format, args...)
}

func (r *runner) dim(s string) string {
	if !r.cfg.Color {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
