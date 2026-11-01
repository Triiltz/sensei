package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIEncodeRequest(t *testing.T) {
	ts, rec := newServer(t, ok(openAIText))
	if _, err := newTestOpenAI(ts, &sleeps{}).Chat(context.Background(), sampleRequest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertGolden(t, "openai_request.json", rec.bodies[0])
	if got := rec.headers[0].Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}
}

func TestOpenAIDecode(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantCalls []ToolCall
		wantStop  string
		wantUsage Usage
		wantErr   string
	}{
		{
			name:      "text only",
			body:      `{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"  Hello there.  "}]}],"usage":{"input_tokens":12,"output_tokens":3}}`,
			wantText:  "Hello there.",
			wantStop:  StopEndTurn,
			wantUsage: Usage{InputTokens: 12, OutputTokens: 3},
		},
		{
			name: "single tool call",
			body: `{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"run_command","arguments":"{\"command\":\"ls\",\"reason\":\"see\"}"}],"usage":{"input_tokens":20,"output_tokens":9}}`,
			wantCalls: []ToolCall{
				{ID: "call_1", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls","reason":"see"}`)},
			},
			wantStop:  StopToolUse,
			wantUsage: Usage{InputTokens: 20, OutputTokens: 9},
		},
		{
			name: "multiple tool calls with text",
			body: `{"status":"completed","output":[
				{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Checking both."}]},
				{"type":"function_call","id":"fc_1","call_id":"call_1","name":"run_command","arguments":"{\"command\":\"ls\"}"},
				{"type":"function_call","id":"fc_2","call_id":"call_2","name":"run_command","arguments":""}
			],"usage":{}}`,
			wantText: "Checking both.",
			wantCalls: []ToolCall{
				{ID: "call_1", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls"}`)},
				{ID: "call_2", Name: "run_command", Arguments: json.RawMessage(`{}`)},
			},
			wantStop: StopToolUse,
		},
		{
			name:     "max output tokens",
			body:     `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"Partial"}]}],"usage":{}}`,
			wantText: "Partial",
			wantStop: StopMaxTokens,
		},
		{
			name:    "failed status",
			body:    `{"status":"failed","error":{"message":"server exploded"},"output":[]}`,
			wantErr: "server exploded",
		},
		{
			name:    "empty output",
			body:    `{"status":"completed","output":[]}`,
			wantErr: "empty response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := newServer(t, ok(tt.body))
			resp, err := newTestOpenAI(ts, &sleeps{}).Chat(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			checkDecoded(t, resp, err, tt.wantText, tt.wantCalls, tt.wantStop, tt.wantUsage, tt.wantErr)
		})
	}
}

func TestOpenAIRoundTripPreservesRaw(t *testing.T) {
	reasoning := `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque=="}`
	call := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"run_command","arguments":"{\"command\":\"ls\",\"reason\":\"see\"}","status":"completed"}`
	first := `{"status":"completed","output":[` + reasoning + `,` + call + `],"usage":{}}`

	ts, rec := newServer(t, ok(first), ok(openAIText))
	o := newTestOpenAI(ts, &sleeps{})

	msgs := []Message{{Role: RoleUser, Content: "list files"}}
	resp, err := o.Chat(context.Background(), Request{Messages: msgs, Tools: []Tool{testTool}})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}

	msgs = append(msgs, resp.Message, Message{
		Role: RoleTool, ToolCallID: resp.Message.ToolCalls[0].ID, ToolName: "run_command", Content: "a.txt",
	})
	if _, err := o.Chat(context.Background(), Request{Messages: msgs, Tools: []Tool{testTool}}); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	second := rec.bodies[1]
	if !jsonEqual(t, field(t, second, "input", 1), reasoning) {
		t.Errorf("reasoning item not replayed verbatim: %v", field(t, second, "input", 1))
	}
	if !jsonEqual(t, field(t, second, "input", 2), call) {
		t.Errorf("function call not replayed verbatim: %v", field(t, second, "input", 2))
	}
	if !jsonEqual(t, field(t, second, "input", 3), `{"type":"function_call_output","call_id":"call_1","output":"a.txt"}`) {
		t.Errorf("tool result has the wrong shape: %v", field(t, second, "input", 3))
	}
}

// checkDecoded is shared by the decode tests of every adapter.
func checkDecoded(t *testing.T, resp Response, err error, wantText string, wantCalls []ToolCall, wantStop string, wantUsage Usage, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("err = %v, want it to contain %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Role != RoleAssistant {
		t.Errorf("Role = %q, want assistant", resp.Message.Role)
	}
	if resp.Message.Content != wantText {
		t.Errorf("Content = %q, want %q", resp.Message.Content, wantText)
	}
	if resp.StopReason != wantStop {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, wantStop)
	}
	if resp.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, wantUsage)
	}
	if len(resp.Message.ToolCalls) != len(wantCalls) {
		t.Fatalf("got %d tool calls, want %d: %+v", len(resp.Message.ToolCalls), len(wantCalls), resp.Message.ToolCalls)
	}
	for i, want := range wantCalls {
		got := resp.Message.ToolCalls[i]
		if got.ID != want.ID || got.Name != want.Name {
			t.Errorf("call %d = %s/%s, want %s/%s", i, got.ID, got.Name, want.ID, want.Name)
		}
		if string(normalizeJSON(t, got.Arguments)) != string(normalizeJSON(t, want.Arguments)) {
			t.Errorf("call %d arguments = %s, want %s", i, got.Arguments, want.Arguments)
		}
	}
	if len(resp.Message.Raw) == 0 {
		t.Error("Raw is empty; the next turn could not replay this message")
	}
}
