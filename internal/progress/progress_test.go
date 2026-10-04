package progress_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/progress"
)

// newPlain builds a Reporter forced into plain mode by handing it a
// plain *bytes.Buffer (never a *os.File, so the TTY check in resolve()
// can never trigger) - this is what a redirected/piped run looks like.
func newPlain(verbose bool) (*progress.Reporter, *bytes.Buffer) {
	var buf bytes.Buffer
	return progress.New(&buf, progress.ModeAuto, verbose, false), &buf
}

func TestPlainLineAndTwoColumn(t *testing.T) {
	r, buf := newPlain(false)
	r.Line("📦", "ghcr.io/example/my-appliance:v3", "linux/arm64")
	want := "📦  ghcr.io/example/my-appliance:v3               linux/arm64\n"
	if got := buf.String(); got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

func TestPlainLineNoDetail(t *testing.T) {
	r, buf := newPlain(false)
	r.Line("✅", "contemper-ready", "")
	if got, want := buf.String(), "✅  contemper-ready\n"; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

func TestPlainSubAndSubChild(t *testing.T) {
	r, buf := newPlain(false)
	r.Sub("✔", "rootfs", "3 layers")
	r.SubChild("ghcr.io/contemper-project/incus-openrc:v5 %s", "linux/arm64")
	got := buf.String()
	wantLines := []string{
		"    ✔ rootfs                                        3 layers",
		"      └ ghcr.io/contemper-project/incus-openrc:v5 linux/arm64",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing line %q", got, want)
		}
	}
}

func TestPlainWarn(t *testing.T) {
	r, buf := newPlain(false)
	r.Warn("local source", "bundle is not reproducible")
	if got := buf.String(); !strings.HasPrefix(got, "⚠️   local source") || !strings.Contains(got, "bundle is not reproducible") {
		t.Errorf("Warn() = %q", got)
	}
}

func TestPlainFail(t *testing.T) {
	r, buf := newPlain(false)
	r.Fail("assemble", "qemu-img not found on PATH", "install it with: apt install qemu")
	got := buf.String()
	if !strings.Contains(got, "✖  assemble: qemu-img not found on PATH") {
		t.Errorf("Fail() missing header line: %q", got)
	}
	if !strings.Contains(got, "install it with: apt install qemu") {
		t.Errorf("Fail() missing hint line: %q", got)
	}
}

func TestFailSilentOnceContextCanceled(t *testing.T) {
	r, buf := newPlain(false)
	ctx, cancel := context.WithCancel(context.Background())
	r.SetContext(ctx)
	r.Fail("merge", "reason", "hint")
	if !strings.Contains(buf.String(), "✖  merge: reason") {
		t.Fatalf("Fail() before cancellation printed %q, want the failure line", buf.String())
	}
	buf.Reset()
	cancel()
	r.BeginStage("💿", "assembling").Fail("assemble", "context canceled", "")
	if buf.Len() != 0 {
		t.Errorf("Fail() after cancellation printed %q, want nothing", buf.String())
	}
}

func TestPlainStageDoneIsOneLine(t *testing.T) {
	r, buf := newPlain(false)
	s := r.BeginStage("🧬", "merging layers")
	// In plain mode, BeginStage prints nothing - only Done does, so a
	// redirected log gets exactly one line for the whole stage.
	if buf.Len() != 0 {
		t.Fatalf("BeginStage should not print in plain mode, got %q", buf.String())
	}
	s.Done("🧬", "merged 5 + 3 layers", "")
	if got, want := buf.String(), "🧬  merged 5 + 3 layers\n"; got != want {
		t.Errorf("Done() = %q, want %q", got, want)
	}
}

func TestPlainVerboseCmd(t *testing.T) {
	r, buf := newPlain(true)
	r.VerboseCmd("mkfs.ext4", []string{"-F", "-L", "contemper-root", "root.img"})
	if !strings.Contains(buf.String(), "mkfs.ext4 -F -L contemper-root root.img") {
		t.Errorf("VerboseCmd() = %q", buf.String())
	}

	r2, buf2 := newPlain(false)
	r2.VerboseCmd("mkfs.ext4", []string{"-F"})
	if buf2.Len() != 0 {
		t.Errorf("VerboseCmd() without --verbose should print nothing, got %q", buf2.String())
	}
}

func TestNilReporterIsSilentAndSafe(t *testing.T) {
	var r *progress.Reporter
	r.Line("📦", "x", "y")
	r.Sub("✔", "a", "b")
	r.SubChild("x")
	r.Warn("a", "b")
	r.SetContext(context.Background())
	r.Fail("stage", "reason", "hint") // must not print anywhere
	r.Finish("done", "1s")
	r.VerboseCmd("x", nil)
	s := r.BeginStage("x", "y")
	s.SetProgress(1, 2, "MiB")
	s.Done("x", "y", "z")
	if r.Writer() == nil {
		t.Errorf("Writer() on nil Reporter should return io.Discard, not nil")
	}
	if r.Elapsed() != 0 {
		t.Errorf("Elapsed() on nil Reporter should be 0")
	}
}

func TestQuietProducesNoReporter(t *testing.T) {
	var buf bytes.Buffer
	r := progress.New(&buf, progress.ModeAuto, false, true)
	if r != nil {
		t.Fatalf("New(..., quiet=true) should return nil")
	}
}

func TestHumanBytes(t *testing.T) {
	gib := int64(1024 * 1024 * 1024)
	cases := []struct {
		n    int64
		want string
	}{
		{500, "500 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{10 * 1024 * 1024, "10.0 MiB"},
		{gib + gib/8, "1.1 GiB"},
	}
	for _, c := range cases {
		if got := progress.HumanBytes(c.n); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestParseMode(t *testing.T) {
	for _, ok := range []string{"auto", "tty", "plain"} {
		if _, err := progress.ParseMode(ok); err != nil {
			t.Errorf("ParseMode(%q): %v", ok, err)
		}
	}
	if _, err := progress.ParseMode("bogus"); err == nil {
		t.Errorf("ParseMode(bogus): expected an error")
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ghcr.io/example/app:v3", "ghcr.io/example/app:v3"},
		{"héllo wörld 📦 ⚠️ ✔", "héllo wörld 📦 ⚠️ ✔"},
		{"two\nlines\tand tab", "two\nlines\tand tab"},
		{"\x1b[2J\x1b]0;title\x07", `\x1b[2J\x1b]0;title\a`},
		{"back\rspace\x08", `back\rspace\b`},
		{"c1\u009bcontrol", `c1\u009bcontrol`},
		{"bidi\u202eflip", `bidi\u202eflip`},
		{"bad\xffbyte", `bad\xffbyte`},
	}
	for _, c := range cases {
		if got := progress.Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReporterEscapesImageStrings(t *testing.T) {
	r, buf := newPlain(true)
	r.Line("📦", "evil\x1b[2Jname", "d\rx")
	r.Sub("✔", "/etc/\x1b]0;x\x07", "")
	r.Warn("link \x1b[31m", "")
	r.Fail("stage", "bad \x1b[0m path", "hint\x07")
	r.VerboseCmd("tool", []string{"/a\x1b[2Jb"})
	st := r.BeginStage("🧬", "label\x1b[H")
	st.Done("🧬", "label\x1b[H", "")
	// Only the verbose command line's own dim wrapper may remain.
	out := strings.NewReplacer("\x1b[2m", "", "\x1b[0m", "").Replace(buf.String())
	for _, c := range []string{"\x1b", "\r", "\x07"} {
		if strings.Contains(out, c) {
			t.Errorf("output contains raw %q:\n%q", c, out)
		}
	}
	if !strings.Contains(out, `evil\x1b[2Jname`) {
		t.Errorf("escaped name missing from %q", out)
	}
}

func TestReporterEscapesNewlinesInFields(t *testing.T) {
	r, buf := newPlain(true)
	r.Line("📦", "/etc/a\n✔ forged line", "detail\r\nx")
	r.Sub("✔", "name\n✖  forged failure", "d\n")
	r.Warn("w\nforged", "")
	r.SubChild("link %s", "a\n      └ forged child")
	r.Fail("st\nage", "first line\nsecond line", "hint\nmore")
	r.ToolFailureOutput("tool says\nsecond\r\x1b[2J")
	r.Finish("done\nforged", "")
	r.VerboseCmd("tool", []string{"a\nb"})
	st := r.BeginStage("🧬", "stage\nforged")
	st.Done("🧬", "stage\nforged", "d\nx")
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for _, l := range lines {
		for _, forged := range []string{"✔ forged", "✖  forged", "forged child"} {
			if strings.HasPrefix(strings.TrimSpace(l), forged) {
				t.Errorf("a field started its own line: %q", l)
			}
		}
	}
	// A multi-line failure reason, as a tool's output makes one, stays multi-line.
	if !strings.Contains(buf.String(), "✖  st\\nage: first line\nsecond line\n") {
		t.Errorf("multi-line reason not kept: %q", buf.String())
	}
}

func TestSanitizeLineEscapesNewline(t *testing.T) {
	if got := progress.SanitizeLine("a\nb\tc\r"); got != `a\nb`+"\t"+`c\r` {
		t.Errorf("SanitizeLine = %q", got)
	}
	if got := progress.Sanitize("a\nb"); got != "a\nb" {
		t.Errorf("Sanitize = %q", got)
	}
}
