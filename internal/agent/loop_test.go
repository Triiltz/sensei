package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/triiltz/sensei/internal/executor"
	"github.com/triiltz/sensei/internal/llm"
	"github.com/triiltz/sensei/internal/policy"
)

// scripted replays a fixed sequence of responses (or errors) and records
// every request, copying the message slice so later appends don't alter it.
type scripted struct {
	steps []step
	reqs  []llm.Request
}

type step struct {
	resp llm.Response
	err  error
}

func (s *scripted) Name() string  { return "scripted" }
func (s *scripted) Model() string { return "scripted-model" }
func (s *scripted) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	req.Messages = append([]llm.Message(nil), req.Messages...)
	s.reqs = append(s.reqs, req)
	i := min(len(s.reqs)-1, len(s.steps)-1)
	return s.steps[i].resp, s.steps[i].err
}

func toolTurn(text string, calls ...llm.ToolCall) step {
	return step{resp: llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: calls},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 2},
	}}
}

func answerTurn(text string) step {
	return step{resp: llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: text},
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 20, OutputTokens: 5},
	}}
}

func runCmd(id, command string) llm.ToolCall {
	return call(id, `{"command":"`+command+`","reason":"check `+command+`"}`)
}

// harness wires a scripted provider to fake approval and execution.
type harness struct {
	provider *scripted
	cfg      Config
	out, log bytes.Buffer
	executed []string
	asked    []string
}

func newHarness(steps ...step) *harness {
	h := &harness{provider: &scripted{steps: steps}}
	h.cfg = Config{
		Provider:   h.provider,
		MaxSteps:   8,
		CmdTimeout: 30 * time.Second,
		Out:        &h.out,
		Log:        &h.log,
		Approve: func(_ context.Context, c RunCommandArgs, _ policy.Decision) (bool, error) {
			h.asked = append(h.asked, c.Command)
			return true, nil
		},
		Exec: func(_ context.Context, command string, _ time.Duration) (executor.Result, error) {
			h.executed = append(h.executed, command)
			return executor.Result{Stdout: "output of " + command + "\n"}, nil
		},
	}
	return h
}

func (h *harness) run(t *testing.T) (Result, error) {
	t.Helper()
	return Run(context.Background(), h.cfg, "why is my app broken?")
}

func TestLoopMultiStepSuccess(t *testing.T) {
	h := newHarness(
		toolTurn("Let me look.", runCmd("c1", "ls")),
		toolTurn("", runCmd("c2", "cat app.log")),
		answerTurn("The log shows a **missing** config file."),
	)

	res, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := strings.Join(h.executed, "|"); got != "ls|cat app.log" {
		t.Errorf("executed %q", got)
	}
	if res.Steps != 3 || len(res.Calls) != 2 || res.HitMaxSteps {
		t.Errorf("Steps=%d Calls=%d HitMaxSteps=%v", res.Steps, len(res.Calls), res.HitMaxSteps)
	}
	if res.Usage != (llm.Usage{InputTokens: 40, OutputTokens: 9}) {
		t.Errorf("Usage = %+v, want the sum over all requests", res.Usage)
	}
	if res.Answer != "The log shows a **missing** config file." {
		t.Errorf("Answer = %q", res.Answer)
	}
	if got := h.out.String(); got != "\nThe log shows a missing config file.\n" {
		t.Errorf("stdout = %q, want only the plain-text answer", got)
	}
	for _, want := range []string{"Let me look.", "→ check ls", "$ cat app.log"} {
		if !strings.Contains(h.log.String(), want) {
			t.Errorf("progress log missing %q:\n%s", want, h.log.String())
		}
	}

	// The last request carries the whole conversation, with each tool
	// result tied to its call.
	last := h.provider.reqs[2]
	if len(last.Messages) != 5 {
		t.Fatalf("got %d messages, want 5 (user, assistant, tool, assistant, tool)", len(last.Messages))
	}
	result := last.Messages[4]
	if result.Role != llm.RoleTool || result.ToolCallID != "c2" || result.ToolName != RunCommandName {
		t.Errorf("tool result = %+v", result)
	}
	if !strings.Contains(result.Content, "exit_code: 0") || !strings.Contains(result.Content, "output of cat app.log") {
		t.Errorf("tool result content = %q", result.Content)
	}
	if len(last.Tools) != 1 || last.Tools[0].Name != RunCommandName {
		t.Errorf("Tools = %+v", last.Tools)
	}
}

