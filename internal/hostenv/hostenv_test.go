package hostenv_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/hostenv"
)

func TestFindOnPATH(t *testing.T) {
	// "sh" should be found on PATH in any test environment.
	if p := hostenv.Find("sh"); p == "" {
		t.Errorf("Find(sh) should locate a shell on PATH")
	}
}

func TestFindMissing(t *testing.T) {
	if p := hostenv.Find("definitely-not-a-real-tool-xyz"); p != "" {
		t.Errorf("Find(bogus) = %q, want empty", p)
	}
}

func TestInstallHint(t *testing.T) {
	hint := hostenv.InstallHint("mkfs.ext4")
	if !strings.Contains(hint, "e2fsprogs") {
		t.Errorf("InstallHint(mkfs.ext4) = %q, want it to mention e2fsprogs", hint)
	}
}

func TestRequiredMissing(t *testing.T) {
	if _, err := hostenv.Required("definitely-not-a-real-tool-xyz"); err == nil {
		t.Errorf("Required(bogus): expected an error")
	}
}

func TestInstallHintQemuImg(t *testing.T) {
	want := "apt install qemu-utils"
	if runtime.GOOS == "darwin" {
		want = "brew install qemu"
	}
	if got := hostenv.InstallHint("qemu-img"); got != want {
		t.Errorf("InstallHint(qemu-img) = %q, want %q", got, want)
	}
}
