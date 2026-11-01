package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/triiltz/sensei/internal/config"
)

const (
	// maxRetries is how many times a 429 or 5xx is retried before giving up.
	maxRetries = 3
	// baseBackoff doubles on each retry: 1s, 2s, 4s.
	baseBackoff = time.Second
	// maxBackoff caps a single wait. A longer Retry-After (a daily quota, for
	// instance) is reported as an error instead of hanging the terminal.
	maxBackoff = 60 * time.Second
)

// New returns the adapter for the configured provider.
func New(cfg *config.Config) (Provider, error) {
	switch cfg.Provider {
	case config.ProviderOpenAI:
		return NewOpenAI(cfg), nil
	case config.ProviderGemini:
		return NewGemini(cfg), nil
	case config.ProviderAnthropic:
		return NewAnthropic(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}
}

// transport is the HTTP plumbing shared by every adapter: JSON encoding,
// retries with backoff, and actionable errors.
type transport struct {
	provider string
	cfg      *config.Config
	client   *http.Client

	// sleep waits between retries; tests replace it to avoid real waits.
	sleep func(ctx context.Context, d time.Duration) error
}

func newTransport(provider string, cfg *config.Config) transport {
	return transport{
		provider: provider,
		cfg:      cfg,
		client:   &http.Client{Timeout: 120 * time.Second},
		sleep:    sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// postJSON sends payload to url and decodes a successful response into out.
// 429 and 5xx responses are retried with exponential backoff, honouring
// Retry-After when the provider sends it.
func (t *transport) postJSON(ctx context.Context, url string, headers map[string]string, payload, out any) (Timing, error) {
	var timing Timing

	body, err := json.Marshal(payload)
	if err != nil {
		return timing, fmt.Errorf("%s: failed to encode request: %w", t.provider, err)
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return timing, fmt.Errorf("%s: failed to create request: %w", t.provider, err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		start := time.Now()
		resp, err := t.client.Do(req)
		if err != nil {
			timing.Latency += time.Since(start)
			if ctx.Err() != nil {
				return timing, ctx.Err()
			}
			return timing, fmt.Errorf("%s: request failed: %w", t.provider, err)
		}
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		timing.Latency += time.Since(start)
		if err != nil {
			return timing, fmt.Errorf("%s: failed to read response: %w", t.provider, err)
		}

		if resp.StatusCode < 400 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return timing, fmt.Errorf("%s: failed to parse response: %w", t.provider, err)
			}
			return timing, nil
		}

		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if !retryable || attempt == maxRetries {
			return timing, t.httpError(resp.StatusCode, respBody)
		}

		wait := backoff(attempt, resp.Header.Get("Retry-After"))
		if wait > maxBackoff {
			return timing, t.httpError(resp.StatusCode, respBody)
		}
		if err := t.sleep(ctx, wait); err != nil {
			return timing, err
		}
		timing.Backoff += wait
	}
}

// backoff returns how long to wait before retry number attempt+1.
func backoff(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if when, err := http.ParseTime(retryAfter); err == nil {
			if d := time.Until(when); d > 0 {
				return d
			}
			return 0
		}
	}
	return baseBackoff << attempt
}

// apiErrorBody covers the error envelope used by all three providers.
type apiErrorBody struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// httpError turns a non-2xx response into an actionable message. Providers
// usually return a JSON error envelope, but gateways and proxies return HTML,
// so the raw body is used as a fallback instead of a parse failure. The API
// key is never part of the message.
func (t *transport) httpError(status int, body []byte) error {
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
			"Check that %s holds a valid %s key, or set SENSEI_PROVIDER to the provider it belongs to",
			t.provider, status, detail, t.cfg.KeyVar, t.provider)
	case http.StatusNotFound:
		return fmt.Errorf("%s: model %q not found (HTTP %d): %s\n\n"+
			"Check SENSEI_MODEL, or unset it to use the default",
			t.provider, t.cfg.Model, status, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: rate limit or quota exceeded (HTTP %d): %s\n\n"+
			"Wait a moment and try again, or check your billing/usage limits",
			t.provider, status, detail)
	default:
		if status >= 500 {
			return fmt.Errorf("%s: provider is unavailable (HTTP %d): %s\n\n"+
				"This is on the provider's side — try again shortly",
				t.provider, status, detail)
		}
		return fmt.Errorf("%s: API error (HTTP %d): %s", t.provider, status, detail)
	}
}

// truncate shortens s to at most n bytes, marking that it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
