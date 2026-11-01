package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Run statuses.
const (
	StatusPass  = "pass"
	StatusFail  = "fail"
	StatusError = "error" // provider error or broken setup, counted as a failure
	// StatusQuota means the provider's rate limit or quota ran out. It says
	// nothing about the model, so it is excluded from the success rate and
	// re-run on --resume.
	StatusQuota = "quota_exhausted"
)

// Run is the record of one task attempt by one model.
type Run struct {
	TaskID   string `json:"task_id"`
	Category string `json:"category"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Repeat   int    `json:"repeat"` // 0-based index among repeats

	Status string `json:"status"`
	Detail string `json:"detail,omitempty"` // why it failed, or the error

	Steps        int      `json:"steps"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd"`   // nil when the model has no price
	LatencyMS    int64    `json:"latency_ms"` // time in model requests
	BackoffMS    int64    `json:"backoff_ms"` // time waiting between retries
	WallMS       int64    `json:"wall_ms"`

	Commands          []Command `json:"commands"`
	DangerousAttempts int       `json:"dangerous_attempts"`
	HitMaxSteps       bool      `json:"hit_max_steps"`
	Answer            string    `json:"answer"`
	StartedAt         time.Time `json:"started_at"`
}

// Command is one run_command call made during a run.
type Command struct {
	Command  string `json:"command"`
	Tier     string `json:"tier"`
	Approved bool   `json:"approved"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Error    string `json:"error,omitempty"`
}

// key identifies a run across resumes.
func (r Run) key() string {
	return fmt.Sprintf("%s/%s/%s/%d", r.Provider, r.Model, r.TaskID, r.Repeat)
}

// done reports whether a run needs no retry on --resume.
func (r Run) done() bool {
	return r.Status != "" && r.Status != StatusQuota
}

// Results is the content of an evals/results/<timestamp>.json file.
type Results struct {
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Runs      []Run     `json:"runs"`
}

// LoadResults reads a results file written by a previous run.
func LoadResults(path string) (*Results, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Results
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// upsert adds run, replacing an earlier attempt with the same key.
func (r *Results) upsert(run Run) {
	for i := range r.Runs {
		if r.Runs[i].key() == run.key() {
			r.Runs[i] = run
			return
		}
	}
	r.Runs = append(r.Runs, run)
}

// save writes the JSON atomically, so an interrupted run never leaves a
// truncated file behind, and refreshes the Markdown summary next to it.
func (r *Results) save(jsonPath string) error {
	r.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(jsonPath, data); err != nil {
		return err
	}
	md := jsonPath[:len(jsonPath)-len(filepath.Ext(jsonPath))] + ".md"
	return writeAtomic(md, []byte(Report(r)))
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Price is the cost of a model in USD per million tokens.
type Price struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// Prices maps model IDs to prices; see evals/prices.yaml.
type Prices map[string]Price

// LoadPrices reads the hand-maintained price table. A missing file is not
// an error: costs are then reported as unknown.
func LoadPrices(path string) (Prices, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Prices{}, nil
	}
	if err != nil {
		return nil, err
	}
	var p Prices
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// cost returns the USD cost of a run, or nil if the model has no price.
func (p Prices) cost(model string, in, out int) *float64 {
	price, ok := p[model]
	if !ok {
		return nil
	}
	c := float64(in)/1e6*price.Input + float64(out)/1e6*price.Output
	return &c
}
