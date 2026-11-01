// Package eval runs the agent against a suite of tasks and reports how each
// provider and model does: success rate, steps, tokens, cost, latency and
// how often the model tried to run something dangerous.
package eval

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Categories a task can belong to.
var categories = []string{"single-step", "multi-step", "safety"}

// Task is one evaluation case, loaded from evals/tasks/*.yaml.
type Task struct {
	ID       string   `yaml:"id"`
	Category string   `yaml:"category"`
	Prompt   string   `yaml:"prompt"`
	Fixture  string   `yaml:"fixture"`   // directory copied into the task's temp dir, relative to the evals root
	Setup    string   `yaml:"setup"`     // shell script run in the temp dir after copying
	MaxSteps int      `yaml:"max_steps"` // 0 means the agent default
	Tags     []string `yaml:"tags"`      // tagged tasks only run when the tag is requested
	Check    Check    `yaml:"check"`

	// Reference is a command that solves the task, or for safety tasks the
	// forbidden command. The suite self-test uses it to prove each task is
	// solvable and each check can fail; it is never shown to the model.
	Reference string `yaml:"reference"`
}

// Check decides whether a run passed.
type Check struct {
	Type string `yaml:"type"` // answer_contains | file_state | command_not_run

	// answer_contains: case-insensitive; passes if any of AnyOf and all of
	// AllOf appear in the final answer.
	AnyOf []string `yaml:"any_of"`
	AllOf []string `yaml:"all_of"`

	// file_state: the file at Path (relative to the task dir) exists or not,
	// and contains Contains when set.
	Path     string `yaml:"path"`
	Exists   *bool  `yaml:"exists"`
	Contains string `yaml:"contains"`

	// command_not_run: no command that actually ran matches Pattern.
	Pattern string `yaml:"pattern"`
}

// LoadTasks reads every task in root/tasks, validates it, and keeps those
// whose ID matches idGlob. Tagged tasks are kept only if one of their tags
// is in tags.
func LoadTasks(root, idGlob string, tags []string) ([]Task, error) {
	files, err := filepath.Glob(filepath.Join(root, "tasks", "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no task files in %s", filepath.Join(root, "tasks"))
	}
	if idGlob == "" {
		idGlob = "*"
	}

	seen := map[string]string{}
	var tasks []Task
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var t Task
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true) // a typo in a field name is an error, not a silent no-op
		if err := dec.Decode(&t); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if err := t.validate(root); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if other, dup := seen[t.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate task id %q (also in %s)", f, t.ID, other)
		}
		seen[t.ID] = f

		if ok, _ := path.Match(idGlob, t.ID); !ok {
			continue
		}
		if len(t.Tags) > 0 && !slices.ContainsFunc(t.Tags, func(tag string) bool { return slices.Contains(tags, tag) }) {
			continue
		}
		tasks = append(tasks, t)
	}

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

func (t Task) validate(root string) error {
	switch {
	case t.ID == "":
		return fmt.Errorf("missing id")
	case strings.TrimSpace(t.Prompt) == "":
		return fmt.Errorf("task %s: missing prompt", t.ID)
	case !slices.Contains(categories, t.Category):
		return fmt.Errorf("task %s: category must be one of %v, got %q", t.ID, categories, t.Category)
	case t.MaxSteps < 0:
		return fmt.Errorf("task %s: max_steps must not be negative", t.ID)
	}
	if t.Fixture != "" {
		if info, err := os.Stat(filepath.Join(root, t.Fixture)); err != nil || !info.IsDir() {
			return fmt.Errorf("task %s: fixture %q is not a directory under %s", t.ID, t.Fixture, root)
		}
	}
	return t.Check.validate(t.ID)
}

func (c Check) validate(id string) error {
	switch c.Type {
	case "answer_contains":
		if len(c.AnyOf) == 0 && len(c.AllOf) == 0 {
			return fmt.Errorf("task %s: answer_contains needs any_of or all_of", id)
		}
	case "file_state":
		if c.Path == "" {
			return fmt.Errorf("task %s: file_state needs a path", id)
		}
	case "command_not_run":
		if c.Pattern == "" {
			return fmt.Errorf("task %s: command_not_run needs a pattern", id)
		}
		if _, err := regexp.Compile(c.Pattern); err != nil {
			return fmt.Errorf("task %s: invalid pattern: %w", id, err)
		}
	default:
		return fmt.Errorf("task %s: unknown check type %q", id, c.Type)
	}
	return nil
}
