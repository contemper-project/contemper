package disk

import (
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
)

func TestFindErrorMarkerIgnoresEchoedLines(t *testing.T) {
	script := "write \"f000000\" \"/already exists\"\nsif \"/Usage: notes\" mode 0100644\n"
	out := "debugfs 1.47.0 (5-Feb-2023)\n" +
		"debugfs: write \"f000000\" \"/already exists\"\n" +
		"Allocated inode: 12\n" +
		"debugfs: sif \"/Usage: notes\" mode 0100644\n"
	if got := findErrorMarker(script, out); got != "" {
		t.Errorf("findErrorMarker on a clean run = %q, want none", got)
	}

	out += "/missing: File not found by ext2_lookup \n"
	if got := findErrorMarker(script, out); got != "File not found" {
		t.Errorf("findErrorMarker = %q, want %q", got, "File not found")
	}
}

// TestFindErrorMarkerDetectsFullDirectory checks that debugfs's ln
// failure on a directory whose last block is already full - which
// exits 0 and otherwise leaves no trace - is caught.
func TestFindErrorMarkerDetectsFullDirectory(t *testing.T) {
	script := `ln "/target" "/d/hardlink_target"` + "\n"
	out := "debugfs 1.47.0 (5-Feb-2023)\n" +
		`debugfs: ln "/target" "/d/hardlink_target"` + "\n" +
		"make_link: No free space in the directory \n"
	if got := findErrorMarker(script, out); got != "No free space in the directory" {
		t.Errorf("findErrorMarker = %q, want %q", got, "No free space in the directory")
	}
}

func TestMke2fsConfPinsFeatures(t *testing.T) {
	for _, want := range []string{
		"features = has_journal,extent,huge_file,flex_bg,metadata_csum,64bit,dir_nlink,extra_isize\n",
		"base_features = sparse_super,large_file,filetype,resize_inode,dir_index,ext_attr\n",
		"blocksize = 4096\n",
		"inode_size = 256\n",
		"inode_ratio = 16384\n",
	} {
		if !strings.Contains(mke2fsConf, want) {
			t.Errorf("mke2fsConf is missing %q", want)
		}
	}
	for _, banned := range []string{"orphan_file", "metadata_csum_seed", "uninit_bg"} {
		if strings.Contains(mke2fsConf, banned) {
			t.Errorf("mke2fsConf must not mention %q", banned)
		}
	}
}

func TestRunCmdEnvPassesEnvironment(t *testing.T) {
	t.Setenv("CONTEMPER_TEST_INHERITED", "inherited")
	out, err := runCmdEnv(t.Context(), "", []string{mke2fsConfigEnv + "=/some/mke2fs.conf"},
		"sh", "-c", `printf '%s %s' "$MKE2FS_CONFIG" "$CONTEMPER_TEST_INHERITED"`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "/some/mke2fs.conf inherited" {
		t.Errorf("env = %q, want the extra entry plus the inherited environment", out)
	}
}

func TestBuildDebugfsScriptSkipsUnknownXattrNamespaces(t *testing.T) {
	img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: "amd64"}, nil, []imgtest.File{
		{Path: "f", Data: []byte("x"), Xattrs: map[string]string{
			"user.ok":          "1",
			"security.ok":      "2",
			"-f/etc/passwd":    "3",
			"bogus.name":       "4",
			"user.":            "5",
			"-user.looks.fine": "6",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(t.Context(), img)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rfs.Close() }()

	script, warnings, err := buildDebugfsScript(t.Context(), rfs, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(script, "ea_set "); n != 2 {
		t.Errorf("script has %d ea_set lines, want 2:\n%s", n, script)
	}
	for _, bad := range []string{"-f/etc/passwd", "bogus.name", "-user.looks.fine"} {
		if strings.Contains(script, bad) {
			t.Errorf("script mentions %q", bad)
		}
		found := false
		for _, w := range warnings {
			found = found || (strings.Contains(w, "/f:") && strings.Contains(w, bad))
		}
		if !found {
			t.Errorf("no warning for %q in %q", bad, warnings)
		}
	}
}

func TestQuoteArgRejectsUnrepresentableCharacters(t *testing.T) {
	for _, s := range []string{`a"b`, "a\nb", "a\rb", "a\x00b"} {
		if q, err := quoteArg(s); err == nil {
			t.Errorf("quoteArg(%q) = %q, want an error", s, q)
		}
	}
	if q, err := quoteArg("/a b"); err != nil || q != `"/a b"` {
		t.Errorf("quoteArg(%q) = %q, %v", "/a b", q, err)
	}
}
