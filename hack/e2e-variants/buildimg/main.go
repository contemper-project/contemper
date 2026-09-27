// Command buildimg pushes a small synthetic OCI image straight to a
// registry, for hack/e2e-variants.sh: a dummy support image (or one of
// its variants) built from a handful of files under
// hack/e2e-variants/files/ plus a set of manifest annotations.
//
// It exists because setting OCI manifest-level annotations reliably
// through `podman build`/`docker buildx` differs enough between the two
// engines (and their versions) to be a poor fit for a portable e2e
// script. This tool instead builds the image directly with
// go-containerregistry - the same library contemper itself uses to read
// it back - so the annotations land exactly where
// docs/reference/support-image-annotations.md says they're read from:
// the platform manifest.
//
// It reuses internal/imgtest, the same synthetic-image builder the
// support package's own tests use, so the images this tool produces are
// built the same way as the ones already exercised in CI's `go test`.
//
// Usage:
//
//	buildimg -ref localhost:5555/repo:tag [-arch amd64] \
//	    [-file /dest/path=local/file]... [-exec /dest/path=local/file]... \
//	    [-annotation key=value]...
//
// -file copies local/file's content to /dest/path in the image's one
// layer with mode 0644; -exec does the same with mode 0755. Either may
// be repeated; an image with none of them has no layers at all (used for
// the annotation-only negative-case support images).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/contemper-project/contemper/internal/imgtest"
)

// repeatedFlag collects every occurrence of a flag given multiple times.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "buildimg:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		ref         string
		arch        string
		files       repeatedFlag
		execFiles   repeatedFlag
		annotations repeatedFlag
	)
	flag.StringVar(&ref, "ref", "", "registry reference to push to, e.g. localhost:5555/repo:tag")
	flag.StringVar(&arch, "arch", "amd64", "image config architecture (GOARCH form: amd64 or arm64)")
	flag.Var(&files, "file", "dest=localfile, mode 0644, repeatable")
	flag.Var(&execFiles, "exec", "dest=localfile, mode 0755, repeatable")
	flag.Var(&annotations, "annotation", "key=value manifest annotation, repeatable")
	flag.Parse()

	if ref == "" {
		return fmt.Errorf("-ref is required")
	}

	var fileEntries []imgtest.File
	for _, spec := range files {
		f, err := readFile(spec, 0o644)
		if err != nil {
			return err
		}
		fileEntries = append(fileEntries, f)
	}
	for _, spec := range execFiles {
		f, err := readFile(spec, 0o755)
		if err != nil {
			return err
		}
		fileEntries = append(fileEntries, f)
	}

	var layers [][]imgtest.File
	if len(fileEntries) > 0 {
		layers = append(layers, fileEntries)
	}

	platform := v1.Platform{OS: "linux", Architecture: arch}
	img, err := imgtest.Image(platform, nil, layers...)
	if err != nil {
		return fmt.Errorf("building image: %w", err)
	}

	if len(annotations) > 0 {
		annMap := make(map[string]string, len(annotations))
		for _, kv := range annotations {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("-annotation %q: want key=value", kv)
			}
			annMap[k] = v
		}
		img = mutate.Annotations(img, annMap).(v1.Image)
	}

	nref, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("parsing -ref %q: %w", ref, err)
	}
	if err := remote.Write(nref, img, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
		return fmt.Errorf("pushing %s: %w", ref, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return fmt.Errorf("reading digest: %w", err)
	}
	fmt.Printf("pushed %s@%s (%d layer(s), %d annotation(s))\n", ref, digest, len(layers), len(annotations))
	return nil
}

// readFile turns a "dest=localfile" spec into an imgtest.File with mode,
// reading localfile's content from disk.
func readFile(spec string, mode int64) (imgtest.File, error) {
	dest, local, ok := strings.Cut(spec, "=")
	if !ok {
		return imgtest.File{}, fmt.Errorf("%q: want dest=localfile", spec)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		return imgtest.File{}, fmt.Errorf("reading %q: %w", local, err)
	}
	return imgtest.File{
		Path: strings.TrimPrefix(dest, "/"),
		Mode: mode,
		Data: data,
	}, nil
}
