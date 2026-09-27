package volume_test

import (
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/volume"
)

func TestDeriveName(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/data", "data"},
		{"/var/lib/app", "var-lib-app"},
		{"/Var/Lib/APP", "var-lib-app"},
		{"/etc/app!config", "etc-appconfig"},
	}
	for _, c := range cases {
		if got := volume.DeriveName(c.path); got != c.want {
			t.Errorf("DeriveName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestDeriveNameLongPathShortened(t *testing.T) {
	long := "/var/lib/some/very/long/application/data/directory"
	got := volume.DeriveName(long)
	if len(got) != 16 {
		t.Fatalf("DeriveName(%q) = %q, len %d, want 16", long, got, len(got))
	}
	if !strings.HasPrefix(got, "var-lib-som-") {
		t.Errorf("DeriveName(%q) = %q, want the 11-char prefix kept", long, got)
	}
	// Two different long paths sharing an 11-char prefix must not collide.
	other := "/var/lib/some/other/incredibly/long/path"
	if got2 := volume.DeriveName(other); got2 == got {
		t.Errorf("two different long paths derived the same name %q", got)
	}
}

func TestDeriveNameEmptyAfterFiltering(t *testing.T) {
	got := volume.DeriveName("/!!!")
	if !strings.HasPrefix(got, "v-") || len(got) != 8 {
		t.Errorf("DeriveName(%q) = %q, want an 8-char v-<hex> fallback", "/!!!", got)
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"data", "var-lib-app", "a", strings.Repeat("a", 16)}
	for _, n := range valid {
		if err := volume.ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q): %v", n, err)
		}
	}
	invalid := []string{"", strings.Repeat("a", 17), "Data", "my_volume", "my.volume", "a b"}
	for _, n := range invalid {
		if err := volume.ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q): expected an error", n)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1073741824", 1073741824},
		{"2GiB", 2 << 30},
		{"512MiB", 512 << 20},
		{"1KiB", 1 << 10},
		{"1GB", 1_000_000_000},
	}
	for _, c := range cases {
		got, err := volume.ParseSize(c.in)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := volume.ParseSize("not-a-size"); err == nil {
		t.Errorf("expected an error for an invalid size")
	}
}

func TestFromConfigDerivesNames(t *testing.T) {
	specs, err := volume.FromConfig([]string{"/data", "/var/log"}, nil)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want 2", len(specs))
	}
	// Sorted by path.
	if specs[0].Path != "/data" || specs[0].Name != "data" || specs[0].SizeBytes != 0 {
		t.Errorf("specs[0] = %+v", specs[0])
	}
	if specs[1].Path != "/var/log" || specs[1].Name != "var-log" {
		t.Errorf("specs[1] = %+v", specs[1])
	}
}

func TestFromConfigLabelsOverrideNameAndSize(t *testing.T) {
	labels := map[string]string{
		"io.contemper.volume./data.size": "5GiB",
		"io.contemper.volume./data.name": "friendly",
	}
	specs, err := volume.FromConfig([]string{"/data"}, labels)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if specs[0].Name != "friendly" {
		t.Errorf("Name = %q, want friendly", specs[0].Name)
	}
	if specs[0].SizeBytes != 5<<30 {
		t.Errorf("SizeBytes = %d, want %d", specs[0].SizeBytes, 5<<30)
	}
}

func TestFromConfigInvalidExplicitName(t *testing.T) {
	labels := map[string]string{"io.contemper.volume./data.name": "Not_Valid"}
	if _, err := volume.FromConfig([]string{"/data"}, labels); err == nil {
		t.Errorf("expected an error for an invalid explicit name")
	}
}

func TestFromConfigDuplicateNamesFail(t *testing.T) {
	// /a and /a2 both derive to a name colliding only if given the same
	// explicit label; use explicit collision to keep this deterministic.
	labels := map[string]string{
		"io.contemper.volume./one.name": "shared",
		"io.contemper.volume./two.name": "shared",
	}
	_, err := volume.FromConfig([]string{"/one", "/two"}, labels)
	if err == nil {
		t.Fatalf("expected an error for duplicate names")
	}
	if !strings.Contains(err.Error(), "/one") || !strings.Contains(err.Error(), "/two") || !strings.Contains(err.Error(), "shared") {
		t.Errorf("error should name both paths and the name: %v", err)
	}
}

func TestFromConfigInvalidSizeLabel(t *testing.T) {
	labels := map[string]string{"io.contemper.volume./data.size": "bogus"}
	if _, err := volume.FromConfig([]string{"/data"}, labels); err == nil {
		t.Errorf("expected an error for an invalid size label")
	}
}

func TestRootSize(t *testing.T) {
	size, err := volume.RootSize(map[string]string{"io.contemper.root.size": "2GiB"})
	if err != nil {
		t.Fatal(err)
	}
	if size != 2<<30 {
		t.Errorf("RootSize = %d, want %d", size, 2<<30)
	}
	if size, err := volume.RootSize(nil); err != nil || size != 0 {
		t.Errorf("RootSize(nil) = %d, %v, want 0, nil", size, err)
	}
}

func TestFstabOptedOut(t *testing.T) {
	if volume.FstabOptedOut(nil) {
		t.Errorf("nil labels should not opt out")
	}
	if !volume.FstabOptedOut(map[string]string{"io.contemper.fstab": "false"}) {
		t.Errorf("expected opt-out")
	}
	if volume.FstabOptedOut(map[string]string{"io.contemper.fstab": "true"}) {
		t.Errorf("did not expect opt-out")
	}
}

func TestFromConfigRejectsInvalidPaths(t *testing.T) {
	for _, p := range []string{"data", "/data\nLABEL=x /etc ext4 defaults 0 0", "/tab\there", "/del\x7f"} {
		if _, err := volume.FromConfig([]string{p}, nil); err == nil {
			t.Errorf("FromConfig(%q) succeeded, want an error", p)
		}
	}
	if _, err := volume.FromConfig([]string{"/srv/my data"}, nil); err != nil {
		t.Errorf("FromConfig with a space in the path: %v", err)
	}
}
