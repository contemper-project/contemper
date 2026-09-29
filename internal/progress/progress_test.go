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
