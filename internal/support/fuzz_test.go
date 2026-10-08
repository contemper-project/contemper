package support_test

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/support"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// annotationsFrom reads "key=value" lines into a map, so a single fuzzed
// string can carry several annotations.
func annotationsFrom(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}

// FuzzParseAnnotations parses fuzzed annotation sets and checks that the
// result is deterministic, well formed, and resolvable without a panic
// against any path-existence function.
func FuzzParseAnnotations(f *testing.F) {
	f.Add("io.contemper.requires.files=/etc/os-release, /usr/bin/x ,,", uint64(0))
	f.Add("io.contemper.branch.init-system.openrc.requires.files=/sbin/openrc-init\n"+
		"io.contemper.branch.init-system.openrc.image=ghcr.io/example/openrc:v1\n"+
		"io.contemper.branch.init-system.systemd.requires.files=/usr/lib/systemd/systemd\n"+
		"io.contemper.branch.init-system.systemd.image=ghcr.io/example/systemd:v1", uint64(1))
	f.Add("io.contemper.branch.init-system.default=none\nio.contemper.branch.init-system.openrc.requires.files=/a", uint64(2))
	f.Add("io.contemper.branch.init-system.default= none \n", uint64(0))
	f.Add("io.contemper.branch.Init-System.openrc.requires.files=/x", uint64(0))
	f.Add("io.contemper.branch.-init.openrc.requires.files=/x", uint64(0))
	f.Add("io.contemper.branch.init-system.default=Bad_Name", uint64(0))
	f.Add("io.contemper.branch.init-system.openrc.requires=x", uint64(0))
	f.Add("io.contemper.branch.init-system.openrc.requires.files.extra=x", uint64(0))
	f.Add("io.contemper.branch.a.b.image=x\nio.contemper.branch.a.default=b", uint64(3))
	f.Add("io.contemper.branch.a..image=x", uint64(0))
	f.Fuzz(func(t *testing.T, raw string, mask uint64) {
		if len(raw) > 8192 {
			t.Skip()
		}
		anns := annotationsFrom(raw)
		schema, err := support.Parse(anns)
		again, err2 := support.Parse(anns)
		if (err == nil) != (err2 == nil) || (err != nil && err.Error() != err2.Error()) {
			t.Fatalf("Parse is not deterministic: %v vs %v", err, err2)
		}
		if err != nil {
			if schema != nil {
				t.Fatalf("Parse returned a schema together with an error")
			}
			return
		}
		if !reflect.DeepEqual(schema, again) {
			t.Fatalf("Parse is not deterministic: %+v vs %+v", schema, again)
		}
		checkPaths := func(what string, paths []string) {
			for _, p := range paths {
				if p == "" || p != strings.TrimSpace(p) || strings.Contains(p, ",") {
					t.Fatalf("%s has malformed path %q", what, p)
				}
			}
		}
		checkPaths("requires", schema.Requires)
		if !sort.SliceIsSorted(schema.Branches, func(i, j int) bool { return schema.Branches[i].Name < schema.Branches[j].Name }) {
			t.Fatalf("branches are not sorted: %+v", schema.Branches)
		}
		for i, b := range schema.Branches {
			if !validName.MatchString(b.Name) || (i > 0 && schema.Branches[i-1].Name == b.Name) {
				t.Fatalf("bad or duplicate branch name %q", b.Name)
			}
			if len(b.Variants) == 0 {
				t.Fatalf("branch %q has no variants", b.Name)
			}
			hasDefault := b.Default == ""
			for j, v := range b.Variants {
				if !validName.MatchString(v.Name) || (j > 0 && b.Variants[j-1].Name >= v.Name) {
					t.Fatalf("branch %q: bad, duplicate or unsorted variant name %q", b.Name, v.Name)
				}
				checkPaths("variant "+v.Name, v.Requires)
				if v.Name == b.Default {
					hasDefault = true
				} else if len(v.Requires) == 0 {
					t.Fatalf("branch %q: variant %q has neither a predicate nor the default role", b.Name, v.Name)
				}
				if v.Image != strings.TrimSpace(v.Image) {
					t.Fatalf("variant image %q is not trimmed", v.Image)
				}
			}
			if !hasDefault {
				t.Fatalf("branch %q: default %q is not one of its variants", b.Name, b.Default)
			}
		}

		// Resolve against an arbitrary existence function (bit i of
		// mask for the i-th distinct path mentioned).
		idx := map[string]int{}
		for _, b := range schema.Branches {
			for _, v := range b.Variants {
				for _, p := range v.Requires {
					if _, ok := idx[p]; !ok {
						idx[p] = len(idx)
					}
				}
			}
		}
		exists := func(p string) bool { return mask>>(uint(idx[p])%64)&1 == 1 }
		resolved, err := support.Resolve(schema, exists)
		resolved2, err2 := support.Resolve(schema, exists)
		if (err == nil) != (err2 == nil) || !reflect.DeepEqual(resolved, resolved2) {
			t.Fatalf("Resolve is not deterministic")
		}
		if err != nil {
			return
		}
		if len(resolved) != len(schema.Branches) {
			t.Fatalf("%d resolutions for %d branches", len(resolved), len(schema.Branches))
		}
		for i, r := range resolved {
			b := schema.Branches[i]
			if r.Branch != b.Name {
				t.Fatalf("resolution %d is for %q, want %q", i, r.Branch, b.Name)
			}
			var won *support.Variant
			for j := range b.Variants {
				if b.Variants[j].Name == r.Variant {
					won = &b.Variants[j]
				}
			}
			if won == nil || won.Image != r.Image {
				t.Fatalf("branch %q resolved to %q (image %q), not one of its variants", b.Name, r.Variant, r.Image)
			}
			if r.Default {
				if r.Variant != b.Default || r.Matched != nil {
					t.Fatalf("branch %q default win is inconsistent: %+v", b.Name, r)
				}
				continue
			}
			if len(r.Matched) == 0 || !reflect.DeepEqual(r.Matched, won.Requires) {
				t.Fatalf("branch %q predicate win has Matched %q, want %q", b.Name, r.Matched, won.Requires)
			}
			for _, p := range r.Matched {
				if !exists(p) {
					t.Fatalf("branch %q matched on missing path %q", b.Name, p)
				}
			}
		}
	})
}

