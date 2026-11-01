package cmd

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	built := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	tests := []struct {
		name, ldflags string
		info          *debug.BuildInfo
		ok            bool
		want          string
	}{
		{"ldflags wins", "v1.1.0", built("v1.0.0"), true, "v1.1.0"},
		{"go install @version", "dev", built("v1.1.0"), true, "v1.1.0"},
		{"local build", "dev", built("(devel)"), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.ldflags, tt.info, tt.ok); got != tt.want {
				t.Errorf("resolveVersion = %q, want %q", got, tt.want)
			}
		})
	}
}

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
