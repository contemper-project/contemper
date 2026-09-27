// Package progress renders the convert/deploy pipelines' progress
// output: one icon-and-two-column line per stage (name on the left,
// status/timing on the right). See docs/guide/how-it-works.md for the
// --progress flag and what each mode looks like.
//
// Two renderers share one API: a TTY renderer that updates the running
// stage in place with a spinner and elapsed timer, and a plain renderer
// that only ever appends lines (used for pipes, CI, NO_COLOR/TERM=dumb,
// or --progress=plain). Every method is nil- and no-op-safe, so --quiet
// is simply "pass a nil *Reporter".
package progress

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Mode selects how progress is rendered.
type Mode string

const (
	ModeAuto  Mode = "auto"
	ModeTTY   Mode = "tty"
	ModePlain Mode = "plain"
)

// ParseMode validates a --progress flag value.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeAuto, ModeTTY, ModePlain:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("invalid --progress value %q (want auto, tty or plain)", s)
	}
}

// resolve turns "auto" into "tty" or "plain" by inspecting w and the
// environment, the way buildx's --progress=auto does.
func resolve(mode Mode, w io.Writer) Mode {
	if mode != ModeAuto {
		return mode
	}
	if os.Getenv("TERM") == "dumb" {
		return ModePlain
	}
	if _, noColor := os.LookupEnv("NO_COLOR"); noColor {
		return ModePlain
	}
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return ModeTTY
	}
	return ModePlain
}

// Reporter renders progress lines to w. Construct with New; a nil
// *Reporter is a valid, silent no-op, which is what --quiet passes down.
type Reporter struct {
	w       io.Writer
	tty     bool
	verbose bool
	start   time.Time

	mu   sync.Mutex
	live *Stage // the currently in-flight stage, tty mode only
}

// New creates a Reporter. Pass quiet=true (or a nil *Reporter, from the
// caller's zero value) for no output at all.
func New(w io.Writer, mode Mode, verbose bool, quiet bool) *Reporter {
	if quiet || w == nil {
		return nil
	}
	return &Reporter{w: w, tty: resolve(mode, w) == ModeTTY, verbose: verbose, start: time.Now()}
}

func (r *Reporter) Verbose() bool { return r != nil && r.verbose }

// Writer exposes the underlying writer for callers that need to stream
// raw bytes (a live serial console) rather than a formatted line. It is
// always safe to call, returning io.Discard when r is nil.
func (r *Reporter) Writer() io.Writer {
	if r == nil {
		return io.Discard
	}
	return r.w
}

// twoCol lays out label and detail as a two-column line: label, padded
// to a fixed width, then detail. Long labels simply push detail right
// rather than truncating anything.
const labelWidth = 46

func twoCol(label, detail string) string {
	if detail == "" {
		return label
	}
	if len(label) < labelWidth {
		label += strings.Repeat(" ", labelWidth-len(label))
	} else {
		label += "  "
	}
	return label + detail
}

// Line prints a single icon + two-column status line, e.g.
// "📦  ghcr.io/example/app:v3               linux/arm64".
func (r *Reporter) Line(icon, label, detail string) {
	r.println(icon + "  " + twoCol(label, detail))
}

// Sub prints an indented, first-level sub-result under the current
// stage, e.g. "    ✔ rootfs                                 3 layers".
func (r *Reporter) Sub(mark, label, detail string) {
	r.println("    " + mark + " " + twoCol(label, detail))
}

// SubChild prints a second-level sub-result, indented further and
// introduced with "└", e.g.
// "      └ ghcr.io/contemper-project/incus-openrc:v5 linux/arm64".
func (r *Reporter) SubChild(format string, args ...any) {
	r.println("      └ " + fmt.Sprintf(format, args...))
}

// Warn prints a "⚠️" line.
func (r *Reporter) Warn(label, detail string) {
	r.println("⚠️   " + twoCol(label, detail))
}

// Blank prints a blank separator line, matching the doc examples'
// spacing between stages.
func (r *Reporter) Blank() {
	r.println("")
}

// VerboseCmd prints a dimmed host-tool invocation line, only when
// --verbose was requested.
func (r *Reporter) VerboseCmd(name string, args []string) {
	if r == nil || !r.verbose {
		return
	}
	r.println(dim("  $ " + strings.Join(append([]string{name}, args...), " ")))
}

// Fail prints the "✖" failure line: a stage name, a plain-language
// reason, and (optionally) what to do about it. It is a no-op on a nil
// Reporter (--quiet): the caller's returned error is still what drives
// the process exit code and main's plain "contemper: <err>" line, so
// nothing is lost, only the extra narration is suppressed along with
// the rest of the progress output.
func (r *Reporter) Fail(stage, reason, hint string) {
	if r == nil {
		return
	}
	r.stopLive(nil)
	r.println("✖  " + stage + ": " + reason)
	if hint != "" {
		r.println("   " + hint)
	}
}

// ToolFailureOutput prints a failed host tool's captured output
// verbatim, prefixed for readability.
func (r *Reporter) ToolFailureOutput(output string) {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return
	}
	for _, line := range strings.Split(output, "\n") {
		r.println("   | " + line)
	}
}

// Finish prints the final "✨" summary line with the total elapsed time
// folded into detail by the caller (matching the doc's "412 MiB   8.2s"
// shape) - Finish itself just prints label/detail, elapsed is available
// via Elapsed for callers that want to format it themselves.
func (r *Reporter) Finish(label, detail string) {
	r.stopLive(nil)
	r.println("✨  " + twoCol(label, detail))
}

// Elapsed returns the time since this Reporter was created.
func (r *Reporter) Elapsed() time.Duration {
	if r == nil {
		return 0
	}
	return time.Since(r.start)
}

func (r *Reporter) println(s string) {
	r.stopLiveForLine()
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintln(r.w, s)
}

// stopLiveForLine clears any in-progress live stage line before printing
// a normal line over it, without marking the stage done.
func (r *Reporter) stopLiveForLine() {
	if r == nil {
		return
	}
	r.mu.Lock()
	live := r.live
	r.mu.Unlock()
	if live != nil {
		live.clearLine()
	}
}

// HumanBytes formats n bytes as a short, human-readable size using
// binary (1024-based) units, e.g. "9.8 MiB".
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), units[exp])
}

// dim wraps s in the ANSI "faint" SGR code, used only for --verbose
// tool-invocation lines. Plain mode still uses this (color is cheap and
// most "plain" consumers are logs, which tools strip or ignore ANSI
// in) - NO_COLOR is handled by disabling it at the call site if ever
// needed; for now --verbose output is developer-facing only.
func dim(s string) string {
	return "\x1b[2m" + s + "\x1b[0m"
}
