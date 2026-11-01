package executor

import "testing"

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		wantText    string
		wantCommand string
	}{
		{
			name:        "conceptual answer without a command",
			response:    "A pipe connects the stdout of one process to the stdin of another.",
			wantText:    "A pipe connects the stdout of one process to the stdin of another.",
			wantCommand: "",
		},
		{
			name:        "single command is extracted",
			response:    "Extracts a tar.gz archive.\nCOMMAND=tar -xzf archive.tar.gz",
			wantText:    "Extracts a tar.gz archive.",
			wantCommand: "tar -xzf archive.tar.gz",
		},
		{
			name:        "multiple commands are shown as text, never executed",
			response:    "Do both:\nCOMMAND=git add .\nCOMMAND=git commit -m wip",
			wantText:    "Do both:\n  $ git add .\n  $ git commit -m wip",
			wantCommand: "",
		},
		{
			name:        "placeholder blocks extraction",
			response:    "Creates a branch.\nCOMMAND=git checkout -b <branch-name>",
			wantText:    "Creates a branch.\n  $ git checkout -b <branch-name>",
			wantCommand: "",
		},
		{
			name:        "shell redirection is not a placeholder",
			response:    "Sorts the file.\nCOMMAND=sort < input.txt > output.txt",
			wantText:    "Sorts the file.",
			wantCommand: "sort < input.txt > output.txt",
		},
		{
			name:        "heredoc-style redirection is not a placeholder",
			response:    "Appends a line.\nCOMMAND=cat >> notes.txt < header.txt",
			wantText:    "Appends a line.",
			wantCommand: "cat >> notes.txt < header.txt",
		},
		{
			name:        "indented command line is still recognised",
			response:    "Lists files.\n   COMMAND=ls -la",
			wantText:    "Lists files.",
			wantCommand: "ls -la",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, command := ParseCommand(tt.response)
			if text != tt.wantText {
				t.Errorf("text:\ngot  %q\nwant %q", text, tt.wantText)
			}
			if command != tt.wantCommand {
				t.Errorf("command:\ngot  %q\nwant %q", command, tt.wantCommand)
			}
		})
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
			if got := normalizePlainText(tt.in); got != tt.want {
				t.Errorf("\ngot  %q\nwant %q", got, tt.want)
			}
		})
	}
}
