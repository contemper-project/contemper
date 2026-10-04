package bundle_test

import (
	"testing"

	"github.com/contemper-project/contemper/internal/bundle"
)

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"my-app-v1.2_3":      "my-app-v1.2_3",
		"app-latest":         "app-latest",
		"a/b":                "a-b",
		"app\x1b[2J-x":       "app--2J-x",
		"héllo":              "h--llo",
		"":                   "bundle",
		".":                  "bundle-.",
		"..":                 "bundle-..",
		"-rf":                "bundle-rf",
		"../../etc/x":        ".._.._etc_x",
		"white space\nnewln": "white-space-newln",
	}
	delete(cases, "../../etc/x")
	for in, want := range cases {
		if got := bundle.SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := bundle.SafeName("../../etc/x"); got != "..-..-etc-x" {
		t.Errorf("SafeName traversal = %q", got)
	}
}
