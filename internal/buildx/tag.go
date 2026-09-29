package buildx

import (
	"path/filepath"
	"strings"
)

// defaultTagSuffix is the tag `contemper build` gives an image when the
// user supplies no --tag.
const defaultTagSuffix = "dev"

// DefaultTag derives the default image tag for a build context
// directory: the directory's base name, sanitized into a valid Docker
// repository name, with tag "dev" - e.g. "./my-app" gives "my-app:dev".
// A name with nothing usable left in it (the root directory, "-" for a
// context read from stdin) gives "image:dev".
func DefaultTag(contextDir string) string {
	dir := contextDir
	if dir == "" {
		dir = "."
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	base := SanitizeRepoName(filepath.Base(filepath.Clean(dir)))
	if base == "" {
		base = "image"
	}
	return base + ":" + defaultTagSuffix
}

// SanitizeRepoName turns s into a valid single-component Docker
// repository name: lowercase letters and digits, joined by the
// separators Docker allows between them ("." , "_", "__" or a run of
// "-"). Any other character becomes "-", a run of separators Docker
// wouldn't accept collapses to a single "-", and separators at either
// end are dropped. The result is empty when s has no letter or digit.
func SanitizeRepoName(s string) string {
	var b strings.Builder
	var sep []rune
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if b.Len() > 0 && len(sep) > 0 {
				b.WriteString(separator(string(sep)))
			}
			sep = sep[:0]
			b.WriteRune(r)
			continue
		}
		if r != '.' && r != '_' {
			r = '-'
		}
		sep = append(sep, r)
	}
	return b.String()
}

// separator returns sep if Docker accepts it between two alphanumeric
// runs of a repository name, and "-" otherwise.
func separator(sep string) string {
	if sep == "." || sep == "_" || sep == "__" || strings.Trim(sep, "-") == "" {
		return sep
	}
	return "-"
}
