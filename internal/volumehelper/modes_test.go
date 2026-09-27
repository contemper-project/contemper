package volumehelper

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHelperScriptsAreExecutable guards the file modes the published
// volumes-support images inherit from the checkout: an init script or
// helper committed as 0644 is silently skipped at boot.
func TestHelperScriptsAreExecutable(t *testing.T) {
	root := filepath.Join("..", "..", "support", "volumes-support")
	for _, rel := range []string{
		"base/usr/lib/contemper/format-volumes",
		"openrc/etc/init.d/contemper-volumes",
		"systemd/etc/systemd/system-generators/contemper-volumes",
	} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is %v, want executable (git update-index --chmod=+x)", rel, info.Mode().Perm())
		}
	}
}
