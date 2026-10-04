package distros

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Leg is one entry on one architecture: a single boot test.
type Leg struct {
	ID   string `json:"id"`
	Arch string `json:"arch"`
}

// Legs returns the entry/architecture pairs to run, in matrix order.
// ids is a comma-separated list of entry IDs (empty means every entry) and
// arch is "amd64", "arm64" or "all"/"" for both. An ID that names no entry,
// or an unknown arch, is an error, so a typo doesn't silently select
// nothing.
func Legs(entries []Entry, ids, arch string) ([]Leg, error) {
	want := map[string]bool{}
	for _, id := range strings.Split(ids, ",") {
		if id = strings.TrimSpace(id); id != "" {
			if _, err := Find(entries, id); err != nil {
				return nil, err
			}
			want[id] = true
		}
	}
	switch arch {
	case "", "all":
		arch = ""
	case "amd64", "arm64":
	default:
		return nil, fmt.Errorf("unknown arch %q (want amd64, arm64 or all)", arch)
	}
	legs := []Leg{}
	for _, e := range entries {
		if len(want) > 0 && !want[e.ID] {
			continue
		}
		for _, a := range e.Arch {
			if arch == "" || a == arch {
				legs = append(legs, Leg{ID: e.ID, Arch: a})
			}
		}
	}
	return legs, nil
}

// MatrixJSON renders legs as a GitHub Actions matrix, {"include":[...]}.
func MatrixJSON(legs []Leg) (string, error) {
	if legs == nil {
		legs = []Leg{}
	}
	b, err := json.Marshal(struct {
		Include []Leg `json:"include"`
	}{legs})
	return string(b), err
}

// Result is the outcome of one leg, written by the workflow and read by
// the report tool.
type Result struct {
	ID   string `json:"id"`
	Arch string `json:"arch"`
	// Outcome is "pass" or "fail".
	Outcome string `json:"outcome"`
	// Stage is where a failing run stopped: "build", "convert" or "boot".
	Stage string `json:"stage,omitempty"`
	// BaseDigest is the resolved digest of the base image, if known.
	BaseDigest string `json:"baseDigest,omitempty"`
	RunURL     string `json:"runUrl,omitempty"`
}
