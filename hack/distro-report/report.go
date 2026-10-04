package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/contemper-project/contemper/test/distros"
)

const (
	failureLabel = "distro-test-failure"
	areaLabel    = "area: target-distros"
)

var markerRE = regexp.MustCompile(`<!-- contemper-distro-test: ([A-Za-z0-9._-]+/[A-Za-z0-9._-]+) -->`)

func marker(key string) string { return "<!-- contemper-distro-test: " + key + " -->" }

// readResults loads every *.json file under dir (recursively, since
// downloaded artifacts may sit in one directory each). A missing dir means
// no results. A file that can't be read or isn't a valid result is skipped:
// the valid results are returned along with an error naming each bad file.
func readResults(dir string) ([]distros.Result, error) {
	var results []distros.Result
	var bad []error
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == dir {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // the path comes from walking the results directory we were given
		if err != nil {
			bad = append(bad, err)
			return nil
		}
		var r distros.Result
		if err := json.Unmarshal(data, &r); err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", path, err))
			return nil
		}
		if r.ID == "" || r.Arch == "" || (r.Outcome != "pass" && r.Outcome != "fail") {
			bad = append(bad, fmt.Errorf("%s: incomplete result", path))
			return nil
		}
		results = append(results, r)
		return nil
	})
	if err != nil {
		bad = append(bad, err)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].ID+"/"+results[i].Arch < results[j].ID+"/"+results[j].Arch
	})
	return results, errors.Join(bad...)
}

type reporter struct {
	gh      *client
	entries []distros.Entry
	runURL  string
	event   string
	dryRun  bool
	log     io.Writer
}

// tracked is what the existing issues say about one entry/architecture.
type tracked struct {
	open   *issue // oldest open issue
	closed *issue // most recent closed issue
}

func (r *reporter) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.log, format+"\n", args...)
}

// report acts on every result, carrying on past a failing one; the
// returned error joins what went wrong.
func (r *reporter) report(results []distros.Result) error {
	issues, err := r.gh.listIssues(failureLabel)
	if err != nil {
		return err
	}
	byKey := map[string]*tracked{}
	for i := range issues {
		is := &issues[i]
		m := markerRE.FindStringSubmatch(is.Body)
		if m == nil {
			continue // not one of ours
		}
		t := byKey[m[1]]
		if t == nil {
			t = &tracked{}
			byKey[m[1]] = t
		}
		switch {
		case is.State == "open" && (t.open == nil || is.Number < t.open.Number):
			t.open = is
		case is.State != "open" && (t.closed == nil || is.Number > t.closed.Number):
			t.closed = is
		}
	}

	have := map[string]bool{}
	var errs []error
	for _, res := range results {
		key := res.ID + "/" + res.Arch
		have[key] = true
		if res.RunURL == "" {
			res.RunURL = r.runURL
		}
		t := byKey[key]
		if t == nil {
			t = &tracked{}
		}
		var err error
		if res.Outcome == "fail" {
			err = r.fail(res, t)
		} else {
			err = r.pass(res, t)
		}
		if err != nil {
			r.logf("%s: error: %v", key, err)
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
	}
	for _, e := range r.entries {
		for _, a := range e.Arch {
			if key := e.ID + "/" + a; !have[key] {
				r.logf("%s: no result in this run, leaving its issues alone", key)
			}
		}
	}
	return errors.Join(errs...)
}

func (r *reporter) fail(res distros.Result, t *tracked) error {
	key := res.ID + "/" + res.Arch
	if t.open != nil {
		done, err := r.mentions(t.open, res.RunURL)
		if err != nil {
			return err
		}
		if done {
			r.logf("%s: failing, run already recorded on #%d", key, t.open.Number)
			return nil
		}
		r.logf("%s: failing, comment on #%d", key, t.open.Number)
		if r.dryRun {
			return nil
		}
		return r.gh.addComment(t.open.Number, failureComment(res))
	}
	title := fmt.Sprintf("Distribution test failing: %s (%s)", res.ID, res.Arch)
	prev := 0
	if t.closed != nil {
		prev = t.closed.Number
	}
	r.logf("%s: failing, create issue %q (previous: #%d)", key, title, prev)
	if r.dryRun {
		return nil
	}
	is, err := r.gh.createIssue(title, r.issueBody(res, prev), []string{failureLabel, areaLabel})
	if err != nil {
		return err
	}
	r.logf("%s: created #%d", key, is.Number)
	return nil
}

func (r *reporter) pass(res distros.Result, t *tracked) error {
	key := res.ID + "/" + res.Arch
	if t.open == nil {
		r.logf("%s: passing", key)
		return nil
	}
	done, err := r.mentions(t.open, res.RunURL)
	if err != nil {
		return err
	}
	r.logf("%s: passing again, close #%d", key, t.open.Number)
	if r.dryRun {
		return nil
	}
	if !done {
		if err := r.gh.addComment(t.open.Number, "Passing again in "+res.RunURL+"."); err != nil {
			return err
		}
	}
	return r.gh.closeIssue(t.open.Number)
}

// mentions reports whether the issue body or one of its comments already
// names the run, which makes a re-run for the same run a no-op.
func (r *reporter) mentions(is *issue, runURL string) (bool, error) {
	if containsURL(is.Body, runURL) {
		return true, nil
	}
	comments, err := r.gh.listComments(is.Number)
	if err != nil {
		return false, err
	}
	for _, c := range comments {
		if containsURL(c.Body, runURL) {
			return true, nil
		}
	}
	return false, nil
}

func details(res distros.Result) string {
	stage := res.Stage
	if stage == "" {
		stage = "unknown"
	}
	digest := "unknown"
	if res.BaseDigest != "" {
		digest = "`" + res.BaseDigest + "`"
	}
	return fmt.Sprintf("- Failed stage: %s\n- Base image digest: %s\n", stage, digest)
}

func failureComment(res distros.Result) string {
	return fmt.Sprintf("Still failing in %s.\n\n%s", res.RunURL, details(res))
}

func (r *reporter) issueBody(res distros.Result, previous int) string {
	var b strings.Builder
	b.WriteString(marker(res.ID + "/" + res.Arch))
	fmt.Fprintf(&b, "\nThe distribution test for **%s** (%s) is failing.\n\n", res.ID, res.Arch)
	for _, e := range r.entries {
		if e.ID == res.ID {
			fmt.Fprintf(&b, "- Base image: `%s`\n- Family: %s\n", e.Base, e.Family)
		}
	}
	fmt.Fprintf(&b, "- First failing run: %s", res.RunURL)
	if r.event != "" {
		fmt.Fprintf(&b, " (%s)", r.event)
	}
	b.WriteString("\n" + details(res))
	if previous > 0 {
		fmt.Fprintf(&b, "- Previously reported in #%d\n", previous)
	}
	b.WriteString("\nLater failing runs are added as comments. This issue is closed automatically when the test passes again.\n")
	return b.String()
}

// containsURL reports whether text names url, not merely a longer URL that
// starts with it (run 12 versus run 123).
func containsURL(text, url string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], url)
		if j < 0 {
			return false
		}
		end := i + j + len(url)
		if end == len(text) || text[end] < '0' || text[end] > '9' {
			return true
		}
		i = end
	}
}
