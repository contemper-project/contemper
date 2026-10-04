package distros

import (
	"strings"
	"testing"
)

func TestLegs(t *testing.T) {
	entries := []Entry{
		{ID: "a", Arch: []string{"amd64", "arm64"}},
		{ID: "b", Arch: []string{"amd64"}},
	}
	tests := []struct {
		ids, arch, want string
		wantErr         string
	}{
		{"", "", "a/amd64 a/arm64 b/amd64", ""},
		{"", "all", "a/amd64 a/arm64 b/amd64", ""},
		{"", "arm64", "a/arm64", ""},
		{"b", "arm64", "", ""},
		{" b , a ", "amd64", "a/amd64 b/amd64", ""},
		{"zzz", "", "", "no matrix entry"},
		{"", "riscv", "", "unknown arch"},
	}
	for _, tt := range tests {
		legs, err := Legs(entries, tt.ids, tt.arch)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Legs(%q,%q) error = %v, want %q", tt.ids, tt.arch, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range legs {
			got = append(got, l.ID+"/"+l.Arch)
		}
		if strings.Join(got, " ") != tt.want {
			t.Errorf("Legs(%q,%q) = %v, want %q", tt.ids, tt.arch, got, tt.want)
		}
	}
}

func TestMatrixJSON(t *testing.T) {
	got, _ := MatrixJSON(nil)
	if got != `{"include":[]}` {
		t.Errorf("empty = %s", got)
	}
	got, _ = MatrixJSON([]Leg{{"a", "amd64"}})
	if got != `{"include":[{"id":"a","arch":"amd64"}]}` {
		t.Errorf("one = %s", got)
	}
}
