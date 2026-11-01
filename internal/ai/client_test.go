package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/triiltz/sensei/internal/config"
)

// newTestClient builds a Client pointed at ts and records any warnings emitted.
func newTestClient(ts *httptest.Server, provider config.Provider, model string, warnings *[]string) *Client {
	c := NewClient(&config.Config{
		APIKey:   "test-key",
		Provider: provider,
		Model:    model,
	})
	c.httpClient = ts.Client()
	c.openAIURL = ts.URL
	c.geminiBase = ts.URL
	c.warnf = func(format string, args ...any) {
		*warnings = append(*warnings, format)
	}
	return c
}

func TestAskOpenAISuccess(t *testing.T) {
	var gotAuth, gotBody string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"  hello  "}}]}`)
	}))
	defer ts.Close()

	var warnings []string
	c := newTestClient(ts, config.ProviderOpenAI, "gpt-4o-mini", &warnings)

	got, err := c.Ask("hi", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("response = %q, want %q (should be trimmed)", got, "hello")
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}

	var req openAIRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body is not valid JSON: %v", err)
	}
	if req.Model != "gpt-4o-mini" {
		t.Errorf("model = %q, want %q", req.Model, "gpt-4o-mini")
	}
	if len(req.Messages) != 2 || req.Messages[0].Content != TerminalSystemPrompt {
		t.Errorf("expected the terminal system prompt as the first message")
	}
}

func TestAskUsesPipeSystemPrompt(t *testing.T) {
	var gotBody string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer ts.Close()

	var warnings []string
	c := newTestClient(ts, config.ProviderOpenAI, "gpt-4o-mini", &warnings)

	if _, err := c.Ask("explain", "#!/bin/bash\necho hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req openAIRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body is not valid JSON: %v", err)
	}
	if req.Messages[0].Content != PipeSystemPrompt {
		t.Error("expected the pipe system prompt when piped content is present")
	}
	if !strings.Contains(req.Messages[1].Content, "echo hi") {
		t.Error("expected the piped content to be included in the user message")
	}
}

func TestAskWarnsOnTruncation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer ts.Close()

	var warnings []string
	c := newTestClient(ts, config.ProviderOpenAI, "gpt-4o-mini", &warnings)

	big := strings.Repeat("x", maxPipeContentLen+1)
	if _, err := c.Ask("explain", big); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0], "truncated") {
		t.Errorf("expected a truncation warning, got %v", warnings)
	}
}

func TestAskDoesNotWarnBelowLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer ts.Close()

	var warnings []string
	c := newTestClient(ts, config.ProviderOpenAI, "gpt-4o-mini", &warnings)

	if _, err := c.Ask("explain", strings.Repeat("x", 100)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestHTTPErrorsAreActionable(t *testing.T) {
	tests := []struct {
		name        string
		provider    config.Provider
		status      int
		body        string
		contentType string
		wantParts   []string
	}{
		{
			name:      "openai 401 points at the api key",
			provider:  config.ProviderOpenAI,
			status:    http.StatusUnauthorized,
			body:      `{"error":{"message":"Incorrect API key provided"}}`,
			wantParts: []string{"authentication failed", "401", "Incorrect API key provided", "SENSEI_API_KEY"},
		},
		{
			name:      "openai 429 points at rate limits",
			provider:  config.ProviderOpenAI,
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"message":"Rate limit reached"}}`,
			wantParts: []string{"rate limit", "429", "Rate limit reached"},
		},
		{
			name:        "gateway HTML does not become a parse error",
			provider:    config.ProviderOpenAI,
			status:      http.StatusBadGateway,
			body:        "<html><body>502 Bad Gateway</body></html>",
			contentType: "text/html",
			wantParts:   []string{"unavailable", "502", "Bad Gateway"},
		},
		{
			name:      "gemini 404 points at the model name",
			provider:  config.ProviderGemini,
			status:    http.StatusNotFound,
			body:      `{"error":{"message":"models/bogus is not found"}}`,
			wantParts: []string{"not found", "404", "bogus", "SENSEI_MODEL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer ts.Close()

			var warnings []string
			c := newTestClient(ts, tt.provider, "bogus", &warnings)

			_, err := c.Ask("hi", "")
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q does not mention %q", err, part)
				}
			}
			if strings.Contains(err.Error(), "failed to parse response") {
				t.Errorf("HTTP failure surfaced as a parse error: %v", err)
			}
		})
	}
}

func TestAskGeminiSuccess(t *testing.T) {
	var gotPath, gotKey, gotBody string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}`)
	}))
	defer ts.Close()

	var warnings []string
	c := newTestClient(ts, config.ProviderGemini, "gemini-2.5-flash", &warnings)

	got, err := c.Ask("ping", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "pong" {
		t.Errorf("response = %q, want %q", got, "pong")
	}
	if want := "/models/gemini-2.5-flash:generateContent"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotKey != "test-key" {
		t.Errorf("x-goog-api-key = %q, want %q", gotKey, "test-key")
	}
	if !strings.Contains(gotBody, "systemInstruction") {
		t.Error("expected the system prompt to be sent as systemInstruction")
	}
}

func TestAskEmptyResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer ts.Close()

	var warnings []string
	c := newTestClient(ts, config.ProviderOpenAI, "gpt-4o-mini", &warnings)

	if _, err := c.Ask("hi", ""); err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Errorf("expected an empty-response error, got %v", err)
	}
}

func TestAskUnsupportedProvider(t *testing.T) {
	c := NewClient(&config.Config{APIKey: "k", Provider: config.Provider("bogus")})
	if _, err := c.Ask("hi", ""); err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected an unsupported-provider error, got %v", err)
	}
}
