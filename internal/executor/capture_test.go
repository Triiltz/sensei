package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCapture(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantStdout string
		wantStderr string
		wantCode   int
	}{
		{name: "stdout", command: "echo hello", wantStdout: "hello\n"},
		{name: "stderr and exit code", command: "echo oops >&2; exit 3", wantStderr: "oops\n", wantCode: 3},
		{name: "no stdin", command: "cat", wantStdout: ""},
		{name: "pipes work", command: "printf 'b\\na\\n' | sort", wantStdout: "a\nb\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Capture(context.Background(), tt.command, 5*time.Second)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != tt.wantStdout || res.Stderr != tt.wantStderr || res.ExitCode != tt.wantCode {
				t.Errorf("got stdout=%q stderr=%q code=%d", res.Stdout, res.Stderr, res.ExitCode)
			}
			if res.TimedOut {
				t.Error("TimedOut should be false")
			}
		})
	}
}

func TestCaptureTimeoutKillsTheProcessGroup(t *testing.T) {
	start := time.Now()
	// The sleep is a child of the shell; killing only the shell would leave
	// it holding stdout open.
	res, err := Capture(context.Background(), "echo started; sleep 30 | cat", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Errorf("TimedOut=%v ExitCode=%d, want true/-1", res.TimedOut, res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "started") {
		t.Errorf("output before the timeout was lost: %q", res.Stdout)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Capture took %v; the timeout did not kill the command", elapsed)
	}
}

func TestCaptureBackgroundProcessDoesNotHang(t *testing.T) {
	start := time.Now()
	res, err := Capture(context.Background(), "sleep 30 & echo done", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "done") {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Capture took %v waiting on a background process", elapsed)
	}
}

func TestCaptureCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := Capture(ctx, "sleep 30", 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCaptureLimitsMemory(t *testing.T) {
	res, err := Capture(context.Background(), "head -c 3000000 /dev/zero | tr '\\0' x", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Stdout) > maxStreamBytes+100 {
		t.Errorf("kept %d bytes, want at most about %d", len(res.Stdout), maxStreamBytes)
	}
	if !strings.Contains(res.Stdout, "more bytes not captured") {
		t.Error("dropped output is not marked")
	}
}

func TestTruncateMiddle(t *testing.T) {
	if got := TruncateMiddle("short", 100); got != "short" {
		t.Errorf("short input changed: %q", got)
	}

	long := strings.Repeat("a", 50) + strings.Repeat("z", 50)
	got := TruncateMiddle(long, 20)
	if !strings.HasPrefix(got, "aaaaaaaaaa\n") || !strings.HasSuffix(got, "\nzzzzzzzzzz") {
		t.Errorf("head/tail not kept: %q", got)
	}
	if !strings.Contains(got, "[... 80 bytes omitted ...]") {
		t.Errorf("cut not marked: %q", got)
	}

	// Multibyte characters straddling the cut must not be split.
	accents := strings.Repeat("ção", 40)
	if got := TruncateMiddle(accents, 31); !utf8.ValidString(got) {
		t.Errorf("result is not valid UTF-8: %q", got)
	}
}

func TestResultFormat(t *testing.T) {
	got := Result{Stdout: "a.txt\n", ExitCode: 0}.Format(30 * time.Second)
	want := "exit_code: 0\nstdout:\na.txt\nstderr:\n(empty)"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	got = Result{TimedOut: true, ExitCode: -1}.Format(30 * time.Second)
	if !strings.Contains(got, "killed after the 30s timeout") {
		t.Errorf("timeout not reported: %s", got)
	}
}
