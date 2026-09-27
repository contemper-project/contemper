package main

import "testing"

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