func TestLoopMaxStepsReached(t *testing.T) {
	h := newHarness(
		toolTurn("", runCmd("c1", "ls")),
		toolTurn("", runCmd("c2", "ls -a")),
		answerTurn("Found two files; the cause is still unknown."),
	)
	h.cfg.MaxSteps = 2

	res, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.HitMaxSteps || res.Steps != 3 || len(h.executed) != 2 {
		t.Errorf("HitMaxSteps=%v Steps=%d executed=%v", res.HitMaxSteps, res.Steps, h.executed)
	}
	if !strings.Contains(h.provider.reqs[2].System, "step limit has been reached") {
		t.Error("the summary request does not tell the model to stop")
	}
	if strings.Contains(h.provider.reqs[1].System, "step limit") {
		t.Error("the limit note leaked into a normal step")
	}
	if !h.provider.reqs[2].NoToolCalls || h.provider.reqs[1].NoToolCalls {
		t.Error("only the summary request must forbid tool calls")
	}
	if len(h.provider.reqs[2].Tools) == 0 {
		t.Error("the summary request must keep the tools declared")
	}
	if res.Answer != "Found two files; the cause is still unknown." {
		t.Errorf("Answer = %q", res.Answer)
	}
}

func TestLoopMaxStepsWithoutSummary(t *testing.T) {
	// The model keeps asking for tools even after being told to stop.
	h := newHarness(toolTurn("", runCmd("c1", "ls")))
	h.cfg.MaxSteps = 1

	res, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.executed) != 1 {
		t.Errorf("tools from the summary request were executed: %v", h.executed)
	}
	if !strings.Contains(res.Answer, "Stopped after 1 steps") {
		t.Errorf("Answer = %q", res.Answer)
	}
}

func TestLoopUserDeclines(t *testing.T) {
	h := newHarness(
		toolTurn("", runCmd("c1", "rm -rf build")),
		answerTurn("Without deleting build/ I can't confirm the fix."),
	)
	h.cfg.Approve = func(context.Context, RunCommandArgs, policy.Decision) (bool, error) { return false, nil }

	res, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.executed) != 0 {
		t.Errorf("a declined command ran: %v", h.executed)
	}
	if res.Calls[0].Approved {
		t.Error("call recorded as approved")
	}
	if got := h.provider.reqs[1].Messages[2].Content; got != declinedOutput {
		t.Errorf("tool result = %q, want %q", got, declinedOutput)
	}
}

func TestLoopPolicyTiers(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		force     bool
		wantAsked bool
		wantTier  policy.Tier
	}{
		{name: "safe runs without asking", command: "ls -la", wantTier: policy.Safe},
		{name: "confirm asks", command: "npm test", wantAsked: true, wantTier: policy.Confirm},
		{name: "force skips confirm", command: "npm test", force: true, wantTier: policy.Confirm},
		{name: "sensitive read asks", command: "cat .env", wantAsked: true, wantTier: policy.Confirm},
		{name: "force never bypasses a sensitive read", command: "cat .env", force: true, wantAsked: true, wantTier: policy.Confirm},
		{name: "dangerous asks", command: "rm -rf build", wantAsked: true, wantTier: policy.Dangerous},
		{name: "force never bypasses dangerous", command: "rm -rf build", force: true, wantAsked: true, wantTier: policy.Dangerous},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(toolTurn("", runCmd("c1", tt.command)), answerTurn("done"))
			h.cfg.Force = tt.force
			var gotDecision policy.Decision
			h.cfg.Approve = func(_ context.Context, c RunCommandArgs, d policy.Decision) (bool, error) {
				h.asked = append(h.asked, c.Command)
				gotDecision = d
				return true, nil
			}

			res, err := h.run(t)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if asked := len(h.asked) > 0; asked != tt.wantAsked {
				t.Errorf("asked = %v, want %v", asked, tt.wantAsked)
			}
			if tt.wantAsked && gotDecision.Tier != tt.wantTier {
				t.Errorf("Approve got tier %s, want %s", gotDecision.Tier, tt.wantTier)
			}
			if res.Calls[0].Tier != tt.wantTier {
				t.Errorf("recorded tier = %s, want %s", res.Calls[0].Tier, tt.wantTier)
			}
		})
	}
}

