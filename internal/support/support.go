// Package support parses and resolves a support image's annotations: its
// unconditional file requirements, and its branch/variant declarations.
//
// See docs/reference/support-image-annotations.md for the schema this
// package implements.
package support

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/contemper-project/contemper/internal/rootfs"
)

// RequiresFilesAnnotation names a comma-separated list of absolute paths
// that must exist in the final merged rootfs, set on the support image's
// own manifest (or its index descriptor, see MergeAnnotations).
const RequiresFilesAnnotation = "io.contemper.requires.files"

// branchPrefix introduces every branch/variant annotation key:
// "io.contemper.branch.<branch>.<rest>".
const branchPrefix = "io.contemper.branch."

// namePattern is the shape required of every branch and variant name.
const namePattern = `[a-z0-9][a-z0-9-]*`

var nameRe = regexp.MustCompile("^" + namePattern + "$")

// Variant is one declared alternative within a Branch.
type Variant struct {
	// Name is the variant's name, e.g. "openrc".
	Name string
	// Requires lists the absolute paths that must all exist (AND) in the
	// source image's merged filesystem for this variant to match. Empty
	// only for a variant that is solely a branch's declared default.
	Requires []string
	// Image is the reference whose layers this variant contributes when
	// it wins, resolved for the same platform as everything else. Empty
	// means the variant is a no-op.
	Image string
}

// Branch is one independently-resolved axis of variation.
type Branch struct {
	// Name is the branch's name, e.g. "init-system".
	Name string
	// Variants is every variant declared in this branch, sorted by name.
	Variants []Variant
	// Default is the variant name applied when no predicate matches, or
	// "" if the branch declares none.
	Default string
}

// Schema is a support image's fully parsed annotation set.
type Schema struct {
	// Requires lists RequiresFilesAnnotation's paths.
	Requires []string
	// Branches is every declared branch, sorted by name.
	Branches []Branch
}

// MergeAnnotations combines a support image's index-descriptor
// annotations (the fallback) with its platform manifest's annotations
// (which win on any key present in both), as
// docs/reference/support-image-annotations.md specifies. Either map may
// be nil.
func MergeAnnotations(indexAnnotations, manifestAnnotations map[string]string) map[string]string {
	merged := make(map[string]string, len(indexAnnotations)+len(manifestAnnotations))
	for k, v := range indexAnnotations {
		merged[k] = v
	}
	for k, v := range manifestAnnotations {
		merged[k] = v
	}
	return merged
}

// Parse reads annotations (as produced by MergeAnnotations, or a plain
// manifest's annotations when there is no index fallback) into a Schema,
// validating branch/variant names and each branch's shape as it goes.
func Parse(annotations map[string]string) (*Schema, error) {
	schema := &Schema{Requires: splitPaths(annotations[RequiresFilesAnnotation])}

	type building struct {
		variants map[string]*Variant
		order    []string
		def      string
	}
	branches := map[string]*building{}
	var branchOrder []string

	branch := func(name string) *building {
		b, ok := branches[name]
		if !ok {
			b = &building{variants: map[string]*Variant{}}
			branches[name] = b
			branchOrder = append(branchOrder, name)
		}
		return b
	}
	variant := func(b *building, name string) *Variant {
		v, ok := b.variants[name]
		if !ok {
			v = &Variant{Name: name}
			b.variants[name] = v
			b.order = append(b.order, name)
		}
		return v
	}

	// Sort keys first so parse errors are deterministic regardless of Go's
	// randomized map iteration order.
	keys := make([]string, 0, len(annotations))
	for k := range annotations {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if !strings.HasPrefix(key, branchPrefix) {
			continue
		}
		value := annotations[key]
		rest := strings.TrimPrefix(key, branchPrefix)
		segs := strings.Split(rest, ".")
		if len(segs) < 2 || !nameRe.MatchString(segs[0]) {
			return nil, fmt.Errorf("invalid support-image annotation %q", key)
		}
		b := branch(segs[0])
		switch {
		case len(segs) == 2 && segs[1] == "default":
			name := strings.TrimSpace(value)
			if !nameRe.MatchString(name) {
				return nil, fmt.Errorf("invalid support-image annotation %q: default %q is not a valid variant name", key, name)
			}
			b.def = name
			variant(b, name) // a default is a declared variant even if named nowhere else

		case len(segs) == 3 && segs[2] == "image" && nameRe.MatchString(segs[1]):
			variant(b, segs[1]).Image = strings.TrimSpace(value)

		case len(segs) == 4 && segs[2] == "requires" && segs[3] == "files" && nameRe.MatchString(segs[1]):
			variant(b, segs[1]).Requires = splitPaths(value)

		default:
			return nil, fmt.Errorf("invalid support-image annotation %q", key)
		}
	}

	sort.Strings(branchOrder)
	for _, name := range branchOrder {
		b := branches[name]
		sort.Strings(b.order)
		variants := make([]Variant, 0, len(b.order))
		for _, vn := range b.order {
			v := *b.variants[vn]
			if v.Name != b.def && len(v.Requires) == 0 {
				return nil, fmt.Errorf("branch %s: variant %s has no requires.files predicate and is not the branch default", name, v.Name)
			}
			variants = append(variants, v)
		}
		schema.Branches = append(schema.Branches, Branch{Name: name, Variants: variants, Default: b.def})
	}
	return schema, nil
}

