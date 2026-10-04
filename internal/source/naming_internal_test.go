package source

import "testing"

func TestArchiveBaseName(t *testing.T) {
	for in, want := range map[string]string{
		"a/b/example.tar":    "example",
		"a/b/example.tar.gz": "example",
		"example.tgz":        "example",
		"a/b/example":        "example",
		"a/../b.tar":         "b",
		".tar":               ".tar",
		"dir/.tar.gz":        ".tar.gz",
	} {
		if got := archiveBaseName(in); got != want {
			t.Errorf("archiveBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}
