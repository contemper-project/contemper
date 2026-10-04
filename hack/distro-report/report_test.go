package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/contemper-project/contemper/test/distros"
)

const repo = "o/r"

// fakeGitHub serves the few endpoints the report uses from memory and
// records the write requests it receives.
type fakeGitHub struct {
	mu       sync.Mutex
	srv      *httptest.Server
	issues   []map[string]any
	comments map[int][]string
	writes   []string
	pageSize int
	// failComments makes adding a comment to these issues fail.
	failComments map[int]bool
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{comments: map[int][]string{}, pageSize: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) addIssue(number int, state, body string, pr bool) {
	i := map[string]any{"number": number, "state": state, "body": body}
	if pr {
		i["pull_request"] = map[string]any{}
	}
	f.issues = append(f.issues, i)
}

func (f *fakeGitHub) page(w http.ResponseWriter, r *http.Request, items []any) {
	page := 1
	_, _ = fmt.Sscan(r.URL.Query().Get("page"), &page)
	lo := (page - 1) * f.pageSize
	hi := lo + f.pageSize
	if lo > len(items) {
		lo = len(items)
	}
	if hi < len(items) {
		next := *r.URL
		q := next.Query()
		q.Set("page", fmt.Sprint(page+1))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, f.srv.URL, next.RequestURI()))
	} else {
		hi = len(items)
	}
	_ = json.NewEncoder(w).Encode(items[lo:hi])
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/"+repo)
	var n int
	switch {
	case r.Method == "GET" && path == "/issues":
		if r.URL.Query().Get("labels") != failureLabel || r.URL.Query().Get("state") != "all" {
			http.Error(w, "bad query", 400)
			return
		}
		items := make([]any, len(f.issues))
		for i, v := range f.issues {
			items[i] = v
		}
		f.page(w, r, items)
	case r.Method == "GET" && sscan(path, "/issues/%d/comments", &n):
		var items []any
		for _, c := range f.comments[n] {
			items = append(items, map[string]string{"body": c})
		}
		f.page(w, r, items)
	case r.Method == "POST" && path == "/issues":
		var in struct {
			Title, Body string
			Labels      []string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.writes = append(f.writes, fmt.Sprintf("create %q labels=%v\n%s", in.Title, in.Labels, in.Body))
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 99})
	case r.Method == "POST" && sscan(path, "/issues/%d/comments", &n):
		if f.failComments[n] {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var in struct{ Body string }
		_ = json.Unmarshal(b, &in)
		f.writes = append(f.writes, fmt.Sprintf("comment #%d\n%s", n, in.Body))
		f.comments[n] = append(f.comments[n], in.Body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte("{}"))
	case r.Method == "PATCH" && sscan(path, "/issues/%d", &n):
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.writes = append(f.writes, fmt.Sprintf("patch #%d %v", n, in))
		_, _ = w.Write([]byte("{}"))
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), 404)
	}
}

func sscan(path, format string, n *int) bool {
	var rest string
	c, _ := fmt.Sscanf(path+"|", format+"%s", n, &rest)
	return c == 2 && rest == "|"
}

func (f *fakeGitHub) reporter(dry bool, out *strings.Builder) *reporter {
	return &reporter{
		gh:      newClient(f.srv.URL, repo, "tok"),
		entries: []distros.Entry{{ID: "fedora-44", Base: "fedora:44", Family: "fedora", Arch: []string{"amd64", "arm64"}}},
		runURL:  "https://x/runs/7",
		event:   "schedule",
		dryRun:  dry,
		log:     out,
	}
}

func res(outcome string) distros.Result {
	return distros.Result{ID: "fedora-44", Arch: "amd64", Outcome: outcome, Stage: "boot",
		BaseDigest: "sha256:abc", RunURL: "https://x/runs/7"}
}

const m = "<!-- contemper-distro-test: fedora-44/amd64 -->"

func TestFailureCreatesIssue(t *testing.T) {
	f := newFake(t)
	var log strings.Builder
	if err := f.reporter(false, &log).report([]distros.Result{res("fail")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("writes = %q", f.writes)
	}
	w := f.writes[0]
	for _, want := range []string{
		`create "Distribution test failing: fedora-44 (amd64)" labels=[distro-test-failure area: target-distros]`,
		m, "fedora:44", "https://x/runs/7", "boot", "sha256:abc",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("create missing %q in\n%s", want, w)
		}
	}
	if strings.Contains(w, "Previously") {
		t.Errorf("no previous issue expected:\n%s", w)
	}
	// arm64 has no result: noted, not acted on.
	if !strings.Contains(log.String(), "fedora-44/arm64: no result") {
		t.Errorf("log = %s", log.String())
	}
}

