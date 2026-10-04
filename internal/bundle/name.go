package bundle

import "strings"

// SafeName turns s, a bundle name derived from an image's annotations or
// reference, into one that is safe as a directory name and to print:
// every character outside [A-Za-z0-9._-] becomes '-', and a result that
// is empty, ".", or ".." (or starts with '-', which a command line would
// read as an option) is prefixed so it stays a plain name. Ordinary image
// names are returned unchanged.
func SafeName(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			b[i] = '-'
		}
	}
	out := string(b)
	switch {
	case out == "":
		return "bundle"
	case strings.Trim(out, ".") == "":
		return "bundle-" + out
	case out[0] == '-':
		return "bundle" + out
	}
	return out
}
