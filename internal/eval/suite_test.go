package eval

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/triiltz/sensei/internal/agent"
	"github.com/triiltz/sensei/internal/policy"
)

// TestSuite validates the real task suite in evals/ without any model: every
// task loads, its setup runs, its reference solution passes the check, and
// the check fails when nothing is done. Safety tasks instead prove that the
// forbidden command is caught by both the check and the policy.
func TestSuite(t *testing.T) {
	const root = "../../evals"
	tasks, err := LoadTasks(root, "*", []string{"docker"})
	if err != nil {
		t.Fatalf("loading the suite: %v", err)
	}
	if len(tasks) < 20 {
		t.Errorf("the suite has %d tasks, want at least 20", len(tasks))
	}
	_, hasGit := exec.LookPath("git")

	for _, task := range tasks {
		t.Run(task.ID, func(t *testing.T) {
			if task.Reference == "" {
				t.Fatal("task has no reference solution")
			}
			if strings.Contains(task.Setup, "git ") && hasGit != nil {
				t.Skip("git is not installed")
			}

			if task.Check.Type == "command_not_run" {
				if d := policy.Classify(task.Reference); d.Tier != policy.Dangerous {
					t.Errorf("policy classifies the forbidden %q as %s (%s), want dangerous", task.Reference, d.Tier, d.Reason)
				}
				if ok, _ := task.Check.Evaluate("", "", []agent.Call{{Command: task.Reference, Approved: true}}); ok {
					t.Errorf("check does not catch %q", task.Reference)
				}
				if ok, _ := task.Check.Evaluate("", "", []agent.Call{{Command: task.Reference, Approved: false}}); !ok {
					t.Error("a denied command must not fail the check")
				}
				return
			}

			ctx := context.Background()
			work, sb, cleanup, err := prepare(ctx, root, task)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()

			if ok, _ := task.Check.Evaluate("", work, nil); ok {
				t.Error("check passes without doing anything")
			}

			res, err := sb.Capture(ctx, task.Reference, 10*time.Second)
			if err != nil || res.ExitCode != 0 {
				t.Fatalf("reference failed (exit %d): %v %s", res.ExitCode, err, res.Stderr)
			}
			if ok, detail := task.Check.Evaluate(res.Stdout, work, nil); !ok {
				t.Errorf("reference solution fails the check: %s\noutput:\n%s", detail, res.Stdout)
			}
		})
	}
}
