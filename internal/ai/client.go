package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/triiltz/sensei/internal/config"
)

// System prompts injected depending on the execution mode.
const (
	TerminalSystemPrompt = `You are SENSEI, a concise terminal assistant.

CRITICAL RULE — LANGUAGE:
You MUST reply in the EXACT same language the user writes their question in.
If the user writes in English, reply in English. If in Portuguese, reply in Portuguese. And so on. No exceptions.

Rules:
- Be extremely brief.
- Output plain text only (terminal-friendly).
- Do NOT use Markdown formatting (no **bold**, *italic*, headings, tables, inline backticks, or code fences).
- If you need a list, use plain lines starting with "- ".
- Only output a COMMAND= line when the user is explicitly asking you to PROVIDE a command to accomplish a task (e.g. "how do I do X?", "give me the command for X", "create a branch called X").
- Do NOT output COMMAND= when the user is asking a conceptual or explanatory question (e.g. "what does X do?", "what is X?", "explain X", "how does X work?"). In those cases, just answer briefly with an explanation.
- When you do output a command, write a short explanation (1-2 lines max) followed by a line starting with COMMAND= containing ONLY the exact command. Example:
  Extracts a tar.gz archive.
  COMMAND=tar -xzf archive.tar.gz
- Never wrap the COMMAND= line in backticks or code blocks.`

	PipeSystemPrompt = `You are SENSEI, a code/log analyst assistant.

CRITICAL RULE — LANGUAGE:
You MUST reply in the EXACT same language the user writes their question in.
If the user writes in English, reply in English. If in Portuguese, reply in Portuguese. And so on. No exceptions.

Rules:
- The user is piping content to you for analysis.
- Explain the provided content clearly and in detail.
- Point out potential issues, key logic, or important patterns.
- Be structured: use short paragraphs or plain bullet points with "- ".
- Output plain text only.
- Do NOT use Markdown formatting (no **bold**, *italic*, headings, tables, inline backticks, or code fences).`
)

// Client handles communication with the AI provider's API.
type Client struct {
	cfg        *config.Config
	httpClient *http.Client
}

// NewClient creates a new AI client from the given config.
func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// maxPipeContentLen is the maximum number of characters allowed from piped input.
// Content beyond this limit is truncated to avoid exceeding model token limits.
const maxPipeContentLen = 100_000

// Ask sends the user prompt (with optional piped context) to the AI and returns the response text.
func (c *Client) Ask(userPrompt string, pipeContent string) (string, error) {
	systemPrompt := TerminalSystemPrompt
	if pipeContent != "" {
		if len(pipeContent) > maxPipeContentLen {
			pipeContent = pipeContent[:maxPipeContentLen] + "\n\n[... content truncated due to size limit ...]"
		}
		systemPrompt = PipeSystemPrompt
		userPrompt = fmt.Sprintf("Content received via pipe:\n```\n%s\n```\n\nUser question: %s", pipeContent, userPrompt)
	}

	switch c.cfg.Provider {
	case config.ProviderOpenAI:
		return c.askOpenAI(systemPrompt, userPrompt)
	case config.ProviderGemini:
		return c.askGemini(systemPrompt, userPrompt)
	default:
		return "", fmt.Errorf("unsupported provider: %s", c.cfg.Provider)
	}
}

// ──────────────────────────────────────────────
// OpenAI (ChatGPT) implementation
// ──────────────────────────────────────────────

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) askOpenAI(systemPrompt, userPrompt string) (string, error) {
	payload := openAIRequest{
		Model: c.cfg.Model,
		Messages: []openAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("openai: failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("openai: failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("openai: failed to read response: %w", err)
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("openai: failed to parse response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("openai: API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai: empty response from API")
	}

	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

// ──────────────────────────────────────────────
// Google Gemini implementation
// ──────────────────────────────────────────────

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) askGemini(systemPrompt, userPrompt string) (string, error) {
	payload := geminiRequest{
		SystemInstruction: &geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		},
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: userPrompt}}},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("gemini: failed to marshal request: %w", err)
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent",
		c.cfg.Model,
	)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("gemini: failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("gemini: failed to read response: %w", err)
	}

	var result geminiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("gemini: failed to parse response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("gemini: API error: %s", result.Error.Message)
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini: empty response from API")
	}

	return strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text), nil
}
