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

func newTTY() (*Reporter, *syncBuf) {
	w := &syncBuf{}
	return New(w, ModeTTY, false, false), w
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

	r, w := newTTY()
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
	r, w := newTTY()
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

func TestNilStageIsSafe(t *testing.T) {
	var s *Stage
	s.SetProgress(1, 2, "B")
	s.SetProgressCount(1, 2, "files")
	s.Done("✔", "x", "y")
	s.Fail("x", "y", "z")
	s.clearLine()
	if s.stop() {
		t.Error("stop on a nil stage reported that it stopped something")
	}
	var r *Reporter
	if st := r.BeginStage("📦", "x"); st != nil {
		t.Errorf("BeginStage on a nil Reporter = %v, want nil", st)
	}
}

func TestSetProgressFormatting(t *testing.T) {
	s := &Stage{}
	for _, tc := range []struct {
		name string
		set  func()
		want string
	}{
		{"bytes with total", func() { s.SetProgress(1536, 3<<20, "downloaded") }, "1.5 KiB / 3.0 MiB downloaded"},
		{"bytes without total", func() { s.SetProgress(42, 0, "read") }, "42 read"},
		{"count with total", func() { s.SetProgressCount(3, 10, "files") }, "3 / 10 files"},
		{"count without total", func() { s.SetProgressCount(7, 0, "files") }, "7 files"},
	} {
		tc.set()
		if s.progress != tc.want {
			t.Errorf("%s: progress = %q, want %q", tc.name, s.progress, tc.want)
		}
	}
}

func TestDrawLine(t *testing.T) {
	quietTicker(t)
	r, w := newTTY()
	s := r.BeginStage("📦", "pulling\x1b[31m")
	defer s.Done("✔", "x", "")
	s.start = time.Now().Add(-65 * time.Second)

	s.draw()
	if got, want := w.String(), clr+"📦  pulling\\x1b[31m ⠋ 1m5s"; got != want {
		t.Errorf("draw = %q, want %q", got, want)
	}

	s.SetProgressCount(2, 5, "files")
	s.draw()
	want := clr + "📦  pulling\\x1b[31m ⠙ 1m5s  2 / 5 files"
	if got := strings.TrimPrefix(w.String(), clr+"📦  pulling\\x1b[31m ⠋ 1m5s"); got != want {
		t.Errorf("second draw = %q, want %q", got, want)
	}
}

func TestDrawCyclesFrames(t *testing.T) {
	quietTicker(t)
	r, w := newTTY()
	s := r.BeginStage("📦", "x")
	defer s.Done("✔", "x", "")
	n := len(spinnerFrames)
	for i := 0; i < n+2; i++ {
		s.draw()
	}
	lines := strings.Split(strings.TrimPrefix(w.String(), clr), clr)
	if len(lines) != n+2 {
		t.Fatalf("got %d draws, want %d", len(lines), n+2)
	}
	for i, l := range lines {
		want := string(spinnerFrames[i%n])
		if !strings.Contains(l, " "+want+" ") {
			t.Errorf("draw %d = %q, want frame %s", i, l, want)
		}
	}
}

func TestAnimateRedrawsAndStops(t *testing.T) {
	old := tickInterval
	tickInterval = time.Millisecond
	t.Cleanup(func() { tickInterval = old })

	r, w := newTTY()
	s := r.BeginStage("📦", "pulling")
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(w.String(), "pulling") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("animate did not redraw; output %q", w.String())
		}
		time.Sleep(time.Millisecond)
	}
	s.Done("✔", "pulled", "")
	select {
	case <-s.animDone:
	default:
		t.Fatal("animate still running after Done returned")
	}
	before := w.String()
	time.Sleep(10 * time.Millisecond)
	if after := w.String(); after != before {
		t.Errorf("output grew after Done: %q", strings.TrimPrefix(after, before))
	}
}

func TestStageDoneTTY(t *testing.T) {
	quietTicker(t)
	r, w := newTTY()
	s := r.BeginStage("📦", "pulling")
	s.draw()
	s.Done("✔", "pulled", "3 layers")
	want := clr + "📦  pulling ⠋ 0s" + clr + "✔  pulled" + strings.Repeat(" ", labelWidth-len("pulled")) + "3 layers\n"
	if got := w.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if liveOf(r) != nil {
		t.Error("live stage not cleared by Done")
	}
}

