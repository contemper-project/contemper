package disk

import (
	"strings"
	"testing"
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
