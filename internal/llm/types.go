// Package llm defines a provider-neutral chat interface with native tool
// calling, and adapters for OpenAI, Gemini and Anthropic.
package llm

import (
	"context"
	"encoding/json"
	"time"
)

// Role is the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn of a conversation.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant messages
	ToolCallID string     // tool-result messages
	ToolName   string     // tool-result messages (Gemini needs the name)

	// Provider-specific opaque data that must be sent back unchanged on the
	// next turn (e.g. Gemini thought signatures, Anthropic content blocks).
	// Only the adapter that produced it reads it; everything else ignores it.
	Raw json.RawMessage
}

// Tool describes a function the model may call.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema object
}

// ToolCall is a model's request to call a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Usage counts the tokens a request consumed.
type Usage struct{ InputTokens, OutputTokens int }

// Timing splits the wall-clock cost of a request: time spent in HTTP calls,
// and time spent waiting between retries.
type Timing struct{ Latency, Backoff time.Duration }

// Normalized stop reasons.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// Request is a provider-neutral chat request.
type Request struct {
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int // 0 means the adapter's default
}

// Response is a provider-neutral chat response.
type Response struct {
	Message    Message // assistant message, may contain ToolCalls
	StopReason string  // one of the Stop* constants
	Usage      Usage
	Timing     Timing
}

// Provider is a chat model behind a specific API.
type Provider interface {
	Name() string
	Model() string
	Chat(ctx context.Context, req Request) (Response, error)
}

// defaultMaxTokens is used when a request does not set MaxTokens. It is
// generous so answers are not cut mid-sentence; models stop well before it.
const defaultMaxTokens = 16000

func maxTokens(req Request) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return defaultMaxTokens
}
