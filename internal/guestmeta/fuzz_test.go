package guestmeta

import (
	"bytes"
	"strings"
	"testing"
)

// plausiblePath reports whether p could be a volume path: absolute, and
// free of ASCII control characters other than tab and newline. (Volume
// paths are checked for all control characters before they reach an
// fstab line; tab and newline are kept here to exercise the escaping.)
func plausiblePath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if (p[i] < 0x20 && p[i] != '\t' && p[i] != '\n') || p[i] == 0x7f {
			return false
		}
	}
	return true
}

// FuzzFstabEscaping checks that a mount point survives FstabLine: the
// line is a single line of exactly six fields (fstab(5) separates fields
// by spaces and tabs), unescaping the second field gives the path back,
// and HasMountPoint finds the entry under the cleaned path.
func FuzzFstabEscaping(f *testing.F) {
	for _, p := range []string{
		"/data", "/with space", "/tab\there", `/back\slash`, `/lit\040eral`, `/\134`,
		"/trailing/", "//double//slash", "/a/../b", "/new\nline", "/nbsp x", "/nel\u0085x",
		"/\\777", "/\\", "/ünï", "/", "/vt\vx",
	} {
		f.Add("data", p)
	}
	f.Fuzz(func(t *testing.T, name, p string) {
		// Volume names are limited to [a-z0-9-] by volume.ValidateName.
		if !plausiblePath(p) || strings.ContainsAny(name, " \t\r\n") {
			t.Skip()
		}
		line := FstabLine(name, p)
		if strings.Contains(line, "\n") {
			t.Fatalf("FstabLine(%q, %q) = %q has a newline", name, p, line)
		}
		fields := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
		if len(fields) != 6 {
			t.Fatalf("FstabLine(%q, %q) = %q has %d fields, want 6", name, p, line, len(fields))
		}
		if got := unescapeFstab(fields[1]); got != p {
			t.Fatalf("mount point %q came back as %q (line %q)", p, got, line)
		}
		if !HasMountPoint([]byte("# comment\n"+line+"\n"), p) {
			t.Fatalf("HasMountPoint does not find %q in %q", p, line)
		}
	})
}

// FuzzAppendFstab checks that appending lines to an fstab keeps what was
// there, adds each missing line once, reports a change exactly when it
// added something, and is idempotent: appending the same lines again
// changes nothing.
func FuzzAppendFstab(f *testing.F) {
	f.Add("", "LABEL=data /data ext4 defaults,nofail 0 2")
	f.Add("UUID=1 / ext4 defaults 0 1\n", "LABEL=data /data ext4 defaults,nofail 0 2\nLABEL=x /x ext4 defaults,nofail 0 2")
	f.Add("UUID=1 / ext4 defaults 0 1", "UUID=1 / ext4 defaults 0 1")
	f.Add("a\nb\n", "b\nc\nc")
	f.Add("a\r\nb\r\n", "a\nb")
	f.Add("\n\n", "")
	f.Fuzz(func(t *testing.T, existingS, linesS string) {
		existing := []byte(existingS)
		// Lines are single fstab lines; FstabLine escapes newlines.
		lines := strings.Split(linesS, "\n")

		updated, changed := AppendFstab(existing, lines)
		if !bytes.HasPrefix(updated, existing) {
			t.Fatalf("existing content was modified: %q -> %q", existing, updated)
		}
		if !changed {
			// Nothing to write; at most the missing final newline.
			if !bytes.Equal(bytes.TrimSuffix(updated, []byte("\n")), bytes.TrimSuffix(existing, []byte("\n"))) {
				t.Fatalf("changed is false but content differs: %q -> %q", existing, updated)
			}
		}
		count := func(b []byte) map[string]int {
			m := map[string]int{}
			for _, l := range strings.Split(string(b), "\n") {
				m[l]++
			}
			return m
		}
		have, before := count(updated), count(existing)
		for _, l := range lines {
			if have[l] == 0 {
				t.Fatalf("line %q is missing after AppendFstab", l)
			}
		}
		again, changedAgain := AppendFstab(updated, lines)
		if changedAgain || !bytes.Equal(again, updated) {
			t.Fatalf("second AppendFstab changed %q to %q", updated, again)
		}
		// A line is added at most once, however often it is in lines.
		for l, n := range have {
			if l == "" {
				continue
			}
			if n > max(before[l], 1) {
				t.Fatalf("line %q occurs %d times after AppendFstab, %d before: %q", l, n, before[l], updated)
			}
		}
	})
}
