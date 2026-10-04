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
