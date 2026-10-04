package progress

import (
	"fmt"
	"sync"
	"time"
)

var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// Stage is a long-running pipeline stage (layer pull/merge, ext4
// population, ...): in TTY mode it animates a spinner and elapsed timer
// in place, optionally with a live "done / total" progress readout,
// until Done or Fail replaces it with a single final line. In plain
// mode nothing is printed until Done/Fail, which then prints exactly
// one line - so a redirected log still gets one line per stage, in
// order, with no cursor-movement noise.
type Stage struct {
	r     *Reporter
	icon  string
	label string

	mu            sync.Mutex
	progress      string
	start         time.Time
	frame         int
	stopCh        chan struct{}
	stopped       bool
	headerPrinted bool
}

// BeginStage starts a new stage. Safe to call on a nil Reporter.
func (r *Reporter) BeginStage(icon, label string) *Stage {
	if r == nil {
		return nil
	}
	s := &Stage{r: r, icon: icon, label: SanitizeLine(label), start: time.Now()}
	switch {
	case r.tty:
		s.stopCh = make(chan struct{})
		r.mu.Lock()
		r.live = s
		r.mu.Unlock()
		go s.animate()
	case r.verbose:
		// Plain mode normally defers all output for a stage to Done, so
		// a redirected log gets exactly one line per stage. In verbose
		// mode, print the header up front too, so the tool-invocation
		// lines that follow have a heading to sit under; Done then skips
		// re-printing an identical, detail-less line (see Done).
		r.println(icon + "  " + s.label)
		s.headerPrinted = true
	}
	return s
}

// SetProgress updates the stage's live "done/total" readout (TTY mode
// only; a no-op otherwise, so callers don't need to check the mode).
func (s *Stage) SetProgress(done, total int64, unit string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if total > 0 {
		s.progress = fmt.Sprintf("%s / %s %s", HumanBytes(done), HumanBytes(total), unit)
	} else {
		s.progress = fmt.Sprintf("%d %s", done, unit)
	}
}

// SetProgressCount is SetProgress for plain counts (files, items) rather
// than byte sizes.
func (s *Stage) SetProgressCount(done, total int64, unit string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if total > 0 {
		s.progress = fmt.Sprintf("%d / %d %s", done, total, unit)
	} else {
		s.progress = fmt.Sprintf("%d %s", done, unit)
	}
}

func (s *Stage) animate() {
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.draw()
		}
	}
}

func (s *Stage) draw() {
	s.mu.Lock()
	frame := spinnerFrames[s.frame%len(spinnerFrames)]
	s.frame++
	elapsed := time.Since(s.start).Round(time.Second)
	progress := s.progress
	s.mu.Unlock()

	line := fmt.Sprintf("%c  %s %c %s", []rune(s.icon)[0], s.label, frame, elapsed)
	if progress != "" {
		line += "  " + progress
	}

	s.r.mu.Lock()
	_, _ = fmt.Fprint(s.r.w, "\r\x1b[K"+line)
	s.r.mu.Unlock()
}

// clearLine erases the current in-place line without finalizing the
// stage, so a normal println can be interleaved (this should not
// normally happen - stages are meant to run one at a time - but it
// keeps output sane if it does).
func (s *Stage) clearLine() {
	if s == nil || s.r == nil || !s.r.tty {
		return
	}
	s.r.mu.Lock()
	_, _ = fmt.Fprint(s.r.w, "\r\x1b[K")
	s.r.mu.Unlock()
}

func (s *Stage) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	already := s.stopped
	s.stopped = true
	s.mu.Unlock()
	if already {
		return
	}
	if s.stopCh != nil {
		close(s.stopCh)
	}
	if s.r != nil {
		s.r.mu.Lock()
		if s.r.live == s {
			s.r.live = nil
		}
		s.r.mu.Unlock()
	}
}

// Done stops the stage and prints its single final line: the doc-style
// "icon  label  detail" - in TTY mode replacing the live spinner line in
// place, in plain mode as the stage's only output line.
func (s *Stage) Done(icon, label, detail string) {
	if s == nil {
		return
	}
	s.stop()
	if s.r.tty {
		s.r.mu.Lock()
		_, _ = fmt.Fprint(s.r.w, "\r\x1b[K")
		s.r.mu.Unlock()
	}
	// If the header was already printed up front (verbose, plain mode)
	// and Done has nothing new to add, don't print an identical second
	// copy of the same line.
	if s.headerPrinted && detail == "" && icon+"  "+SanitizeLine(label) == s.icon+"  "+s.label {
		return
	}
	s.r.println(icon + "  " + twoCol(label, detail))
}

// Fail stops the stage and delegates to Reporter.Fail.
func (s *Stage) Fail(stage, reason, hint string) {
	if s == nil {
		return
	}
	s.stop()
	s.r.Fail(stage, reason, hint)
}

// stopLive is called by Reporter.Fail/Finish to make sure no stage is
// left animating when the program is about to print a final line or
// exit.
func (r *Reporter) stopLive(_ *Stage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	live := r.live
	r.live = nil
	r.mu.Unlock()
	if live != nil {
		live.stop()
		if r.tty {
			r.mu.Lock()
			_, _ = fmt.Fprint(r.w, "\r\x1b[K")
			r.mu.Unlock()
		}
	}
}