// splitPaths parses a comma-separated path list, trimming whitespace and
// dropping empty entries.
func splitPaths(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var paths []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// CheckRequires verifies every path in s.Requires exists in rfs.
func (s *Schema) CheckRequires(rfs *rootfs.Rootfs) error {
	if missing := missingPaths(s.Requires, func(p string) bool {
		_, err := rfs.Resolve(p)
		return err == nil
	}); len(missing) > 0 {
		return fmt.Errorf("support image requires missing paths: %s", strings.Join(missing, ", "))
	}
	return nil
}

func missingPaths(paths []string, exists func(string) bool) []string {
	var missing []string
	for _, p := range paths {
		if !exists(p) {
			missing = append(missing, p)
		}
	}
	return missing
}

// Resolved is one branch's winning variant, as returned by Resolve.
type Resolved struct {
	Branch  string
	Variant string
	// Image is the winning variant's image reference, or "" for a no-op.
	Image string
	// Default records whether the variant won as the branch's declared
	// default (no predicate matched, or it has none) rather than by a
	// predicate match, for progress reporting.
	Default bool
	// Matched lists the paths checked for the winning variant when it won
	// by a predicate match (nil for a Default win).
	Matched []string
}

// Resolve evaluates schema's branches against exists, a path-existence
// function that must be backed by the source image's merged filesystem
// only, before any support-image layers are merged (so a support image
// can never satisfy its own predicates). It returns one Resolved per
// branch, in the branches' sorted order.
func Resolve(schema *Schema, exists func(path string) bool) ([]Resolved, error) {
	out := make([]Resolved, 0, len(schema.Branches))
	for _, b := range schema.Branches {
		type checked struct {
			variant Variant
			missing []string
		}
		var matched, checks []checked
		for _, v := range b.Variants {
			if len(v.Requires) == 0 {
				// Only a branch's default may lack a predicate; it is
				// never a match candidate on its own.
				continue
			}
			c := checked{variant: v, missing: missingPaths(v.Requires, exists)}
			checks = append(checks, c)
			if len(c.missing) == 0 {
				matched = append(matched, c)
			}
		}

		switch len(matched) {
		case 1:
			out = append(out, Resolved{
				Branch:  b.Name,
				Variant: matched[0].variant.Name,
				Image:   matched[0].variant.Image,
				Matched: matched[0].variant.Requires,
			})

		case 0:
			if b.Default == "" {
				var parts []string
				for _, c := range checks {
					parts = append(parts, fmt.Sprintf("%s (checked %s, missing %s)",
						c.variant.Name, strings.Join(c.variant.Requires, ", "), strings.Join(c.missing, ", ")))
				}
				return nil, fmt.Errorf("branch %s: no variant matched and no default is declared: %s", b.Name, strings.Join(parts, "; "))
			}
			out = append(out, Resolved{Branch: b.Name, Variant: b.Default, Image: variantNamed(b, b.Default).Image, Default: true})

		default:
			var parts []string
			for _, c := range matched {
				parts = append(parts, fmt.Sprintf("%s (checked %s)", c.variant.Name, strings.Join(c.variant.Requires, ", ")))
			}
			return nil, fmt.Errorf("branch %s: %d variants matched, want exactly one: %s", b.Name, len(matched), strings.Join(parts, "; "))
		}
	}
	return out, nil
}

func variantNamed(b Branch, name string) Variant {
	for _, v := range b.Variants {
		if v.Name == name {
			return v
		}
	}
	return Variant{Name: name}
}