func TestLoopToolErrors(t *testing.T) {
	h := newHarness(
		toolTurn("",
			call("c1", `not json`),
			runCmd("c2", "false"),
			runCmd("c3", "missing-binary"),
		),
		answerTurn("Two commands failed."),
	)
	h.cfg.Exec = func(_ context.Context, command string, _ time.Duration) (executor.Result, error) {
		h.executed = append(h.executed, command)
		if command == "missing-binary" {
			return executor.Result{}, errors.New("could not run command: not found")
		}
		return executor.Result{Stderr: "boom\n", ExitCode: 1}, nil
	}

	res, err := h.run(t)
	if err != nil {
		t.Fatalf("a tool error must not stop the run: %v", err)
	}

	results := h.provider.reqs[1].Messages[2:]
	if len(results) != 3 {
		t.Fatalf("got %d tool results, want 3", len(results))
	}
	if !strings.HasPrefix(results[0].Content, "Error: invalid run_command arguments") {
		t.Errorf("malformed call result = %q", results[0].Content)
	}
	if !strings.Contains(results[1].Content, "exit_code: 1") || !strings.Contains(results[1].Content, "boom") {
		t.Errorf("failed command result = %q", results[1].Content)
	}
	if !strings.Contains(results[2].Content, "could not run command") {
		t.Errorf("exec error result = %q", results[2].Content)
	}
	if res.Calls[1].ExitCode != 1 || res.Calls[2].Error == "" || res.Calls[0].Error == "" {
		t.Errorf("Calls = %+v", res.Calls)
	}
}

func TestLoopProviderErrorMidRun(t *testing.T) {
	h := newHarness(
		toolTurn("", runCmd("c1", "ls")),
		step{err: errors.New("gemini: provider is unavailable (HTTP 503)")},
	)

	res, err := h.run(t)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want the provider error", err)
	}
	if res.Steps != 2 || len(res.Calls) != 1 {
		t.Errorf("partial result lost: Steps=%d Calls=%d", res.Steps, len(res.Calls))
	}
	if h.out.Len() != 0 {
		t.Errorf("an answer was printed after a failure: %q", h.out.String())
	}
}

func TestLoopMultipleToolCallsInOneResponse(t *testing.T) {
	h := newHarness(
		toolTurn("", runCmd("c1", "docker ps -a"), runCmd("c2", "df -h")),
		answerTurn("The disk is full."),
	)

	if _, err := h.run(t); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(h.executed, "|"); got != "docker ps -a|df -h" {
		t.Errorf("executed %q, want both in order", got)
	}
	msgs := h.provider.reqs[1].Messages
	if len(msgs) != 4 || msgs[2].ToolCallID != "c1" || msgs[3].ToolCallID != "c2" {
		t.Errorf("both results must follow the assistant turn, in order: %+v", msgs)
	}
}

func TestLoopCancelledDuringConfirmation(t *testing.T) {
	h := newHarness(toolTurn("", runCmd("c1", "npm test")), answerTurn("never"))
	h.cfg.Approve = func(context.Context, RunCommandArgs, policy.Decision) (bool, error) { return false, context.Canceled }

	if _, err := h.run(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(h.provider.reqs) != 1 || len(h.executed) != 0 {
		t.Errorf("the run continued after Ctrl+C: reqs=%d executed=%v", len(h.provider.reqs), h.executed)
	}
}

func TestLoopVerbosePrintsOutput(t *testing.T) {
	h := newHarness(toolTurn("", runCmd("c1", "ls")), answerTurn("ok"))
	h.cfg.Verbose = true

	if _, err := h.run(t); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(h.log.String(), "    output of ls") {
		t.Errorf("verbose log lacks the command output:\n%s", h.log.String())
	}
}

func TestLoopAnswersWithoutTools(t *testing.T) {
	h := newHarness(answerTurn("A pipe connects two processes."))

	res, err := h.run(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Steps != 1 || h.out.String() != "A pipe connects two processes.\n" {
		t.Errorf("Steps=%d stdout=%q", res.Steps, h.out.String())
	}
}
