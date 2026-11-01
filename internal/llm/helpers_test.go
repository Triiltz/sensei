package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/triiltz/sensei/internal/config"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

const testKey = "test-secret-key"

// fakeResp is one canned HTTP response.
type fakeResp struct {
	status int
	body   string
	header map[string]string
}

func ok(body string) fakeResp { return fakeResp{status: http.StatusOK, body: body} }

// recorder captures every request the fake server receives.
type recorder struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	paths   []string
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// newServer replies with resps in order, repeating the last one.
func newServer(t *testing.T, resps ...fakeResp) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		i := len(rec.bodies)
		rec.bodies = append(rec.bodies, body)
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Unlock()

		resp := resps[min(i, len(resps)-1)]
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.status)
		io.WriteString(w, resp.body)
	}))
	t.Cleanup(ts.Close)
	return ts, rec
}

func testConfig(p config.Provider, model string) *config.Config {
	return &config.Config{APIKey: testKey, Provider: p, Model: model, KeyVar: "SENSEI_API_KEY"}
}

// sleeps records backoff waits instead of sleeping.
type sleeps struct{ waits []time.Duration }

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

func newTestOpenAI(ts *httptest.Server, s *sleeps) *OpenAI {
	o := NewOpenAI(testConfig(config.ProviderOpenAI, "gpt-test"))
	o.url, o.client, o.sleep = ts.URL, ts.Client(), s.sleep
	return o
}

func newTestGemini(ts *httptest.Server, s *sleeps) *Gemini {
	g := NewGemini(testConfig(config.ProviderGemini, "gemini-test"))
	g.baseURL, g.client, g.sleep = ts.URL, ts.Client(), s.sleep
	return g
}

func newTestAnthropic(ts *httptest.Server, s *sleeps) *Anthropic {
	a := NewAnthropic(testConfig(config.ProviderAnthropic, "claude-test"))
	a.url, a.client, a.sleep = ts.URL, ts.Client(), s.sleep
	return a
}

var testTool = Tool{
	Name:        "run_command",
	Description: "Run a shell command.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"reason":{"type":"string"}},"required":["command","reason"]}`),
}

// sampleRequest covers every message kind: user, assistant with text and a
// tool call (no Raw, so the adapter builds it), and a tool result.
var sampleRequest = Request{
	System: "You are a test.",
	Messages: []Message{
		{Role: RoleUser, Content: "list the files"},
		{Role: RoleAssistant, Content: "Checking.", ToolCalls: []ToolCall{
			{ID: "call_1", Name: "run_command", Arguments: json.RawMessage(`{"command":"ls","reason":"see the files"}`)},
		}},
		{Role: RoleTool, ToolCallID: "call_1", ToolName: "run_command", Content: "a.txt\nb.txt"},
	},
	Tools:     []Tool{testTool},
	MaxTokens: 1024,
}

// assertGolden compares a JSON body with testdata/<name>, ignoring key order
// and whitespace. Run `go test ./internal/llm -update` to rewrite it.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	gotNorm := normalizeJSON(t, got)

	if *update {
		if err := os.WriteFile(path, append(gotNorm, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run with -update to create it): %v", err)
	}
	if !bytes.Equal(gotNorm, normalizeJSON(t, want)) {
		t.Errorf("request does not match %s\ngot:\n%s", path, gotNorm)
	}
}

func normalizeJSON(t *testing.T, b []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return out
}

// field digs into a decoded JSON body, e.g. field(t, body, "input", 1).
func field(t *testing.T, body []byte, path ...any) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("path %v: %T is not an object", path, v)
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("path %v: no index %d", path, k)
			}
			v = a[k]
		}
	}
	return v
}

// jsonEqual reports whether a decoded value matches a JSON literal.
func jsonEqual(t *testing.T, got any, want string) bool {
	t.Helper()
	g, _ := json.Marshal(got)
	return bytes.Equal(normalizeJSON(t, g), normalizeJSON(t, []byte(want)))
}
