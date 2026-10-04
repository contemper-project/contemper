package volume

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// labelsFrom reads "key=value" lines into a label map.
func labelsFrom(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}

// FuzzParseSize checks ParseSize against what a size has to be: never
// negative, a plain byte count round-trips, and a count with a binary or
// decimal suffix is that count times the suffix.
func FuzzParseSize(f *testing.F) {
	for _, s := range []string{
		"", "0", "1073741824", "2GiB", "512MiB", "1KiB", "1GB", "1.5GiB", " 3 MB ", "10b", "2gib",
		"not-a-size", "-1", "-5GiB", "NaNGiB", "InfMiB", "1e30GiB", "0x10", "1_000", "+5", "9223372036854775807",
		"9223372036854775808", "8EiB", "1e18B", "1GıB", "B", "GiB", ".5KB", "1e3KB",
	} {
		f.Add(s, uint32(7))
	}
	f.Fuzz(func(t *testing.T, s string, n uint32) {
		got, err := ParseSize(s)
		if err == nil && got < 0 {
			t.Fatalf("ParseSize(%q) = %d", s, got)
		}
		if err == nil && strings.TrimSpace(s) != s {
			if again, err := ParseSize(strings.TrimSpace(s)); err != nil || again != got {
				t.Fatalf("ParseSize(%q) = %d but the trimmed string gives %d, %v", s, got, again, err)
			}
		}
		for _, sfx := range []struct {
			s    string
			mult int64
		}{{"", 1}, {"B", 1}, {"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}} {
			in := strconv.FormatUint(uint64(n), 10) + sfx.s
			got, err := ParseSize(in)
			if want := int64(n) * sfx.mult; err != nil || got != want {
				t.Fatalf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
			}
		}
	})
}

// FuzzVolumeNames checks that DeriveName always yields a name that
// ValidateName accepts (so a derived name is as safe as an explicit one
// as a label and an fstab token), and that ValidateName accepts exactly
// 1 to 16 bytes of [a-z0-9-].
func FuzzVolumeNames(f *testing.F) {
	for _, s := range []string{
		"/data", "/var/lib/app!", "/", "", "/ünï/çødé", "/UPPER/Case", strings.Repeat("/abcdefghij", 5),
		"/!!!", "/a b", "data", "-", "a-b-c", strings.Repeat("a", 16), strings.Repeat("a", 17), "\x00", "a\nb",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		name := DeriveName(s)
		if err := ValidateName(name); err != nil {
			t.Fatalf("DeriveName(%q) = %q is not a valid name: %v", s, name, err)
		}
		if DeriveName(s) != name {
			t.Fatalf("DeriveName(%q) is not deterministic", s)
		}
		valid := s != "" && len(s) <= MaxNameLen
		for i := 0; i < len(s); i++ {
			valid = valid && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= '0' && s[i] <= '9' || s[i] == '-')
		}
		if (ValidateName(s) == nil) != valid {
			t.Fatalf("ValidateName(%q) = %v, want valid = %v", s, ValidateName(s), valid)
		}
		if valid && DeriveName("/"+s) != s && len(s) <= MaxNameLen {
			t.Fatalf("DeriveName(/%s) = %q, want the name back", s, DeriveName("/"+s))
		}
	})
}

// FuzzFromConfig resolves fuzzed VOLUME paths and labels. A success has
// to be sorted by path, hold only absolute control-free paths, valid
// unique names and capped sizes, and not depend on the order the paths
// were declared in; RootSize has to stay within its cap.
func FuzzFromConfig(f *testing.F) {
	f.Add("/data\n/var/lib/app", "io.contemper.volume./data.size=2GiB\nio.contemper.volume./data.name=db")
	f.Add("/data", "io.contemper.volume./data.seed=false")
	f.Add("/data", "io.contemper.volume./data.seed=maybe")
	f.Add("/a/b\n/a-b", "")
	f.Add("data", "")
	f.Add("/ctl\x01", "")
	f.Add("/big", "io.contemper.volume./big.size=2TiB")
	f.Add("/x", "io.contemper.root.size=64GiB")
	f.Add("/x", "io.contemper.root.size=4GiB\nio.contemper.volume./x.name= Bad Name ")
	f.Add("/same\n/same", "")
	f.Fuzz(func(t *testing.T, pathsS, labelsS string) {
		if len(pathsS)+len(labelsS) > 4096 {
			t.Skip()
		}
		paths := strings.Split(pathsS, "\n")
		labels := labelsFrom(labelsS)

		if size, err := RootSize(labels); err == nil && (size < 0 || size > MaxRootSizeLabel) {
			t.Fatalf("RootSize = %d, outside [0, %d]", size, int64(MaxRootSizeLabel))
		}

		specs, err := FromConfig(paths, labels)
		reversed := append([]string(nil), paths...)
		sort.Sort(sort.Reverse(sort.StringSlice(reversed)))
		specs2, err2 := FromConfig(reversed, labels)
		if (err == nil) != (err2 == nil) || !reflect.DeepEqual(specs, specs2) {
			t.Fatalf("result depends on path order: %v / %v", err, err2)
		}
		if err != nil {
			return
		}
		if len(specs) != len(paths) {
			t.Fatalf("%d specs for %d paths", len(specs), len(paths))
		}
		names := map[string]string{}
		for i, s := range specs {
			if i > 0 && specs[i-1].Path > s.Path {
				t.Fatalf("specs are not sorted by path")
			}
			if !strings.HasPrefix(s.Path, "/") || strings.ContainsAny(s.Path, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\x0c\r\x7f") {
				t.Fatalf("bad path %q", s.Path)
			}
			if err := ValidateName(s.Name); err != nil {
				t.Fatalf("volume %q has invalid name: %v", s.Path, err)
			}
			if other, dup := names[s.Name]; dup {
				t.Fatalf("volumes %q and %q share the name %q", other, s.Path, s.Name)
			}
			names[s.Name] = s.Path
			if s.SizeBytes < 0 || s.SizeBytes > MaxVolumeSize {
				t.Fatalf("volume %q has size %d", s.Path, s.SizeBytes)
			}
		}
	})
}
