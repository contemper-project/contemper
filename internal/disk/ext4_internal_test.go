package disk

import "testing"

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
