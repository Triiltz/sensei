package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/triiltz/sensei/internal/executor"
	"github.com/triiltz/sensei/internal/llm"
)

// fakeProvider returns a canned response and records the request.
type fakeProvider struct {
	resp llm.Response
	err  error
	got  llm.Request
}

func (f *fakeProvider) Name() string  { return "fake" }
func (f *fakeProvider) Model() string { return "fake-model" }
func (f *fakeProvider) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	f.got = req
	return f.resp, f.err
}

func call(id, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: RunCommandName, Arguments: json.RawMessage(args)}
}

func TestAsk(t *testing.T) {
	tests := []struct {
		name      string
		pipe      string
		resp      llm.Response
		wantText  string
		wantCmds  []executor.Command
		wantWarns int
	}{
		{
			name:     "conceptual answer",
			resp:     llm.Response{Message: llm.Message{Content: "A pipe connects two processes."}},
			wantText: "A pipe connects two processes.",
		},
		{
			name: "tool call becomes a command",
			resp: llm.Response{Message: llm.Message{Content: "Lists files.", ToolCalls: []llm.ToolCall{
				call("c1", `{"command":" ls -la ","reason":"See the files."}`),
			}}},
			wantText: "Lists files.",
			wantCmds: []executor.Command{{Command: "ls -la", Reason: "See the files."}},
		},
		{
			name: "multiple tool calls are all returned",
			resp: llm.Response{Message: llm.Message{ToolCalls: []llm.ToolCall{
				call("c1", `{"command":"git add .","reason":"stage"}`),
				call("c2", `{"command":"git commit -m wip","reason":"commit"}`),
			}}},
			wantCmds: []executor.Command{
				{Command: "git add .", Reason: "stage"},
				{Command: "git commit -m wip", Reason: "commit"},
			},
		},
		{
			name: "malformed tool calls are dropped with a warning",
			resp: llm.Response{Message: llm.Message{ToolCalls: []llm.ToolCall{
				call("c1", `not json`),
				call("c2", `{"command":"","reason":"nothing"}`),
				{ID: "c3", Name: "delete_everything", Arguments: json.RawMessage(`{}`)},
			}}},
			wantWarns: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakeProvider{resp: tt.resp}
			var warns []string
			answer, err := Ask(context.Background(), p, "question", tt.pipe, func(f string, a ...any) {
				warns = append(warns, f)
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if answer.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", answer.Text, tt.wantText)
			}
			if len(answer.Commands) != len(tt.wantCmds) {
				t.Fatalf("Commands = %+v, want %+v", answer.Commands, tt.wantCmds)
			}
			for i := range tt.wantCmds {
				if answer.Commands[i] != tt.wantCmds[i] {
					t.Errorf("command %d = %+v, want %+v", i, answer.Commands[i], tt.wantCmds[i])
				}
			}
			if len(warns) != tt.wantWarns {
				t.Errorf("warnings = %v, want %d", warns, tt.wantWarns)
			}
		})
	}
}

func TestAskOneShotOffersRunCommand(t *testing.T) {
	p := &fakeProvider{resp: llm.Response{Message: llm.Message{Content: "ok"}}}
	if _, err := Ask(context.Background(), p, "list files", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.got.System != OneShotSystemPrompt {
		t.Error("expected the one-shot system prompt")
	}
	if len(p.got.Tools) != 1 || p.got.Tools[0].Name != RunCommandName {
		t.Errorf("Tools = %+v, want only run_command", p.got.Tools)
	}
	if len(p.got.Messages) != 1 || p.got.Messages[0].Content != "list files" {
		t.Errorf("Messages = %+v", p.got.Messages)
	}
}

func TestAskPipeModeHasNoTools(t *testing.T) {
	p := &fakeProvider{resp: llm.Response{Message: llm.Message{Content: "ok"}}}
	if _, err := Ask(context.Background(), p, "summarize", "diff --git a b", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.got.System != PipeSystemPrompt {
		t.Error("expected the pipe system prompt")
	}
	if len(p.got.Tools) != 0 {
		t.Errorf("pipe mode must not offer tools, got %+v", p.got.Tools)
	}
	if !strings.Contains(p.got.Messages[0].Content, "diff --git a b") {
		t.Error("piped content missing from the prompt")
	}
}

func TestAskTruncatesLargePipes(t *testing.T) {
	p := &fakeProvider{resp: llm.Response{Message: llm.Message{Content: "ok"}}}
	var warns []string
	warnf := func(f string, a ...any) { warns = append(warns, f) }

	if _, err := Ask(context.Background(), p, "explain", strings.Repeat("x", maxPipeContentLen+1), warnf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "truncated") {
		t.Errorf("expected a truncation warning, got %v", warns)
	}

	warns = nil
	if _, err := Ask(context.Background(), p, "explain", strings.Repeat("x", 100), warnf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("expected no warnings below the limit, got %v", warns)
	}
}

func TestAskPropagatesProviderErrors(t *testing.T) {
	p := &fakeProvider{err: errors.New("boom")}
	if _, err := Ask(context.Background(), p, "hi", "", nil); err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
}
