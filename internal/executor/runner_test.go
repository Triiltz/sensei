package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeTTY swaps the package seams for the duration of a test and records
// what was printed and which commands ran.
type fakeTTY struct {
	out bytes.Buffer
	ran []string
}

func setup(t *testing.T, ttyInput string, ttyErr error) *fakeTTY {
	t.Helper()
	f := &fakeTTY{}

	oldStdout, oldTTY, oldShell := stdout, openTTY, runShell
	t.Cleanup(func() { stdout, openTTY, runShell = oldStdout, oldTTY, oldShell })

	stdout = &f.out
	openTTY = func() (io.ReadCloser, error) {
		if ttyErr != nil {
			return nil, ttyErr
		}
		return io.NopCloser(strings.NewReader(ttyInput)), nil
	}
	runShell = func(_ context.Context, command string) error {
		f.ran = append(f.ran, command)
		return nil
	}
	return f
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		cmds       []Command
		force      bool
		ttyInput   string
		ttyErr     error
		wantRan    []string
		wantOutput []string
	}{
		{
			name:       "conceptual answer without a command",
			text:       "A pipe connects the stdout of one process to the stdin of another.",
			wantOutput: []string{"A pipe connects"},
		},
		{
			name:       "confirmed command runs",
			text:       "Extracts a tar.gz archive.",
			cmds:       []Command{{Command: "tar -xzf archive.tar.gz"}},
			ttyInput:   "y\n",
			wantRan:    []string{"tar -xzf archive.tar.gz"},
			wantOutput: []string{"Extracts a tar.gz archive.", "Suggested command: tar -xzf archive.tar.gz", "Run it? [y/N]"},
		},
		{
			name:       "anything but yes aborts",
			cmds:       []Command{{Command: "ls -la"}},
			ttyInput:   "\n",
			wantOutput: []string{"Aborted."},
		},
		{
			name:       "force skips the prompt",
			cmds:       []Command{{Command: "ls -la"}},
			force:      true,
			wantRan:    []string{"ls -la"},
			wantOutput: []string{"Suggested command: ls -la"},
		},
		{
			name:       "no terminal means no run",
			cmds:       []Command{{Command: "ls -la"}},
			ttyErr:     errors.New("no tty"),
			wantOutput: []string{"no terminal to confirm"},
		},
		{
			name:       "reason is shown when the model wrote no text",
			cmds:       []Command{{Command: "ls -la", Reason: "Lists the files."}},
			ttyInput:   "n\n",
			wantOutput: []string{"Lists the files."},
		},
		{
			name:       "multiple commands are shown as text, never executed",
			text:       "Do both:",
			cmds:       []Command{{Command: "git add ."}, {Command: "git commit -m wip"}},
			force:      true,
			wantOutput: []string{"Do both:\n  $ git add .\n  $ git commit -m wip"},
		},
		{
			name:       "placeholder blocks execution",
			text:       "Creates a branch.",
			cmds:       []Command{{Command: "git checkout -b <branch-name>"}},
			force:      true,
			wantOutput: []string{"  $ git checkout -b <branch-name>"},
		},
		{
			name:    "shell redirection is not a placeholder",
			cmds:    []Command{{Command: "sort < input.txt > output.txt"}},
			force:   true,
			wantRan: []string{"sort < input.txt > output.txt"},
		},
		{
			name:    "heredoc-style redirection is not a placeholder",
			cmds:    []Command{{Command: "cat >> notes.txt < header.txt"}},
			force:   true,
			wantRan: []string{"cat >> notes.txt < header.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := setup(t, tt.ttyInput, tt.ttyErr)

			if err := Run(context.Background(), tt.text, tt.cmds, tt.force); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if strings.Join(f.ran, "|") != strings.Join(tt.wantRan, "|") {
				t.Errorf("ran %q, want %q", f.ran, tt.wantRan)
			}
			for _, want := range tt.wantOutput {
				if !strings.Contains(f.out.String(), want) {
					t.Errorf("output does not contain %q:\n%s", want, f.out.String())
				}
			}
		})
	}
}

func TestConfirmIsCancelledByContext(t *testing.T) {
	f := setup(t, "", nil)
	// A reader that never returns, like a user who never answers.
	openTTY = func() (io.ReadCloser, error) {
		r, _ := io.Pipe()
		return r, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx, "", []Command{{Command: "ls"}}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(f.ran) != 0 {
		t.Errorf("command ran after cancellation: %q", f.ran)
	}
}

func TestNormalizePlainText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
		{
			name: "markdown heading marker is removed",
			in:   "## Summary\nAll good.",
			want: "Summary\nAll good.",
		},
		{
			name: "shebang is preserved",
			in:   "The script starts with:\n#!/bin/bash",
			want: "The script starts with:\n#!/bin/bash",
		},
		{
			name: "shell comment is preserved",
			in:   "#comment without a space keeps its hash",
			want: "#comment without a space keeps its hash",
		},
		{
			name: "bold markers are stripped",
			in:   "This is **important** and __also this__.",
			want: "This is important and also this.",
		},
		{
			name: "paired code span is unwrapped",
			in:   "Run `ls -la` to list files.",
			want: "Run ls -la to list files.",
		},
		{
			name: "unmatched backtick survives",
			in:   "Command substitution uses `date` or $(date), and a lone ` is left alone.",
			want: "Command substitution uses date or $(date), and a lone ` is left alone.",
		},
		{
			name: "asterisk and plus bullets become dashes",
			in:   "* first\n+ second\n- third",
			want: "- first\n- second\n- third",
		},
		{
			name: "code fence markers are dropped but content stays",
			in:   "Example:\n```\n# a comment\nls -la\n```",
			want: "Example:\n\n# a comment\nls -la",
		},
		{
			name: "indentation is preserved",
			in:   "Steps:\n  - **one**\n  - two",
			want: "Steps:\n  - one\n  - two",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizePlainText(tt.in); got != tt.want {
				t.Errorf("\ngot  %q\nwant %q", got, tt.want)
			}
		})
	}
}
