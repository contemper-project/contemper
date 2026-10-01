package main

import (
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/volume"
)

func TestSeedReportLine(t *testing.T) {
	cases := []struct {
		name       string
		spec       volume.Spec
		hasContent bool
		substr     string
	}{
		{"opted out wins over content", volume.Spec{Path: "/data", Seed: false}, true, "opted out"},
		{"seeded", volume.Spec{Path: "/data", Seed: true}, true, "copied onto the volume"},
		{"nothing to seed", volume.Spec{Path: "/data", Seed: true}, false, "no content"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := seedReportLine(c.spec, c.hasContent)
			if !strings.Contains(got, c.substr) {
				t.Errorf("seedReportLine(%+v, %v) = %q, want it to contain %q", c.spec, c.hasContent, got, c.substr)
			}
			if !strings.Contains(got, c.spec.Path) {
				t.Errorf("seedReportLine(%+v, %v) = %q, want it to name the path", c.spec, c.hasContent, got)
			}
		})
	}
}

func TestNoSeedVolumeNamesNilWhenNothingOptsOut(t *testing.T) {
	specs := []volume.Spec{{Path: "/data", Name: "data", Seed: true}, {Path: "/logs", Name: "logs", Seed: true}}
	if got := noSeedVolumeNames(specs); got != nil {
		t.Errorf("noSeedVolumeNames = %v, want nil", got)
	}
}

func TestNoSeedVolumeNamesPicksOutOptedOutOnes(t *testing.T) {
	specs := []volume.Spec{
		{Path: "/data", Name: "data", Seed: true},
		{Path: "/logs", Name: "logs", Seed: false},
		{Path: "/cache", Name: "cache", Seed: false},
	}
	got := noSeedVolumeNames(specs)
	want := []string{"logs", "cache"}
	if len(got) != len(want) {
		t.Fatalf("noSeedVolumeNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("noSeedVolumeNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRedactedVariantRef(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		"ghcr.io/example/support-openrc:v1":  "ghcr.io/example/support-openrc:v1",
		"oci-archive:/home/user/build/v.tar": "oci-archive:v.tar",
		"oci:/home/user/layouts/openrc":      "oci:openrc",
		"docker-archive:/tmp/x/variant.tar":  "docker-archive:variant.tar",
	}
	for in, want := range cases {
		if got := redactedVariantRef(in); got != want {
			t.Errorf("redactedVariantRef(%q) = %q, want %q", in, got, want)
		}
	}
}
