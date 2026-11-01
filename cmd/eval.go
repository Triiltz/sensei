package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/triiltz/sensei/internal/config"
	"github.com/triiltz/sensei/internal/eval"
	"github.com/triiltz/sensei/internal/executor"
	"github.com/triiltz/sensei/internal/llm"
)

var evalFlags struct {
	root, tasks, tags            string
	providers, models, repeatFor string
	repeat, parallel             int
	resume, outDir               string
	cmdTimeout                   time.Duration
	yes                          bool
}

var evalCmd = &cobra.Command{
	Use:    "eval",
	Short:  "Benchmark providers and models on the task suite in evals/",
	Hidden: true,
	Long: `Runs every task in evals/tasks against each selected provider/model and writes
evals/results/<timestamp>.json (raw runs) and .md (summary table).

Each task runs in a fresh temporary directory with a temporary HOME and no
API keys in the environment. That is isolation, not a sandbox: commands still
run as your user. Dangerous commands are always denied.

This calls the real provider APIs and uses quota or money.

Examples:
  sensei eval --providers gemini --tasks 'find-*'
  sensei eval --providers gemini,openai,anthropic --repeat 3 --repeat-for gemini=1
  sensei eval --models gemini=gemini-3.5-flash-lite,gemini=gemini-3.8-flash
  sensei eval --resume evals/results/2026-11-02T10-00-00.json`,
	Args: cobra.NoArgs,
	RunE: runEval,
}

func init() {
	f := evalCmd.Flags()
	f.StringVar(&evalFlags.root, "root", "evals", "Directory with tasks/, fixtures/ and prices.yaml")
	f.StringVar(&evalFlags.tasks, "tasks", "*", "Glob of task IDs to run, e.g. 'safety-*'")
	f.StringVar(&evalFlags.tags, "tags", "", "Also run tasks with these tags, e.g. docker")
	f.StringVar(&evalFlags.providers, "providers", "gemini", "Comma-separated providers: gemini, openai, anthropic")
	f.StringVar(&evalFlags.models, "models", "", "Models per provider, e.g. gemini=gemini-3.8-flash,openai=gpt-6-luna")
	f.IntVar(&evalFlags.repeat, "repeat", 1, "Runs per task and model (outputs are non-deterministic)")
	f.StringVar(&evalFlags.repeatFor, "repeat-for", "", "Repeat override per provider or provider:model, e.g. gemini=1,openai=3")
	f.IntVar(&evalFlags.parallel, "parallel", 1, "Runs in parallel (keep 1 on free tiers)")
	f.StringVar(&evalFlags.resume, "resume", "", "Continue a results file: skip finished runs, retry quota-exhausted ones")
	f.StringVar(&evalFlags.outDir, "out-dir", "evals/results", "Where new results files are written")
	f.DurationVar(&evalFlags.cmdTimeout, "cmd-timeout", 30*time.Second, "Timeout for each command the agent runs")
	f.BoolVarP(&evalFlags.yes, "yes", "y", false, "Do not ask before calling the APIs")
	rootCmd.AddCommand(evalCmd)
}

func runEval(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	fl := evalFlags

	tasks, err := eval.LoadTasks(fl.root, fl.tasks, splitComma(fl.tags))
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks match %q", fl.tasks)
	}
	targets, err := eval.ParseTargets(fl.providers, fl.models, fl.repeat, fl.repeatFor)
	if err != nil {
		return err
	}
	prices, err := eval.LoadPrices(filepath.Join(fl.root, "prices.yaml"))
	if err != nil {
		return err
	}

	// Check every key before spending anything.
	for _, t := range targets {
		if _, err := config.LoadFor(t.Provider, t.Model); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	}

	results := &eval.Results{CreatedAt: time.Now().UTC()}
	outPath := filepath.Join(fl.outDir, time.Now().Format("2006-01-02T15-04-05")+".json")
	if fl.resume != "" {
		if results, err = eval.LoadResults(fl.resume); err != nil {
			return err
		}
		outPath = fl.resume
	} else if err := os.MkdirAll(fl.outDir, 0o755); err != nil {
		return err
	}

	runs := 0
	fmt.Fprintf(os.Stderr, "%d tasks × targets:\n", len(tasks))
	for _, t := range targets {
		fmt.Fprintf(os.Stderr, "  %-40s ×%d\n", t, t.Repeat)
		runs += len(tasks) * t.Repeat
	}
	fmt.Fprintf(os.Stderr, "Up to %d runs (minus those already done), each a few model requests. Results: %s\n", runs, outPath)

	if !fl.yes {
		ok, err := executor.Confirm(ctx, os.Stderr, "This calls the real APIs. Continue? [y/N]: ")
		if err != nil || !ok {
			return err
		}
	}

	err = eval.RunAll(ctx, eval.Options{
		Root:       fl.root,
		Tasks:      tasks,
		Targets:    targets,
		Parallel:   fl.parallel,
		CmdTimeout: fl.cmdTimeout,
		Prices:     prices,
		Results:    results,
		OutPath:    outPath,
		Log:        os.Stderr,
		NewProvider: func(t eval.Target) (llm.Provider, error) {
			cfg, err := config.LoadFor(t.Provider, t.Model)
			if err != nil {
				return nil, err
			}
			return llm.New(cfg)
		},
	})
	if ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "\nInterrupted. Continue with: sensei eval --resume %s\n", outPath)
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nWrote %s and %s\n", outPath, strings.TrimSuffix(outPath, ".json")+".md")
	return nil
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
