// Package distros describes the distribution test matrix: the entries in
// matrix.json, each naming a test image (test/distros/<containerfile>)
// and the base image and build arguments it is built with.
package distros

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed matrix.json
var embedded []byte

// Entry is one row of the matrix.
type Entry struct {
	// ID names the entry, for example "fedora-44".
	ID string `json:"id"`
	// Kind says what the entry tests. "target" entries convert and boot
	// an image of the distribution.
	Kind string `json:"kind"`
	// Family groups entries that share packaging and boot tooling.
	Family string `json:"family"`
	// Containerfile is the directory under test/distros holding the
	// Containerfile that builds the entry.
	Containerfile string `json:"containerfile"`
	// Base is the base image reference, passed as the BASE build arg.
	Base string `json:"base"`
	// BuildArgs are further build args for the Containerfile.
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	// Arch lists the architectures the entry is tested on.
	Arch []string `json:"arch"`
	// Since is the contemper release the entry first ships in.
	Since string `json:"since"`
	// Notes is free text for the docs.
	Notes string `json:"notes,omitempty"`
}

// Kinds and Arches list the values the matrix accepts.
var (
	Kinds  = []string{"target"}
	Arches = []string{"amd64", "arm64"}
)

// Parse decodes matrix JSON, rejecting unknown fields.
func Parse(data []byte) ([]Entry, error) {
	var entries []Entry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("parsing matrix: %w", err)
	}
	return entries, nil
}

// Load returns the entries of the embedded matrix.json.
func Load() ([]Entry, error) { return Parse(embedded) }

// Find returns the entry with the given ID.
func Find(entries []Entry, id string) (Entry, error) {
	var ids []string
	for _, e := range entries {
		if e.ID == id {
			return e, nil
		}
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return Entry{}, fmt.Errorf("no matrix entry %q (have %v)", id, ids)
}

// MirrorRef rewrites a Docker Hub image reference to go through the
// registry mirror, for example "alpine:3.24" with "mirror.gcr.io" becomes
// "mirror.gcr.io/library/alpine:3.24". References to other registries
// (quay.io/..., registry.opensuse.org/..., localhost:5000/...) and an empty
// mirror leave ref unchanged.
func MirrorRef(ref, mirror string) string {
	mirror = strings.TrimSuffix(mirror, "/")
	if mirror == "" {
		return ref
	}
	for _, hub := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, hub)
	}
	first, _, hasSlash := strings.Cut(ref, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return ref // another registry
	}
	if !hasSlash {
		return mirror + "/library/" + ref
	}
	return mirror + "/" + ref
}
