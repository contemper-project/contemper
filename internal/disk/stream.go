package disk

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/contemper-project/contemper/internal/subprocess"
)

// lineWriter collects everything written to it and reports each
// complete line, ended by '\n' or '\r' (qemu-img redraws its progress
// readout with a bare '\r'), to onLine as it arrives. A trailing partial
// line is reported by flush.
type lineWriter struct {
	all     bytes.Buffer
	pending []byte
	onLine  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.all.Write(p)
	for _, c := range p {
		if c == '\n' || c == '\r' {
			w.flush()
			continue
		}
		w.pending = append(w.pending, c)
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.pending) == 0 {
		return
	}
	if w.onLine != nil {
		w.onLine(string(w.pending))
	}
	w.pending = w.pending[:0]
}

// runCmdStream is runCmd that also hands each line of the tool's output
// (stdout and stderr interleaved) to onLine while it runs. The returned
// output is the complete combined output, exactly as runCmd returns it.
func runCmdStream(ctx context.Context, dir, name string, onLine func(string), args ...string) (string, error) {
	cmd := subprocess.Command(ctx, name, args...)
	cmd.Dir = dir
	// One writer for both streams, so exec copies them through a single
	// goroutine and the lines keep their order.
	w := &lineWriter{onLine: onLine}
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	w.flush()
	return w.all.String(), err
}

// debugfsCommandCounter returns an onLine callback for runCmdStream that
// turns debugfs's "debugfs: <command>" echoes into a percentage of the
// script's commands, reported to stage whenever the whole percent
// changes.
func debugfsCommandCounter(script string, report func(pct int)) func(string) {
	total := 0
	for _, line := range strings.Split(script, "\n") {
		if line != "" {
			total++
		}
	}
	done, last := 0, -1
	return func(line string) {
		if total == 0 || !strings.HasPrefix(line, "debugfs: ") {
			return
		}
		done++
		pct := min(done*100/total, 100)
		if pct != last {
			last = pct
			report(pct)
		}
	}
}

// qemuProgressRe matches the readout `qemu-img -p` prints, e.g.
// "    (42.13/100%)".
var qemuProgressRe = regexp.MustCompile(`^\s*\((\d+(?:\.\d+)?)/100%\)\s*$`)

// parseQemuProgress returns the percentage in one line of `qemu-img -p`
// output, and whether the line was a progress readout at all.
func parseQemuProgress(line string) (float64, bool) {
	m := qemuProgressRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return pct, true
}