// FuzzMergeDeclarations checks the precedence (labels, then manifest,
// then index), that the result holds exactly the union of the keys, and
// that the inputs are untouched.
func FuzzMergeDeclarations(f *testing.F) {
	f.Add("io.contemper.requires.files=/from-index\nk=v", "io.contemper.requires.files=/from-manifest", "io.contemper.requires.files=/from-label")
	f.Add("", "", "")
	f.Add("a=1", "", "")
	f.Add("", "a=2", "a=3")
	f.Fuzz(func(t *testing.T, index, manifest, labels string) {
		idx, man, lab := annotationsFrom(index), annotationsFrom(manifest), annotationsFrom(labels)
		idxCopy, manCopy, labCopy := annotationsFrom(index), annotationsFrom(manifest), annotationsFrom(labels)
		merged := support.MergeDeclarations(lab, man, idx)
		if !reflect.DeepEqual(idx, idxCopy) || !reflect.DeepEqual(man, manCopy) || !reflect.DeepEqual(lab, labCopy) {
			t.Fatalf("MergeDeclarations modified its input")
		}
		for k := range merged {
			_, a := idx[k]
			_, b := man[k]
			_, c := lab[k]
			if !a && !b && !c {
				t.Fatalf("merged has extra key %q", k)
			}
		}
		for k, v := range idx {
			want := v
			if mv, ok := man[k]; ok {
				want = mv
			}
			if lv, ok := lab[k]; ok {
				want = lv
			}
			if merged[k] != want {
				t.Fatalf("merged[%q] = %q, want %q", k, merged[k], want)
			}
		}
		for k, v := range man {
			want := v
			if lv, ok := lab[k]; ok {
				want = lv
			}
			if merged[k] != want {
				t.Fatalf("merged[%q] = %q, want %q", k, merged[k], want)
			}
		}
		for k, v := range lab {
			if merged[k] != v {
				t.Fatalf("label value for %q lost: %q", k, merged[k])
			}
		}
	})
}
