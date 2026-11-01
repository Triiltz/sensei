package llm

import (
	"context"
	"encoding/json"
	"testing"
)

const geminiText = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`

func TestGeminiEncodeRequest(t *testing.T) {
	ts, rec := newServer(t, ok(geminiText))
	if _, err := newTestGemini(ts, &sleeps{}).Chat(context.Background(), sampleRequest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertGolden(t, "gemini_request.json", rec.bodies[0])
	if got := rec.headers[0].Get("x-goog-api-key"); got != testKey {
		t.Errorf("x-goog-api-key = %q", got)
	}
	if rec.paths[0] != "/models/gemini-test:generateContent" {
		t.Errorf("path = %q", rec.paths[0])
	}
}

func TestGeminiDecode(t *testing.T) {
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
			name:      "text parts are joined and thoughts skipped",
			body:      `{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me think","thought":true},{"text":"Hello "},{"text":"there."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"thoughtsTokenCount":5}}`,
			wantText:  "Hello there.",
			wantStop:  StopEndTurn,
			wantUsage: Usage{InputTokens: 12, OutputTokens: 8},
		},
		{
			name: "single tool call without an id gets a stable one",
			body: `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"ls","reason":"see"}},"thoughtSignature":"c2lnMQ=="}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":9}}`,
			wantCalls: []ToolCall{
				{ID: "sensei_call_0_0", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls","reason":"see"}`)},
			},
			wantStop:  StopToolUse,
			wantUsage: Usage{InputTokens: 20, OutputTokens: 9},
		},
		{
			name: "multiple tool calls keep native ids",
			body: `{"candidates":[{"content":{"role":"model","parts":[
				{"functionCall":{"id":"fc_a","name":"run_command","args":{"command":"ls"}}},
				{"functionCall":{"id":"fc_b","name":"run_command"}}
			]},"finishReason":"STOP"}]}`,
			wantCalls: []ToolCall{
				{ID: "fc_a", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls"}`)},
				{ID: "fc_b", Name: "run_command", Arguments: json.RawMessage(`{}`)},
			},
			wantStop: StopToolUse,
		},
		{
			name:     "max tokens",
			body:     `{"candidates":[{"content":{"parts":[{"text":"Partial"}]},"finishReason":"MAX_TOKENS"}]}`,
			wantText: "Partial",
			wantStop: StopMaxTokens,
		},
		{
			name:    "blocked prompt reports the reason",
			body:    `{"promptFeedback":{"blockReason":"SAFETY"}}`,
			wantErr: "blocked (SAFETY)",
		},
		{
			name:    "empty candidate reports the finish reason",
			body:    `{"candidates":[{"content":{"parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
			wantErr: "MALFORMED_FUNCTION_CALL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := newServer(t, ok(tt.body))
			resp, err := newTestGemini(ts, &sleeps{}).Chat(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			checkDecoded(t, resp, err, tt.wantText, tt.wantCalls, tt.wantStop, tt.wantUsage, tt.wantErr)
		})
	}
}

func TestGeminiRoundTripPreservesThoughtSignature(t *testing.T) {
	callPart := `{"functionCall":{"name":"run_command","args":{"command":"ls","reason":"see"}},"thoughtSignature":"c2lnMQ=="}`
	first := `{"candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true,"thoughtSignature":"c2lnMA=="},` + callPart + `]},"finishReason":"STOP"}]}`

	ts, rec := newServer(t, ok(first), ok(geminiText))
	g := newTestGemini(ts, &sleeps{})

	msgs := []Message{{Role: RoleUser, Content: "list files"}}
	resp, err := g.Chat(context.Background(), Request{Messages: msgs, Tools: []Tool{testTool}})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}

	msgs = append(msgs, resp.Message, Message{
		Role: RoleTool, ToolCallID: resp.Message.ToolCalls[0].ID, ToolName: "run_command", Content: "a.txt",
	})
	if _, err := g.Chat(context.Background(), Request{Messages: msgs, Tools: []Tool{testTool}}); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	second := rec.bodies[1]
	if role := field(t, second, "contents", 1, "role"); role != "model" {
		t.Errorf("assistant turn role = %v, want model", role)
	}
	if !jsonEqual(t, field(t, second, "contents", 1, "parts", 1), callPart) {
		t.Errorf("function call part not replayed verbatim: %v", field(t, second, "contents", 1, "parts", 1))
	}
	// The generated ID never reaches Gemini.
	want := `{"role":"user","parts":[{"functionResponse":{"name":"run_command","response":{"output":"a.txt"}}}]}`
	if !jsonEqual(t, field(t, second, "contents", 2), want) {
		t.Errorf("tool result has the wrong shape: %v", field(t, second, "contents", 2))
	}
}

func TestGeminiGroupsParallelResults(t *testing.T) {
	ts, rec := newServer(t, ok(geminiText))
	_, err := newTestGemini(ts, &sleeps{}).Chat(context.Background(), Request{Messages: []Message{
		{Role: RoleUser, Content: "check both"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "fc_a", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls"}`)},
			{ID: "fc_b", Name: "run_command", Arguments: json.RawMessage(`{"command":"pwd"}`)},
		}},
		{Role: RoleTool, ToolCallID: "fc_a", ToolName: "run_command", Content: "a.txt"},
		{Role: RoleTool, ToolCallID: "fc_b", ToolName: "run_command", Content: "/tmp"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := `{"role":"user","parts":[
		{"functionResponse":{"id":"fc_a","name":"run_command","response":{"output":"a.txt"}}},
		{"functionResponse":{"id":"fc_b","name":"run_command","response":{"output":"/tmp"}}}
	]}`
	if got := field(t, rec.bodies[0], "contents"); len(got.([]any)) != 3 {
		t.Fatalf("got %d contents, want 3 (results grouped in one)", len(got.([]any)))
	}
	if !jsonEqual(t, field(t, rec.bodies[0], "contents", 2), want) {
		t.Errorf("results not grouped: %v", field(t, rec.bodies[0], "contents", 2))
	}
}
