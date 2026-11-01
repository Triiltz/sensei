package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPipe(t *testing.T) {
	pipe := func(data string) *os.File {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.WriteString(data)
		w.Close()
		t.Cleanup(func() { r.Close() })
		return r
	}
	open := func(path string) *os.File {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	file := filepath.Join(t.TempDir(), "in.txt")
	os.WriteFile(file, []byte("from a file\n"), 0o644)

	tests := []struct {
		name        string
		in          *os.File
		wantContent string
		wantPiped   bool
	}{
		{"pipe with content", pipe("  diff --git a b\n"), "diff --git a b", true},
		{"empty pipe", pipe(""), "", true},
		{"whitespace only", pipe("\n\n"), "", true},
		{"redirected file", open(file), "from a file", true},
		{"/dev/null is not input", open(os.DevNull), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, piped := readPipe(tt.in)
			if content != tt.wantContent || piped != tt.wantPiped {
				t.Errorf("readPipe = (%q, %v), want (%q, %v)", content, piped, tt.wantContent, tt.wantPiped)
			}
		})
	}
}
