// Package imgtest builds synthetic in-process OCI images for tests, so
// merge/validate/support logic can be exercised without a registry or a
// real container build.
package imgtest

import (
	"archive/tar"
	"bytes"
	"io"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// File describes one tar entry to place in a synthetic layer.
type File struct {
	Path               string
	Typeflag           byte // defaults to tar.TypeReg
	Mode               int64
	UID, GID           int
	Data               []byte
	Linkname           string
	Devmajor, Devminor int64
	// Xattrs, if non-nil, is written as "SCHILY.xattr.<name>" PAX
	// records, the convention GNU tar and Go's archive/tar use for
	// extended attributes.
	Xattrs map[string]string
	// ModTime, if non-zero, overrides the tar entry's mtime (epoch by
	// default).
	ModTime time.Time
}

var epoch = time.Unix(0, 0)

// Layer builds a v1.Layer containing files, in order.
func Layer(files []File) (v1.Layer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		typeflag := f.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := f.Mode
		if mode == 0 {
			switch typeflag {
			case tar.TypeDir:
				mode = 0o755
			default:
				mode = 0o644
			}
		}
		modTime := f.ModTime
		if modTime.IsZero() {
			modTime = epoch
		}
		hdr := &tar.Header{
			Name:     f.Path,
			Typeflag: typeflag,
			Mode:     mode,
			Uid:      f.UID,
			Gid:      f.GID,
			Linkname: f.Linkname,
			Devmajor: f.Devmajor,
			Devminor: f.Devminor,
			Size:     int64(len(f.Data)),
			ModTime:  modTime,
		}
		if len(f.Xattrs) > 0 {
			hdr.Format = tar.FormatPAX
			hdr.PAXRecords = make(map[string]string, len(f.Xattrs))
			for name, value := range f.Xattrs {
				hdr.PAXRecords["SCHILY.xattr."+name] = value
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if len(f.Data) > 0 {
			if _, err := tw.Write(f.Data); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	// LayerFromReader is deprecated because a plain io.Reader can only be
	// consumed once, which breaks callers that read a layer's contents
	// more than once (e.g. to hash it, then to write it). Opener reopens
	// buf's already-built bytes on every call instead.
	data := buf.Bytes()
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
}

// Image builds a v1.Image for platform with the given labels, from one
// layer per []File in layers (applied bottom to top).
func Image(platform v1.Platform, labels map[string]string, layers ...[]File) (v1.Image, error) {
	img := empty.Image
	for _, lf := range layers {
		l, err := Layer(lf)
		if err != nil {
			return nil, err
		}
		img, err = mutate.AppendLayers(img, l)
		if err != nil {
			return nil, err
		}
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cfg = cfg.DeepCopy()
	cfg.OS = platform.OS
	cfg.Architecture = platform.Architecture
	if cfg.Config.Labels == nil {
		cfg.Config.Labels = map[string]string{}
	}
	for k, v := range labels {
		cfg.Config.Labels[k] = v
	}
	return mutate.ConfigFile(img, cfg)
}

// WithVolumes returns img with the given VOLUME paths declared in its
// config.
func WithVolumes(img v1.Image, paths ...string) (v1.Image, error) {
	if len(paths) == 0 {
		return img, nil
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cfg = cfg.DeepCopy()
	if cfg.Config.Volumes == nil {
		cfg.Config.Volumes = map[string]struct{}{}
	}
	for _, p := range paths {
		cfg.Config.Volumes[p] = struct{}{}
	}
	return mutate.ConfigFile(img, cfg)
}

// WhiteoutFile returns a File that whites out name in a higher layer.
func WhiteoutFile(name string) File {
	dir, base := splitPath(name)
	var p string
	if dir != "" {
		p = dir + "/.wh." + base
	} else {
		p = ".wh." + base
	}
	return File{Path: p, Typeflag: tar.TypeReg}
}

func splitPath(p string) (dir, base string) {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i], p[i+1:]
		}
	}
	return "", p
}