func TestStopTwiceIsSafe(t *testing.T) {
	quietTicker(t)
	r, _ := newTTY()
	s := r.BeginStage("📦", "x")
	if !s.stop() {
		t.Error("first stop = false, want true")
	}
	if s.stop() {
		t.Error("second stop = true, want false")
	}
}

func TestReporterFinishAndFailStopLiveStage(t *testing.T) {
	quietTicker(t)
	for _, tc := range []struct {
		name string
		call func(r *Reporter)
		want string
	}{
		{"Finish", func(r *Reporter) { r.Finish("all done", "") }, clr + "✨  all done\n"},
		{"Fail", func(r *Reporter) { r.Fail("pull", "boom", "") }, clr + "✖  pull: boom\n"},
	} {
		r, w := newTTY()
		s := r.BeginStage("📦", "pulling")
		tc.call(r)
		if got := w.String(); got != tc.want {
			t.Errorf("%s: output = %q, want %q", tc.name, got, tc.want)
		}
		if !s.stopped || liveOf(r) != nil {
			t.Errorf("%s: stage still live (stopped=%v)", tc.name, s.stopped)
		}
		select {
		case <-s.animDone:
		default:
			t.Errorf("%s: animate still running", tc.name)
		}
	}
}

func TestLineWhileLiveClearsSpinner(t *testing.T) {
	quietTicker(t)
	r, w := newTTY()
	s := r.BeginStage("📦", "pulling")
	s.draw()
	r.Line("ℹ", "note", "")
	want := clr + "📦  pulling ⠋ 0s" + clr + "ℹ  note\n"
	if got := w.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if liveOf(r) != s {
		t.Error("a normal line must not detach the live stage")
	}
	s.Done("✔", "pulled", "")
}

func TestVerbosePlainHeaderAndDone(t *testing.T) {
	var buf syncBuf
	r := New(&buf, ModePlain, true, false)

	r.BeginStage("📦", "pulling").Done("📦", "pulling", "")
	if got, want := buf.String(), "📦  pulling\n"; got != want {
		t.Errorf("identical Done: output = %q, want %q", got, want)
	}

	buf = syncBuf{}
	r.BeginStage("📦", "pulling").Done("📦", "pulling", "3 layers")
	want := "📦  pulling\n📦  pulling" + strings.Repeat(" ", labelWidth-len("pulling")) + "3 layers\n"
	if got := buf.String(); got != want {
		t.Errorf("Done with detail: output = %q, want %q", got, want)
	}

	buf = syncBuf{}
	r.BeginStage("📦", "pulling").Done("✔", "pulled", "")
	if got, want := buf.String(), "📦  pulling\n✔  pulled\n"; got != want {
		t.Errorf("Done with new label: output = %q, want %q", got, want)
	}

	// Non-verbose plain mode prints nothing until Done.
	var quiet syncBuf
	pr := New(&quiet, ModePlain, false, false)
	st := pr.BeginStage("📦", "pulling")
	if quiet.String() != "" {
		t.Errorf("header printed in non-verbose plain mode: %q", quiet.String())
	}
	st.Done("✔", "pulled", "")
	if got, want := quiet.String(), "✔  pulled\n"; got != want {
		t.Errorf("non-verbose Done: output = %q, want %q", got, want)
	}
}

func TestLiveBookkeeping(t *testing.T) {
	quietTicker(t)
	r, _ := newTTY()
	s1 := r.BeginStage("📦", "one")
	s2 := r.BeginStage("📦", "two")
	if liveOf(r) != s2 {
		t.Fatal("a new stage must replace the live pointer")
	}
	s1.Done("✔", "one", "")
	if liveOf(r) != s2 {
		t.Error("stopping an older stage cleared a newer stage's live pointer")
	}
	s2.Done("✔", "two", "")
	if liveOf(r) != nil {
		t.Error("live pointer not cleared once the live stage stopped")
	}
}
