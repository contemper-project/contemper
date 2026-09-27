// Package bundle writes contemper.json, the manifest that accompanies a
// bundle's disk file. Fields follow the design handover's "contemper
// bundle" section (HANDOVER.md §5).
package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FormatVersion is the current contemper.json schema version.
const FormatVersion = 1

// ImageRef identifies a source image by reference and digest.
type ImageRef struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// SupportRef identifies the support image that was merged in, and where
// its reference came from: "target" when the target's own default
// applied, or "flag" when --support replaced it.
type SupportRef struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
	Origin string `json:"origin"`
}

// DiskInfo describes the bundle's disk file.
type DiskInfo struct {
	File      string `json:"file"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

// Healthcheck mirrors the subset of an OCI HEALTHCHECK contemper carries
// through as a deployment hint, in human-readable durations.
type Healthcheck struct {
	Test        []string `json:"test,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	StartPeriod string   `json:"startPeriod,omitempty"`
	Retries     int      `json:"retries,omitempty"`
}

// Hints carries deployment-facing metadata that isn't used to assemble
// the disk itself: EXPOSEd ports and a HEALTHCHECK, if the source image
// declared them.
type Hints struct {
	ExposedPorts []string     `json:"exposedPorts,omitempty"`
	Healthcheck  *Healthcheck `json:"healthcheck,omitempty"`
}

// SupportVariant records one branch's resolved variant: which variant won,
// and (unless it was a no-op) the image its layers came from.
type SupportVariant struct {
	Branch  string `json:"branch"`
	Variant string `json:"variant"`
	Ref     string `json:"ref,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// Manifest is the top-level contemper.json document.
type Manifest struct {
	FormatVersion    int              `json:"formatVersion"`
	ContemperVersion string           `json:"contemperVersion"`
	CreatedAt        time.Time        `json:"createdAt"`
	Source           ImageRef         `json:"source"`
	Support          *SupportRef      `json:"support,omitempty"`
	SupportVariants  []SupportVariant `json:"support.variants,omitempty"`
	Target           string           `json:"target"`
	Arch             string           `json:"arch"`
	Disk             DiskInfo         `json:"disk"`
	Volumes          []string         `json:"volumes,omitempty"`
	Hints            Hints            `json:"hints"`
	Reproducible     bool             `json:"reproducible"`
}

// Read parses <dir>/contemper.json.
func Read(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "contemper.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &m, nil
}

// Write marshals m as indented JSON to <dir>/contemper.json.
func Write(dir string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling contemper.json: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, "contemper.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// SortedKeys returns the sorted keys of a string-set map, the shape
// v1.Config uses for Volumes and ExposedPorts.
func SortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
