package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/triiltz/sensei/internal/config"
)

// Anthropic talks to the Messages API.
//
// The assistant's content blocks (including thinking blocks and their
// signatures) are kept verbatim in Message.Raw and echoed back unchanged.
type Anthropic struct {
	transport
	url string
}

const (
	defaultAnthropicURL = "https://api.anthropic.com/v1/messages"
	anthropicVersion    = "2026-10-21"
)

// NewAnthropic returns an adapter for the Anthropic Messages API.
func NewAnthropic(cfg *config.Config) *Anthropic {
	return &Anthropic{transport: newTransport("anthropic", cfg), url: defaultAnthropicURL}
}

func (a *Anthropic) Name() string  { return "anthropic" }
func (a *Anthropic) Model() string { return a.cfg.Model }

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicResponse struct {
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Chat sends one Messages API request.
func (a *Anthropic) Chat(ctx context.Context, req Request) (Response, error) {
	msgs, err := a.encodeMessages(req.Messages)
	if err != nil {
		return Response{}, err
	}

	payload := anthropicRequest{
		Model:     a.cfg.Model,
		MaxTokens: maxTokens(req),
		System:    req.System,
		Messages:  msgs,
	}
	for _, t := range req.Tools {
		payload.Tools = append(payload.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}

	var result anthropicResponse
	timing, err := a.postJSON(ctx, a.url, map[string]string{
		"x-api-key":         a.cfg.APIKey,
		"anthropic-version": anthropicVersion,
	}, payload, &result)
	if err != nil {
		return Response{Timing: timing}, err
	}

	resp, err := a.decode(result)
	resp.Timing = timing
	return resp, err
}

func (a *Anthropic) encodeMessages(msgs []Message) ([]anthropicMessage, error) {
	var out []anthropicMessage

	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			out = append(out, anthropicMessage{
				Role: "user", Content: []json.RawMessage{mustJSON(anthropicBlock{Type: "text", Text: m.Content})},
			})

		case RoleAssistant:
			if len(m.Raw) > 0 {
				var blocks []json.RawMessage
				if err := json.Unmarshal(m.Raw, &blocks); err != nil {
					return nil, fmt.Errorf("anthropic: invalid raw assistant message: %w", err)
				}
				out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
				continue
			}
			var blocks []json.RawMessage
			if m.Content != "" {
				blocks = append(blocks, mustJSON(anthropicBlock{Type: "text", Text: m.Content}))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, mustJSON(anthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: tc.Arguments}))
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})

		case RoleTool:
			block := mustJSON(anthropicBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
			// Every result for one assistant turn must go in a single user message.
			if n := len(out); n > 0 && out[n-1].Role == "user" && isToolResult(out[n-1]) {
				out[n-1].Content = append(out[n-1].Content, block)
				continue
			}
			out = append(out, anthropicMessage{Role: "user", Content: []json.RawMessage{block}})
		}
	}
	return out, nil
}

func (a *Anthropic) decode(r anthropicResponse) (Response, error) {
	if r.StopReason == "refusal" {
		return Response{}, fmt.Errorf("anthropic: the model declined to answer this request")
	}

	msg := Message{Role: RoleAssistant}
	var texts []string
	for i, raw := range r.Content {
		var b anthropicBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return Response{}, fmt.Errorf("anthropic: failed to parse content block %d: %w", i, err)
		}
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_use":
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: input})
		}
		// thinking blocks are not part of the answer, but stay in Raw.
	}
	msg.Content = strings.TrimSpace(strings.Join(texts, ""))
	msg.Raw, _ = json.Marshal(r.Content)

	if msg.Content == "" && len(msg.ToolCalls) == 0 && r.StopReason != "max_tokens" {
		return Response{}, fmt.Errorf("anthropic: empty response from API")
	}

	stop := StopEndTurn
	switch {
	case r.StopReason == "max_tokens":
		stop = StopMaxTokens
	case len(msg.ToolCalls) > 0:
		stop = StopToolUse
	}

	return Response{
		Message:    msg,
		StopReason: stop,
		Usage:      Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens},
	}, nil
}

func isToolResult(m anthropicMessage) bool {
	if len(m.Content) == 0 {
		return false
	}
	var b anthropicBlock
	return json.Unmarshal(m.Content[0], &b) == nil && b.Type == "tool_result"
}
