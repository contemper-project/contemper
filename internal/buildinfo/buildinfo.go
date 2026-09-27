// Package buildinfo reports the contemper version, commit and build date.
//
// A release build injects version, commit and date via
// -ldflags "-X .../internal/buildinfo.version=... -X .../internal/buildinfo.commit=... -X .../internal/buildinfo.date=..."
// (see .goreleaser.yaml). A plain `go build` or `go install` leaves them
// empty; Get then falls back to runtime/debug.ReadBuildInfo, which the Go
// toolchain fills in on its own from the module version and VCS metadata.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Set at build time via -ldflags -X; see the package doc comment.
var (
	version string
	commit  string
	date    string
)

// Info is a build's resolved version, commit and date. Commit and Date may
// be empty when neither -ldflags nor VCS metadata provided them (for
// example a `go build` outside a git checkout, or with VCS stamping
// disabled).
type Info struct {
	Version string
	Commit  string
	Date    string
}

// Get returns the running binary's build information. It prefers values
// injected at build time via -ldflags; when those are absent (a plain `go
// build` or `go install`) it falls back to the module version and VCS
// stamp that runtime/debug.ReadBuildInfo reports.
func Get() Info {
	if version != "" {
		return Info{Version: version, Commit: commit, Date: date}
	}

	info := Info{Version: "dev"}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}

	var revision string
	var modified bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		case "vcs.time":
			info.Date = s.Value
		}
	}
	if revision != "" {
		if modified {
			revision += "-dirty"
		}
		info.Commit = revision
	}

	return info
}

// String renders the info the way `contemper version` prints it, e.g.
// "0.3.0 (commit abcdef1, built 2026-09-27T12:00:00Z)". Commit and/or date
// are omitted when unknown.
func (i Info) String() string {
	s := i.Version
	switch {
	case i.Commit != "" && i.Date != "":
		s += fmt.Sprintf(" (commit %s, built %s)", i.Commit, i.Date)
	case i.Commit != "":
		s += fmt.Sprintf(" (commit %s)", i.Commit)
	case i.Date != "":
		s += fmt.Sprintf(" (built %s)", i.Date)
	}
	return s
}
