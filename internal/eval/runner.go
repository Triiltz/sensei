package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/triiltz/sensei/internal/agent"
	"github.com/triiltz/sensei/internal/config"
	"github.com/triiltz/sensei/internal/executor"
	"github.com/triiltz/sensei/internal/llm"
	"github.com/triiltz/sensei/internal/policy"
)

// setupTimeout bounds a task's setup script.
const setupTimeout = 60 * time.Second

// Target is a provider and model to evaluate, and how many times to run
// each task on it.
type Target struct {
	Provider config.Provider
	Model    string
	Repeat   int
}

func (t Target) String() string { return string(t.Provider) + "/" + t.Model }

// Options configures an evaluation.
type Options struct {
	Root       string // the evals directory (fixtures are relative to it)
	Tasks      []Task
	Targets    []Target
	Parallel   int
	CmdTimeout time.Duration
	Prices     Prices

	// Results is where runs are accumulated; pass a loaded file to resume.
	// It is saved to OutPath after every run.
	Results *Results
	OutPath string

	// NewProvider builds the client for a target. Tests inject a fake.
	NewProvider func(Target) (llm.Provider, error)

	Log io.Writer // progress lines
}

type job struct {
	target Target
	task   Task
	repeat int
}

// RunAll executes every (target, task, repeat) that is not already done in
// opts.Results. When a target runs out of quota, its remaining runs are
// skipped so a later --resume can pick them up.
func RunAll(ctx context.Context, opts Options) error {
	if opts.Results == nil {
		opts.Results = &Results{CreatedAt: time.Now().UTC()}
	}
	if opts.Parallel < 1 {
		opts.Parallel = 1
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}

	done := map[string]bool{}
	for _, r := range opts.Results.Runs {
		if r.done() {
			done[r.key()] = true
		}
	}

	// Interleave targets so a partial run still covers every model.
	var jobs []job
	for _, task := range opts.Tasks {
		for rep := 0; rep < maxRepeat(opts.Targets); rep++ {
			for _, target := range opts.Targets {
				if rep >= target.Repeat {
					continue
				}
				r := Run{Provider: string(target.Provider), Model: target.Model, TaskID: task.ID, Repeat: rep}
				if !done[r.key()] {
					jobs = append(jobs, job{target: target, task: task, repeat: rep})
				}
			}
		}
	}
	fmt.Fprintf(opts.Log, "%d runs to do (%d already done)\n", len(jobs), len(done))

	providers := map[string]llm.Provider{}
	for _, t := range opts.Targets {
		p, err := opts.NewProvider(t)
		if err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
		providers[t.String()] = p
	}

	var (
		mu        sync.Mutex
		exhausted = map[string]bool{}
		saveErr   error
		wg        sync.WaitGroup
		queue     = make(chan job)
	)

	worker := func() {
		defer wg.Done()
		for j := range queue {
			mu.Lock()
			skip := exhausted[j.target.String()]
			mu.Unlock()
			if skip || ctx.Err() != nil {
				continue
			}

			run := runOne(ctx, opts, providers[j.target.String()], j)
			if ctx.Err() != nil {
				continue // interrupted: leave it for --resume
			}

			mu.Lock()
			if run.Status == StatusQuota && !exhausted[j.target.String()] {
				exhausted[j.target.String()] = true
				fmt.Fprintf(opts.Log, "%s: quota exhausted, skipping its remaining runs (use --resume later)\n", j.target)
			}
			opts.Results.upsert(run)
			if err := opts.Results.save(opts.OutPath); err != nil && saveErr == nil {
				saveErr = err
			}
			fmt.Fprintf(opts.Log, "%-15s %-28s %-24s #%d %s\n", run.Status, j.target, j.task.ID, j.repeat+1, run.Detail)
			mu.Unlock()
		}
	}

	for i := 0; i < opts.Parallel; i++ {
		wg.Add(1)
		go worker()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()

	if saveErr != nil {
		return fmt.Errorf("saving results: %w", saveErr)
	}
	if err := opts.Results.save(opts.OutPath); err != nil {
		return fmt.Errorf("saving results: %w", err)
	}
	return ctx.Err()
}

func maxRepeat(targets []Target) int {
	n := 0
	for _, t := range targets {
		n = max(n, t.Repeat)
	}
	return n
}

// runOne runs a task once in a fresh temporary directory.
func runOne(ctx context.Context, opts Options, provider llm.Provider, j job) Run {
	run := Run{
		TaskID:    j.task.ID,
		Category:  j.task.Category,
		Provider:  string(j.target.Provider),
		Model:     j.target.Model,
		Repeat:    j.repeat,
		StartedAt: time.Now().UTC(),
	}
	start := time.Now()
	defer func() { run.WallMS = time.Since(start).Milliseconds() }()

	work, sandbox, cleanup, err := prepare(ctx, opts.Root, j.task)
	if err != nil {
		return failed(run, err)
	}
	defer cleanup()

	maxSteps := j.task.MaxSteps
	if maxSteps == 0 {
		maxSteps = 8
	}

	var answer bytes.Buffer
	result, err := agent.Run(ctx, agent.Config{
		Provider:   provider,
		Dir:        work,
		MaxSteps:   maxSteps,
		CmdTimeout: opts.CmdTimeout,
		Out:        &answer,
		Log:        io.Discard,
		// Unattended: confirm-tier commands are approved, dangerous ones are
		// always denied and counted.
		Approve: func(_ context.Context, _ agent.RunCommandArgs, d policy.Decision) (bool, error) {
			return d.Tier != policy.Dangerous, nil
		},
		Exec: sandbox.Capture,
	}, j.task.Prompt)

	run.Steps = result.Steps
	run.InputTokens = result.Usage.InputTokens
	run.OutputTokens = result.Usage.OutputTokens
	run.LatencyMS = result.Timing.Latency.Milliseconds()
	run.BackoffMS = result.Timing.Backoff.Milliseconds()
	run.HitMaxSteps = result.HitMaxSteps
	run.Answer = result.Answer
	run.CostUSD = opts.Prices.cost(j.target.Model, run.InputTokens, run.OutputTokens)
	for _, c := range result.Calls {
		run.Commands = append(run.Commands, Command{
			Command: c.Command, Tier: c.Tier.String(), Approved: c.Approved,
			ExitCode: c.ExitCode, TimedOut: c.TimedOut, Error: c.Error,
		})
		if c.Tier == policy.Dangerous {
			run.DangerousAttempts++
		}
	}

	switch {
	case err != nil && llm.IsRateLimited(err):
		run.Status, run.Detail = StatusQuota, firstLine(err.Error())
	case err != nil:
		run.Status, run.Detail = StatusError, firstLine(err.Error())
	default:
		ok, detail := j.task.Check.Evaluate(result.Answer, work, result.Calls)
		run.Status, run.Detail = StatusFail, detail
		if ok {
			run.Status = StatusPass
		}
	}
	return run
}

// prepare creates the task's temporary directory: the fixture copied into
// work/, an empty home/, and the setup script run. cleanup removes it all.
func prepare(ctx context.Context, root string, task Task) (work string, sb executor.Sandbox, cleanup func(), err error) {
	tmp, err := os.MkdirTemp("", "sensei-eval-")
	if err != nil {
		return "", sb, nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()

	work, home := filepath.Join(tmp, "work"), filepath.Join(tmp, "home")
	for _, dir := range []string{work, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", sb, nil, err
		}
	}
	if task.Fixture != "" {
		if err := copyDir(filepath.Join(root, task.Fixture), work); err != nil {
			return "", sb, nil, fmt.Errorf("copying fixture: %w", err)
		}
	}

	sb = executor.Sandbox{Dir: work, Env: sandboxEnv(home)}
	if strings.TrimSpace(task.Setup) != "" {
		res, err := sb.Capture(ctx, task.Setup, setupTimeout)
		if err == nil && (res.ExitCode != 0 || res.TimedOut) {
			err = fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		if err != nil {
			return "", sb, nil, fmt.Errorf("setup failed: %w", err)
		}
	}
	return work, sb, func() { os.RemoveAll(tmp) }, nil
}

func failed(run Run, err error) Run {
	run.Status, run.Detail = StatusError, firstLine(err.Error())
	return run
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// sandboxEnv is the environment for task commands: HOME points at an empty
// temp dir, API keys are removed so a command like `env` cannot leak them,
// and git works without the user's config.
func sandboxEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "HOME" || strings.HasPrefix(name, "SENSEI_") || strings.HasSuffix(name, "_API_KEY") ||
			strings.HasPrefix(name, "GIT_") || name == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Sensei Eval", "GIT_AUTHOR_EMAIL=eval@sensei.local",
		"GIT_COMMITTER_NAME=Sensei Eval", "GIT_COMMITTER_EMAIL=eval@sensei.local",
		"GIT_PAGER=cat", "PAGER=cat",
	)
}

// copyDir copies the regular files and directories under src into dst.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
