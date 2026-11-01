package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	// maxStreamBytes caps what is kept of each stream in memory while the
	// command runs; anything past it is counted but not stored.
	maxStreamBytes = 1 << 20
	// toolOutputBytes is the budget per stream in the text sent back to the
	// model, split between the head and the tail.
	toolOutputBytes = 4 << 10
)

// Result is the outcome of a captured command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Duration time.Duration
}

// Capture runs command in the current directory and environment. See
// Sandbox.Capture.
func Capture(ctx context.Context, command string, timeout time.Duration) (Result, error) {
	return Sandbox{}.Capture(ctx, command, timeout)
}

// Sandbox sets where captured commands run. The zero value inherits the
// current directory and environment. It isolates (a different directory and
// HOME), it does not contain: commands still run as the current user.
type Sandbox struct {
	Dir string   // working directory
	Env []string // full environment, as in exec.Cmd.Env
}

// Capture runs command through /bin/sh with no stdin and a timeout, and
// returns its output instead of streaming it. It is used in agent mode,
// where the output goes back to the model.
//
// The command runs in its own process group so a timeout or Ctrl+C kills
// everything it started, not just the shell.
func (s Sandbox) Capture(ctx context.Context, command string, timeout time.Duration) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// /bin/sh rather than $SHELL: the model writes POSIX commands, and fish
	// or nushell would reject many of them.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	stdout := &limitedBuffer{max: maxStreamBytes}
	stderr := &limitedBuffer{max: maxStreamBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// Background processes that keep the pipes open must not hang us.
	cmd.WaitDelay = time.Second

	start := time.Now()
	err := cmd.Run()
	if cmd.Process != nil {
		// Nothing the command started outlives it.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // the shell exited; only a background child held the pipes
	}
	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}

	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	case ctx.Err() != nil:
		return res, ctx.Err() // cancelled by the user
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	case err != nil:
		return res, fmt.Errorf("could not run command: %w", err)
	}
	return res, nil
}

// Format renders a result as the tool output sent to the model.
func (r Result) Format(timeout time.Duration) string {
	var b strings.Builder
	if r.TimedOut {
		fmt.Fprintf(&b, "exit_code: none (killed after the %s timeout)\n", timeout)
	} else {
		fmt.Fprintf(&b, "exit_code: %d\n", r.ExitCode)
	}
	fmt.Fprintf(&b, "stdout:\n%s\n", orEmpty(TruncateMiddle(r.Stdout, toolOutputBytes)))
	fmt.Fprintf(&b, "stderr:\n%s", orEmpty(TruncateMiddle(r.Stderr, toolOutputBytes)))
	return b.String()
}

func orEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return strings.TrimRight(s, "\n")
}

// TruncateMiddle keeps the head and the tail of s within about max bytes and
// marks the cut. Cuts land on rune boundaries so the result stays valid UTF-8.
func TruncateMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2

	head := half
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	tail := len(s) - half
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}

	return fmt.Sprintf("%s\n[... %d bytes omitted ...]\n%s", s[:head], tail-head, s[tail:])
}

// limitedBuffer stores up to max bytes and silently drops the rest, so a
// command that floods its output cannot exhaust memory.
type limitedBuffer struct {
	buf     bytes.Buffer
	max     int
	dropped int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) <= room {
			l.buf.Write(p)
		} else {
			l.buf.Write(p[:room])
			l.dropped += len(p) - room
		}
	} else {
		l.dropped += len(p)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string {
	if l.dropped == 0 {
		return l.buf.String()
	}
	return fmt.Sprintf("%s\n[... %d more bytes not captured ...]", l.buf.String(), l.dropped)
}

// shellPath is the shell used for interactive (streamed) commands.
func shellPath() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}
