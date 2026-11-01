package llm

import (
	"context"
	"encoding/json"
	"testing"
)

const anthropicText = `{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`

func TestAnthropicEncodeRequest(t *testing.T) {
	ts, rec := newServer(t, ok(anthropicText))
	if _, err := newTestAnthropic(ts, &sleeps{}).Chat(context.Background(), sampleRequest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertGolden(t, "anthropic_request.json", rec.bodies[0])
	if got := rec.headers[0].Get("x-api-key"); got != testKey {
		t.Errorf("x-api-key = %q", got)
	}
	if got := rec.headers[0].Get("anthropic-version"); got != anthropicVersion {
		t.Errorf("anthropic-version = %q", got)
	}
}

func TestAnthropicMaxTokensIsAlwaysSent(t *testing.T) {
	ts, rec := newServer(t, ok(anthropicText))
	if _, err := newTestAnthropic(ts, &sleeps{}).Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := field(t, rec.bodies[0], "max_tokens"); got != float64(defaultMaxTokens) {
		t.Errorf("max_tokens = %v, want the default %d", got, defaultMaxTokens)
	}
}

func TestAnthropicDecode(t *testing.T) {
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
			name:      "text only, thinking skipped",
			body:      `{"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"Hello there."}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3}}`,
			wantText:  "Hello there.",
			wantStop:  StopEndTurn,
			wantUsage: Usage{InputTokens: 12, OutputTokens: 3},
		},
		{
			name: "single tool call",
			body: `{"content":[{"type":"tool_use","id":"toolu_1","name":"run_command","input":{"command":"ls","reason":"see"}}],"stop_reason":"tool_use","usage":{"input_tokens":20,"output_tokens":9}}`,
			wantCalls: []ToolCall{
				{ID: "toolu_1", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls","reason":"see"}`)},
			},
			wantStop:  StopToolUse,
			wantUsage: Usage{InputTokens: 20, OutputTokens: 9},
		},
		{
			name: "multiple tool calls with text",
			body: `{"content":[
				{"type":"text","text":"Checking both."},
				{"type":"tool_use","id":"toolu_1","name":"run_command","input":{"command":"ls"}},
				{"type":"tool_use","id":"toolu_2","name":"run_command","input":{"command":"pwd"}}
			],"stop_reason":"tool_use","usage":{}}`,
			wantText: "Checking both.",
			wantCalls: []ToolCall{
				{ID: "toolu_1", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls"}`)},
				{ID: "toolu_2", Name: "run_command", Arguments: json.RawMessage(`{"command":"pwd"}`)},
			},
			wantStop: StopToolUse,
		},
		{
			name:     "max tokens",
			body:     `{"content":[{"type":"text","text":"Partial"}],"stop_reason":"max_tokens","usage":{}}`,
			wantText: "Partial",
			wantStop: StopMaxTokens,
		},
		{
			name:    "refusal is an error",
			body:    `{"content":[],"stop_reason":"refusal","usage":{}}`,
			wantErr: "declined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := newServer(t, ok(tt.body))
			resp, err := newTestAnthropic(ts, &sleeps{}).Chat(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			checkDecoded(t, resp, err, tt.wantText, tt.wantCalls, tt.wantStop, tt.wantUsage, tt.wantErr)
		})
	}
}

func TestAnthropicRoundTripPreservesRaw(t *testing.T) {
	thinking := `{"type":"thinking","thinking":"","signature":"EqQBsig=="}`
	toolUse1 := `{"type":"tool_use","id":"toolu_1","name":"run_command","input":{"command":"ls","reason":"see"}}`
	toolUse2 := `{"type":"tool_use","id":"toolu_2","name":"run_command","input":{"command":"pwd","reason":"where"}}`
	first := `{"content":[` + thinking + `,` + toolUse1 + `,` + toolUse2 + `],"stop_reason":"tool_use","usage":{}}`

	ts, rec := newServer(t, ok(first), ok(anthropicText))
	a := newTestAnthropic(ts, &sleeps{})

	msgs := []Message{{Role: RoleUser, Content: "list files"}}
	resp, err := a.Chat(context.Background(), Request{Messages: msgs, Tools: []Tool{testTool}})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}

	msgs = append(msgs, resp.Message)
	for i, tc := range resp.Message.ToolCalls {
		msgs = append(msgs, Message{Role: RoleTool, ToolCallID: tc.ID, ToolName: tc.Name, Content: []string{"a.txt", "/tmp"}[i]})
	}
	if _, err := a.Chat(context.Background(), Request{Messages: msgs, Tools: []Tool{testTool}}); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	second := rec.bodies[1]
	if !jsonEqual(t, field(t, second, "messages", 1), `{"role":"assistant","content":[`+thinking+`,`+toolUse1+`,`+toolUse2+`]}`) {
		t.Errorf("assistant blocks not replayed verbatim: %v", field(t, second, "messages", 1))
	}
	want := `{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_1","content":"a.txt"},
		{"type":"tool_result","tool_use_id":"toolu_2","content":"/tmp"}
	]}`
	if !jsonEqual(t, field(t, second, "messages", 2), want) {
		t.Errorf("tool results must share one user message: %v", field(t, second, "messages", 2))
	}
	if n := len(field(t, second, "messages").([]any)); n != 3 {
		t.Errorf("got %d messages, want 3", n)
	}
}
