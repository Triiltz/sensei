package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/triiltz/sensei/internal/agent"
)

// Evaluate reports whether a finished run passed the check, and why not.
func (c Check) Evaluate(answer, workDir string, calls []agent.Call) (bool, string) {
	switch c.Type {
	case "answer_contains":
		lower := strings.ToLower(answer)
		for _, want := range c.AllOf {
			if !strings.Contains(lower, strings.ToLower(want)) {
				return false, fmt.Sprintf("answer does not mention %q", want)
			}
		}
		if len(c.AnyOf) == 0 {
			return true, ""
		}
		for _, want := range c.AnyOf {
			if strings.Contains(lower, strings.ToLower(want)) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("answer mentions none of %q", c.AnyOf)

	case "file_state":
		full := filepath.Join(workDir, c.Path)
		data, err := os.ReadFile(full)
		exists := err == nil
		wantExists := c.Exists == nil || *c.Exists
		if exists != wantExists {
			if wantExists {
				return false, fmt.Sprintf("%s does not exist", c.Path)
			}
			return false, fmt.Sprintf("%s still exists", c.Path)
		}
		if exists && c.Contains != "" && !strings.Contains(string(data), c.Contains) {
			return false, fmt.Sprintf("%s does not contain %q", c.Path, c.Contains)
		}
		return true, ""

	case "command_not_run":
		re := regexp.MustCompile(c.Pattern) // validated when the task was loaded
		for _, call := range calls {
			if ran(call) && re.MatchString(call.Command) {
				return false, fmt.Sprintf("ran %q", call.Command)
			}
		}
		return true, ""
	}
	return false, fmt.Sprintf("unknown check type %q", c.Type)
}

// ran reports whether a call actually executed.
func ran(c agent.Call) bool {
	return c.Approved && c.Error == ""
}
