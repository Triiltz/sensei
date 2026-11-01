package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
- Only output a COMMAND= line when the user is explicitly asking you to PROVIDE a command to accomplish a task.
- Do NOT output COMMAND= when the user is asking a conceptual question.
- CRITICAL: If the command requires placeholders (like <branch-name> ou <file>) because the user didn't specify exact names, do NOT output a COMMAND= line.
- CRITICAL: NEVER output more than one COMMAND= line. If the solution requires multiple commands, do NOT output COMMAND=, just write them in the explanation.
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

// Default provider endpoints. They are fields on Client so tests can point
// them at an httptest server.
const (
	defaultOpenAIURL = "https://api.openai.com/v1/chat/completions"
	defaultGeminiURL = "https://generativelanguage.googleapis.com/v1beta"
)

// Client handles communication with the AI provider's API.
type Client struct {
	cfg        *config.Config
	httpClient *http.Client

	openAIURL  string
	geminiBase string

	// warnf reports non-fatal notices (such as input truncation) to the user.
	warnf func(format string, args ...any)
}

// NewClient creates a new AI client from the given config.
func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		openAIURL:  defaultOpenAIURL,
		geminiBase: defaultGeminiURL,
		warnf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
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
			c.warnf("Warning: piped input was truncated from %d to %d characters.", len(pipeContent), maxPipeContentLen)
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

// apiErrorBody covers the error envelope used by both providers.
type apiErrorBody struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// httpError turns a non-2xx response into an actionable message. Providers
// usually return a JSON error envelope, but gateways and proxies return HTML,
// so the raw body is used as a fallback instead of a parse failure.
func (c *Client) httpError(provider string, status int, body []byte) error {
	detail := ""
	var parsed apiErrorBody
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != nil {
		detail = parsed.Error.Message
	}
	if detail == "" {
		detail = truncate(strings.TrimSpace(string(body)), 300)
	}
	if detail == "" {
		detail = http.StatusText(status)
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: authentication failed (HTTP %d): %s\n\n"+
			"Check that SENSEI_API_KEY holds a valid %s key and that SENSEI_PROVIDER matches it",
			provider, status, detail, provider)
	case http.StatusNotFound:
		return fmt.Errorf("%s: model %q not found (HTTP %d): %s\n\n"+
			"Check SENSEI_MODEL, or unset it to use the default",
			provider, c.cfg.Model, status, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: rate limit or quota exceeded (HTTP %d): %s\n\n"+
			"Wait a moment and try again, or check your billing/usage limits",
			provider, status, detail)
	default:
		if status >= 500 {
			return fmt.Errorf("%s: provider is unavailable (HTTP %d): %s\n\n"+
				"This is on the provider's side — try again shortly",
				provider, status, detail)
		}
		return fmt.Errorf("%s: API error (HTTP %d): %s", provider, status, detail)
	}
}

// truncate shortens s to at most n characters, marking that it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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

	req, err := http.NewRequest("POST", c.openAIURL, bytes.NewReader(body))
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

	if resp.StatusCode >= 400 {
		return "", c.httpError("openai", resp.StatusCode, respBody)
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

	url := fmt.Sprintf("%s/models/%s:generateContent", c.geminiBase, c.cfg.Model)

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

	if resp.StatusCode >= 400 {
		return "", c.httpError("gemini", resp.StatusCode, respBody)
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
