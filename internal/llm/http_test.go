package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/triiltz/sensei/internal/config"
)

const openAIText = `{"status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":1}}`

func TestRetry(t *testing.T) {
	tests := []struct {
		name      string
		resps     []fakeResp
		wantErr   string
		wantWaits []time.Duration
		wantCalls int
	}{
		{
			name: "429 honours Retry-After in seconds",
			resps: []fakeResp{
				{status: 429, body: `{"error":{"message":"slow down"}}`, header: map[string]string{"Retry-After": "2"}},
				ok(openAIText),
			},
			wantWaits: []time.Duration{2 * time.Second},
			wantCalls: 2,
		},
		{
			name: "Gemini's RetryInfo in the body is honoured when there is no header",
			resps: []fakeResp{
				{status: 429, body: `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"27.149s"}]}}`},
				ok(openAIText),
			},
			wantWaits: []time.Duration{28 * time.Second},
			wantCalls: 2,
		},
		{
			name: "5xx backs off exponentially",
			resps: []fakeResp{
				{status: 503, body: "busy"},
				{status: 502, body: "busy"},
				{status: 500, body: "busy"},
				ok(openAIText),
			},
			wantWaits: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
			wantCalls: 4,
		},
		{
			name:      "gives up after three retries",
			resps:     []fakeResp{{status: 503, body: "busy"}},
			wantErr:   "unavailable (HTTP 503)",
			wantWaits: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
			wantCalls: 4,
		},
		{
			name:      "4xx is not retried",
			resps:     []fakeResp{{status: 400, body: `{"error":{"message":"bad schema"}}`}},
			wantErr:   "bad schema",
			wantCalls: 1,
		},
		{
			name: "a Retry-After beyond the cap fails fast instead of hanging",
			resps: []fakeResp{
				{status: 429, body: `{"error":{"message":"daily quota"}}`, header: map[string]string{"Retry-After": "3600"}},
			},
			wantErr:   "daily quota",
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, rec := newServer(t, tt.resps...)
			s := &sleeps{}
			resp, err := newTestOpenAI(ts, s).Chat(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if rec.count() != tt.wantCalls {
				t.Errorf("server got %d requests, want %d", rec.count(), tt.wantCalls)
			}
			if len(s.waits) != len(tt.wantWaits) {
				t.Fatalf("waits = %v, want %v", s.waits, tt.wantWaits)
			}
			var total time.Duration
			for i := range s.waits {
				if s.waits[i] != tt.wantWaits[i] {
					t.Errorf("wait %d = %v, want %v", i, s.waits[i], tt.wantWaits[i])
				}
				total += s.waits[i]
			}
			if resp.Timing.Backoff != total {
				t.Errorf("Timing.Backoff = %v, want %v (reported separately from latency)", resp.Timing.Backoff, total)
			}
		})
	}
}

func TestRetryStopsWhenCancelled(t *testing.T) {
	ts, rec := newServer(t, fakeResp{status: 503, body: "busy"})
	o := newTestOpenAI(ts, &sleeps{})

	ctx, cancel := context.WithCancel(context.Background())
	o.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return sleepCtx(ctx, d)
	}

	_, err := o.Chat(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rec.count() != 1 {
		t.Errorf("server got %d requests after cancellation, want 1", rec.count())
	}
}

func TestBackoffParsesHTTPDate(t *testing.T) {
	when := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	d := backoff(0, when)
	if d < 8*time.Second || d > 10*time.Second {
		t.Errorf("backoff(%q) = %v, want about 10s", when, d)
	}
	if d := backoff(2, "not a date"); d != 4*time.Second {
		t.Errorf("unparseable Retry-After should fall back to exponential backoff, got %v", d)
	}
}

func TestHTTPErrorsAreActionable(t *testing.T) {
	tests := []struct {
		name      string
		provider  config.Provider
		status    int
		body      string
		wantParts []string
	}{
		{
			name:      "401 points at the variable holding the key",
			provider:  config.ProviderOpenAI,
			status:    http.StatusUnauthorized,
			body:      `{"error":{"message":"Incorrect API key provided"}}`,
			wantParts: []string{"openai", "authentication failed", "401", "Incorrect API key provided", "SENSEI_API_KEY"},
		},
		{
			name:      "429 points at rate limits",
			provider:  config.ProviderGemini,
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`,
			wantParts: []string{"gemini", "rate limit", "429", "Resource exhausted"},
		},
		{
			name:      "gateway HTML does not become a parse error",
			provider:  config.ProviderAnthropic,
			status:    http.StatusBadGateway,
			body:      "<html><body>502 Bad Gateway</body></html>",
			wantParts: []string{"anthropic", "unavailable", "502", "Bad Gateway"},
		},
		{
			name:      "404 points at the model name",
			provider:  config.ProviderGemini,
			status:    http.StatusNotFound,
			body:      `{"error":{"message":"models/gemini-test is not found"}}`,
			wantParts: []string{"not found", "404", "gemini-test", "SENSEI_MODEL"},
		},
		{
			name:      "long bodies are truncated",
			provider:  config.ProviderAnthropic,
			status:    http.StatusBadRequest,
			body:      strings.Repeat("x", 1000),
			wantParts: []string{"HTTP 400", strings.Repeat("x", 300) + "..."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := newServer(t, fakeResp{status: tt.status, body: tt.body})
			s := &sleeps{}
			var p Provider
			switch tt.provider {
			case config.ProviderOpenAI:
				p = newTestOpenAI(ts, s)
			case config.ProviderGemini:
				p = newTestGemini(ts, s)
			case config.ProviderAnthropic:
				p = newTestAnthropic(ts, s)
			}

			_, err := p.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, part := range tt.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q does not mention %q", err, part)
				}
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("error leaks the API key: %v", err)
			}
			if strings.Contains(err.Error(), "failed to parse response") {
				t.Errorf("HTTP failure surfaced as a parse error: %v", err)
			}
		})
	}
}

func TestNewSelectsAdapter(t *testing.T) {
	for _, p := range []config.Provider{config.ProviderOpenAI, config.ProviderGemini, config.ProviderAnthropic} {
		got, err := New(testConfig(p, "m"))
		if err != nil {
			t.Fatalf("New(%s): %v", p, err)
		}
		if got.Name() != string(p) || got.Model() != "m" {
			t.Errorf("New(%s) = %s/%s", p, got.Name(), got.Model())
		}
	}
	if _, err := New(testConfig("mistral", "m")); err == nil {
		t.Error("expected an error for an unknown provider")
	}
}
