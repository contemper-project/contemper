package progress

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is a mutex-guarded writer, so tests can read what the animate
// goroutine wrote without a data race.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func newTTY(verbose bool) (*Reporter, *syncBuf) {
	w := &syncBuf{}
	return New(w, ModeTTY, verbose, false), w
}

// TestStopWaitsForInFlightDraw holds a draw between reading the stage
// state and writing its line, calls Done, and checks that Done does not
// return (and so no final line is printed) until that draw has finished:
// otherwise the spinner line would land after the final line and leave a
// stale, unterminated line behind.
func TestStopWaitsForInFlightDraw(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	drawGate = func() {
		once.Do(func() { close(reached) })
		<-release
	}
	oldTick := tickInterval
	tickInterval = time.Millisecond
	t.Cleanup(func() { drawGate, tickInterval = nil, oldTick })

	r, w := newTTY(false)
	s := r.BeginStage("📦", "pulling")
	<-reached

	finished := make(chan struct{})
	go func() {
		s.Done("✔", "pulled", "")
		close(finished)
	}()
	select {
	case <-finished:
		// Done returned while a draw is still pending: release it and
		// show what it would have written.
		close(release)
		time.Sleep(50 * time.Millisecond)
		t.Fatalf("Done returned with a draw in flight; output %q", w.String())
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-finished
	time.Sleep(20 * time.Millisecond)

	got := w.String()
	if !strings.HasSuffix(got, "\r\x1b[K✔  pulled\n") {
		t.Errorf("output does not end with the final line: %q", got)
	}
}

// TestDrawIconShapes covers an empty icon (which used to panic), a
// multi-rune icon, and one with a control character.
func TestDrawIconShapes(t *testing.T) {
	for _, tc := range []struct{ icon, want string }{
		{"", "\r\x1b[K  lbl ⠋ 0s"},
		{"⚠️", "\r\x1b[K⚠️  lbl ⠋ 0s"},
		{"\x1b", "\r\x1b[K\\x1b  lbl ⠋ 0s"},
	} {
		w := &syncBuf{}
		r := New(w, ModeTTY, false, false)
		s := &Stage{r: r, icon: tc.icon, label: "lbl", start: time.Now()}
		s.draw()
		if got := w.String(); got != tc.want {
			t.Errorf("icon %q: draw wrote %q, want %q", tc.icon, got, tc.want)
		}
	}
}

// TestDoneTwicePrintsOnce: a second Done (or a Done after Fail) on a
// stopped stage must not print the final line again.
func TestDoneTwicePrintsOnce(t *testing.T) {
	r, w := newTTY(false)
	s := r.BeginStage("📦", "pulling")
	s.Done("✔", "pulled", "")
	s.Done("✔", "pulled", "")
	s.Fail("pull", "late", "")
	if got, want := strings.Count(w.String(), "pulled\n"), 1; got != want {
		t.Errorf("final line printed %d times, want %d; output %q", got, want, w.String())
	}
	if strings.Contains(w.String(), "late") {
		t.Errorf("Fail after Done printed: %q", w.String())
	}

	var plain syncBuf
	pr := New(&plain, ModePlain, false, false)
	ps := pr.BeginStage("📦", "pulling")
	ps.Done("✔", "pulled", "")
	ps.Done("✔", "pulled", "")
	if got, want := plain.String(), "✔  pulled\n"; got != want {
		t.Errorf("plain output = %q, want %q", got, want)
	}
}

const clr = "\r\x1b[K"

// quietTicker stops the spinner from drawing on its own for the rest of
// the test, so output only holds what the test itself triggers.
func quietTicker(t *testing.T) {
	t.Helper()
	old := tickInterval
	tickInterval = time.Hour
	t.Cleanup(func() { tickInterval = old })
}

func liveOf(r *Reporter) *Stage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live
}

func TestStageFailTTY(t *testing.T) {
	quietTicker(t)
	r, w := newTTY(false)
	s := r.BeginStage("📦", "pulling")
	s.draw()
	s.Fail("pull", "boom", "try again")
	want := clr + "📦  pulling ⠋ 0s" + clr + "✖  pull: boom\n   try again\n"
	if got := w.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if liveOf(r) != nil {
		t.Error("live stage not cleared by Fail")
	}
}
