package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/triiltz/sensei/internal/config"
)

// OpenAI talks to the Responses API. Chat Completions is not used because
// current OpenAI models only call functions there with reasoning disabled.
//
// The assistant's output items (messages, function calls and reasoning) are
// kept verbatim in Message.Raw and replayed as input on the next turn, which
// is how the Responses API expects reasoning to be carried across tool calls.
type OpenAI struct {
	transport
	url string
}

const defaultOpenAIURL = "https://api.openai.com/v1/responses"

// NewOpenAI returns an adapter for the OpenAI Responses API.
func NewOpenAI(cfg *config.Config) *OpenAI {
	return &OpenAI{transport: newTransport("openai", cfg), url: defaultOpenAIURL}
}

func (o *OpenAI) Name() string  { return "openai" }
func (o *OpenAI) Model() string { return o.cfg.Model }

type openAIRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []json.RawMessage `json:"input"`
	Tools           []openAITool      `json:"tools,omitempty"`
	ToolChoice      string            `json:"tool_choice,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens"`
}

type openAITool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIResponse struct {
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type openAIOutputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Chat sends one request to the Responses API.
func (o *OpenAI) Chat(ctx context.Context, req Request) (Response, error) {
	input, err := o.encodeInput(req.Messages)
	if err != nil {
		return Response{}, err
	}

	payload := openAIRequest{
		Model:           o.cfg.Model,
		Instructions:    req.System,
		Input:           input,
		MaxOutputTokens: maxTokens(req),
	}
	for _, t := range req.Tools {
		payload.Tools = append(payload.Tools, openAITool{
			Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		})
	}
	if req.NoToolCalls && len(payload.Tools) > 0 {
		payload.ToolChoice = "none"
	}

	var result openAIResponse
	timing, err := o.postJSON(ctx, o.url, map[string]string{
		"Authorization": "Bearer " + o.cfg.APIKey,
	}, payload, &result)
	if err != nil {
		return Response{Timing: timing}, err
	}

	resp, err := o.decode(result)
	resp.Timing = timing
	return resp, err
}

func (o *OpenAI) encodeInput(msgs []Message) ([]json.RawMessage, error) {
	var input []json.RawMessage
	add := func(v any) {
		b, _ := json.Marshal(v)
		input = append(input, b)
	}

	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			add(map[string]string{"role": "user", "content": m.Content})

		case RoleAssistant:
			if len(m.Raw) > 0 {
				var items []json.RawMessage
				if err := json.Unmarshal(m.Raw, &items); err != nil {
					return nil, fmt.Errorf("openai: invalid raw assistant message: %w", err)
				}
				input = append(input, items...)
				continue
			}
			if m.Content != "" {
				add(map[string]string{"role": "assistant", "content": m.Content})
			}
			for _, tc := range m.ToolCalls {
				add(map[string]string{
					"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": string(tc.Arguments),
				})
			}

		case RoleTool:
			add(map[string]string{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		}
	}
	return input, nil
}

func (o *OpenAI) decode(r openAIResponse) (Response, error) {
	if r.Status == "failed" {
		msg := "unknown error"
		if r.Error != nil {
			msg = r.Error.Message
		}
		return Response{}, fmt.Errorf("openai: request failed: %s", msg)
	}

	msg := Message{Role: RoleAssistant}
	var texts []string
	for _, raw := range r.Output {
		var item openAIOutputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return Response{}, fmt.Errorf("openai: failed to parse output item: %w", err)
		}
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					texts = append(texts, c.Text)
				}
			}
		case "function_call":
			args := item.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Arguments: json.RawMessage(args)})
		}
	}
	msg.Content = strings.TrimSpace(strings.Join(texts, "\n"))
	msg.Raw, _ = json.Marshal(r.Output)

	if msg.Content == "" && len(msg.ToolCalls) == 0 && r.Status != "incomplete" {
		return Response{}, fmt.Errorf("openai: empty response from API")
	}

	stop := StopEndTurn
	switch {
	case r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "max_output_tokens":
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
