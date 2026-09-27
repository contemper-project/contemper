package buildinfo

import "testing"

func TestGetFallsBackToDevWithoutLdflags(t *testing.T) {
	// In `go test`, the ldflags vars are never set, so Get must fall back
	// to runtime/debug.ReadBuildInfo rather than returning an empty Info.
	info := Get()
	if info.Version == "" {
		t.Fatal("Get().Version is empty, want a fallback version")
	}
}

func TestGetPrefersLdflagsValues(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	t.Cleanup(func() { version, commit, date = oldVersion, oldCommit, oldDate })

	version, commit, date = "1.2.3", "abc1234", "2026-09-27T00:00:00Z"

	info := Get()
	if info.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", info.Version, "1.2.3")
	}
	if info.Commit != "abc1234" {
		t.Errorf("Commit = %q, want %q", info.Commit, "abc1234")
	}
	if info.Date != "2026-09-27T00:00:00Z" {
		t.Errorf("Date = %q, want %q", info.Date, "2026-09-27T00:00:00Z")
	}
}

func TestInfoString(t *testing.T) {
	cases := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "version only",
			info: Info{Version: "dev"},
			want: "dev",
		},
		{
			name: "version and commit",
			info: Info{Version: "1.2.3", Commit: "abc1234"},
			want: "1.2.3 (commit abc1234)",
		},
		{
			name: "version and date",
			info: Info{Version: "1.2.3", Date: "2026-09-27T00:00:00Z"},
			want: "1.2.3 (built 2026-09-27T00:00:00Z)",
		},
		{
			name: "version, commit and date",
			info: Info{Version: "1.2.3", Commit: "abc1234", Date: "2026-09-27T00:00:00Z"},
			want: "1.2.3 (commit abc1234, built 2026-09-27T00:00:00Z)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.info.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}