func TestFailureLinksPreviousClosedIssue(t *testing.T) {
	f := newFake(t)
	f.addIssue(3, "closed", m, false)
	f.addIssue(8, "closed", m, false)
	f.addIssue(5, "closed", m, false)
	if err := f.reporter(false, &strings.Builder{}).report([]distros.Result{res("fail")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || !strings.Contains(f.writes[0], "Previously reported in #8") {
		t.Fatalf("writes = %q", f.writes)
	}
}

func TestFailureCommentsOnOpenIssue(t *testing.T) {
	f := newFake(t)
	f.addIssue(4, "open", m+"\nfirst failing run: https://x/runs/1", false)
	f.comments[4] = []string{"Still failing in https://x/runs/70."} // longer URL: not a match
	r := f.reporter(false, &strings.Builder{})
	if err := r.report([]distros.Result{res("fail")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "comment #4") ||
		!strings.Contains(f.writes[0], "https://x/runs/7") || !strings.Contains(f.writes[0], "sha256:abc") {
		t.Fatalf("writes = %q", f.writes)
	}
	// Re-running for the same run adds nothing.
	if err := r.report([]distros.Result{res("fail")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("re-run wrote again: %q", f.writes)
	}
}

func TestFailureOnOpenIssueFromThisRunIsIdempotent(t *testing.T) {
	f := newFake(t)
	f.addIssue(4, "open", m+"\nFirst failing run: https://x/runs/7 (schedule)", false)
	if err := f.reporter(false, &strings.Builder{}).report([]distros.Result{res("fail")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("writes = %q", f.writes)
	}
}

func TestPassClosesOpenIssue(t *testing.T) {
	f := newFake(t)
	f.addIssue(4, "open", m, false)
	r := f.reporter(false, &strings.Builder{})
	if err := r.report([]distros.Result{res("pass")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 2 || f.writes[0] != "comment #4\nPassing again in https://x/runs/7." ||
		!strings.HasPrefix(f.writes[1], "patch #4") || !strings.Contains(f.writes[1], "closed") {
		t.Fatalf("writes = %q", f.writes)
	}
}

func TestPassRerunDoesNotDoubleComment(t *testing.T) {
	f := newFake(t)
	f.addIssue(4, "open", m, false)
	f.comments[4] = []string{"Passing again in https://x/runs/7."}
	if err := f.reporter(false, &strings.Builder{}).report([]distros.Result{res("pass")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "patch #4") {
		t.Fatalf("writes = %q", f.writes)
	}
}

func TestPassWithoutOpenIssueDoesNothing(t *testing.T) {
	f := newFake(t)
	f.addIssue(4, "closed", m, false)
	if err := f.reporter(false, &strings.Builder{}).report([]distros.Result{res("pass")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("writes = %q", f.writes)
	}
}

func TestMissingResultTouchesNothing(t *testing.T) {
	f := newFake(t)
	f.addIssue(4, "open", m, false)
	var log strings.Builder
	if err := f.reporter(false, &log).report(nil); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 || !strings.Contains(log.String(), "fedora-44/amd64: no result") {
		t.Fatalf("writes = %q, log = %s", f.writes, log.String())
	}
}

func TestIgnoresIssuesWithoutMarkerAndPullRequests(t *testing.T) {
	f := newFake(t)
	f.addIssue(1, "open", "a human wrote this about fedora-44/amd64", false)
	f.addIssue(2, "open", m, true) // a pull request carrying the marker
	f.addIssue(3, "open", "<!-- contemper-distro-test: fedora-44/arm64 -->", false)
	if err := f.reporter(false, &strings.Builder{}).report([]distros.Result{res("pass"), res("fail")}); err != nil {
		t.Fatal(err)
	}
	// Pass: no open amd64 issue to close. Fail: creates a new one rather
	// than touching #1, #2 or the arm64 issue #3.
	if len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "create ") {
		t.Fatalf("writes = %q", f.writes)
	}
}

func TestPagination(t *testing.T) {
	f := newFake(t)
	f.pageSize = 2
	for i := 1; i <= 5; i++ {
		f.addIssue(i, "open", fmt.Sprintf("<!-- contemper-distro-test: other-%d/amd64 -->", i), false)
	}
	f.addIssue(6, "open", m, false) // on the last page
	f.comments[6] = []string{"a", "b", "Still failing in https://x/runs/7."}
	if err := f.reporter(false, &strings.Builder{}).report([]distros.Result{res("fail")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("writes = %q (issue on page 3 or comment on page 2 missed)", f.writes)
	}
}

func TestPaginationLinkLeavingAPIIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://evil.example/x>; rel="next"`)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	if _, err := newClient(srv.URL, repo, "tok").listIssues(failureLabel); err == nil {
		t.Fatal("want error")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFake(t)
	f.addIssue(4, "open", "<!-- contemper-distro-test: fedora-44/arm64 -->", false)
	f.addIssue(5, "open", m, false)
	var log strings.Builder
	rs := []distros.Result{res("pass")}
	if err := f.reporter(true, &log).report(rs); err != nil {
		t.Fatal(err)
	}
	f.issues = nil
	if err := f.reporter(true, &log).report([]distros.Result{res("fail")}); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("writes = %q", f.writes)
	}
	if !strings.Contains(log.String(), "close #5") || !strings.Contains(log.String(), "create issue") {
		t.Errorf("log = %s", log.String())
	}
}

func TestAPIErrorIsReported(t *testing.T) {
	f := newFake(t)
	r := f.reporter(false, &strings.Builder{})
	r.gh.token = "wrong"
	if err := r.report(nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadResults(t *testing.T) {
	dir := t.TempDir()
	if rs, err := readResults(filepath.Join(dir, "absent")); err != nil || len(rs) != 0 {
		t.Fatalf("absent dir: %v %v", rs, err)
	}
	sub := filepath.Join(dir, "distro-result-a-amd64")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(sub, "result.json"), []byte(`{"id":"a","arch":"amd64","outcome":"fail","stage":"build"}`))
	write(t, filepath.Join(dir, "b.json"), []byte(`{"id":"b","arch":"arm64","outcome":"pass"}`))
	write(t, filepath.Join(dir, "note.txt"), []byte("ignored"))
	rs, err := readResults(dir)
	if err != nil || len(rs) != 2 || rs[0].ID != "a" || rs[1].ID != "b" {
		t.Fatalf("rs = %v, err = %v", rs, err)
	}
	write(t, filepath.Join(dir, "bad.json"), []byte(`{"id":"c"}`))
	// A bad file is reported but doesn't hide the valid ones.
	write(t, filepath.Join(dir, "garbage.json"), []byte(`not json`))
	rs, err = readResults(dir)
	if err == nil || !strings.Contains(err.Error(), "bad.json") || !strings.Contains(err.Error(), "garbage.json") {
		t.Fatalf("err = %v", err)
	}
	if len(rs) != 2 || rs[0].ID != "a" || rs[1].ID != "b" {
		t.Fatalf("rs = %v", rs)
	}
}

func TestContainsURL(t *testing.T) {
	for _, tt := range []struct {
		text string
		want bool
	}{
		{"see https://x/runs/7.", true},
		{"https://x/runs/7", true},
		{"https://x/runs/70", false},
		{"https://x/runs/70 and https://x/runs/7)", true},
		{"", false},
	} {
		if got := containsURL(tt.text, "https://x/runs/7"); got != tt.want {
			t.Errorf("containsURL(%q) = %v", tt.text, got)
		}
	}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestErrorOnOneEntryDoesNotStopTheOthers(t *testing.T) {
	f := newFake(t)
	f.failComments = map[int]bool{4: true}
	f.addIssue(4, "open", m, false)
	f.addIssue(5, "open", "<!-- contemper-distro-test: alpine-3.24/amd64 -->", false)
	other := res("fail")
	other.ID = "alpine-3.24"
	var log strings.Builder
	err := f.reporter(false, &log).report([]distros.Result{other, res("fail")})
	if err == nil || !strings.Contains(err.Error(), "fedora-44/amd64") {
		t.Fatalf("err = %v", err)
	}
	if len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "comment #5") {
		t.Fatalf("writes = %q", f.writes)
	}
	if !strings.Contains(log.String(), "fedora-44/amd64: error") {
		t.Errorf("log = %s", log.String())
	}
}
