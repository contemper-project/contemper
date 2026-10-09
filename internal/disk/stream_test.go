package disk

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLineWriterSplitsOnNewlineAndCarriageReturn(t *testing.T) {
	var got []string
	w := &lineWriter{onLine: func(l string) { got = append(got, l) }}
	// Chunk boundaries fall mid-line and between \r and \n.
	for _, chunk := range []string{"debugfs: wr", "ite a b\ndebugfs: ", "mkdir c\r", "\n    (1.00/100%)\r    (2.", "50/100%)\rtail"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	w.flush()
	want := []string{"debugfs: write a b", "debugfs: mkdir c", "    (1.00/100%)", "    (2.50/100%)", "tail"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	if all := w.all.String(); !strings.HasPrefix(all, "debugfs: write a b\n") || !strings.HasSuffix(all, "\rtail") {
		t.Errorf("full output not kept verbatim: %q", all)
	}
}

func TestParseQemuProgress(t *testing.T) {
	for _, tc := range []struct {
		line string
		want float64
		ok   bool
	}{
		{"    (0.00/100%)", 0, true},
		{"    (42.13/100%)", 42.13, true},
		{"(100.00/100%)", 100, true},
		{"qemu-img: error while writing", 0, false},
		{"", 0, false},
	} {
		got, ok := parseQemuProgress(tc.line)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseQemuProgress(%q) = %v, %v; want %v, %v", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

// fakeTool writes an executable shell script and returns its path.
func fakeTool(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunCmdStreamKeepsFullOutputAndReportsLines(t *testing.T) {
	tool := fakeTool(t, "echo 'debugfs: one'\necho 'oops: File not found' >&2\necho 'debugfs: two'\nexit 3\n")
	var lines []string
	out, err := runCmdStream(context.Background(), "", tool, func(l string) { lines = append(lines, l) })
	if err == nil {
		t.Fatal("exit status 3 was not reported as an error")
	}
	want := "debugfs: one\noops: File not found\ndebugfs: two\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if len(lines) != 3 {
		t.Errorf("lines = %q, want 3", lines)
	}
	// Existing failure detection still sees the marker in the streamed output.
	if m := findErrorMarker("one\ntwo\n", out); m != "File not found" {
		t.Errorf("findErrorMarker = %q", m)
	}
}

func TestDebugfsCommandCounterReportsPercent(t *testing.T) {
	var got []int
	script := "mkdir \"/a\"\nwrite \"f1\" \"/a/b\"\n\nsif \"/a\" uid 0\nsif \"/a\" gid 0\n"
	cb := debugfsCommandCounter(script, func(pct int) { got = append(got, pct) })
	// Output that is not a command echo (banner, allocation notices) is
	// not counted.
	cb("debugfs 1.47.0 (5-Feb-2023)")
	cb("debugfs: mkdir \"/a\"")
	cb("Allocated inode: 12")
	cb("debugfs: write \"f1\" \"/a/b\"")
	cb("debugfs: sif \"/a\" uid 0")
	cb("debugfs: sif \"/a\" gid 0")
	cb("debugfs: an echo beyond the script") // clamped at 100%
	if want := []int{25, 50, 75, 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("percentages = %v, want %v", got, want)
	}
}

func TestDebugfsCommandCounterEmptyScript(t *testing.T) {
	debugfsCommandCounter("", func(int) { t.Error("reported progress for an empty script") })("debugfs: x")
}
