// Package limits bounds how much content contemper accepts from an image
// while merging its layers and unpacking archives. Image content is
// untrusted: a tar header may claim a huge (sparse) file that a few
// kilobytes of layer expand into, and without a bound that fills the
// temporary directory. The defaults are far above any real image; the
// trusted --max-rootfs-size flag raises them.
package limits

import (
	"context"
	"fmt"

	"github.com/contemper-project/contemper/internal/progress"
)

// Limits caps the content read from one image or archive.
type Limits struct {
	// MaxFileSize is the largest size one entry may claim.
	MaxFileSize int64
	// MaxTotalSize is the largest sum of all entry sizes.
	MaxTotalSize int64
	// MaxEntries is the largest number of tar entries.
	MaxEntries int
}

// Default returns the built-in limits. A 64 GiB total is larger than any
// root filesystem contemper is asked to build (the root partition itself
// defaults to a few GiB), 32 GiB for one file covers the largest
// plausible single artifact (a model or database file), and four million
// entries is several times a full distribution's file count (a desktop
// install has well under one million).
func Default() Limits {
	return Limits{
		MaxFileSize:  32 << 30,
		MaxTotalSize: 64 << 30,
		MaxEntries:   4_000_000,
	}
}

// WithMaxSize returns l with the total and per-file size limits both set
// to n, which is what --max-rootfs-size does.
func (l Limits) WithMaxSize(n int64) Limits {
	l.MaxTotalSize, l.MaxFileSize = n, n
	return l
}

type ctxKey struct{}

// NewContext returns a context carrying l.
func NewContext(ctx context.Context, l Limits) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the limits in ctx, or Default if there are none.
func FromContext(ctx context.Context) Limits {
	if l, ok := ctx.Value(ctxKey{}).(Limits); ok {
		return l
	}
	return Default()
}

// Counter tracks the entries and sizes seen so far against a Limits.
type Counter struct {
	l       Limits
	entries int
	total   int64
}

// NewCounter returns a Counter enforcing l.
func NewCounter(l Limits) *Counter { return &Counter{l: l} }

const hint = "; a larger image needs --max-rootfs-size"

// Check records one tar entry named name that claims size bytes of
// content, and returns an error naming the entry and the limit it breaks.
// Call it on the header, before reading or writing any content.
func (c *Counter) Check(name string, size int64) error {
	c.entries++
	if c.entries > c.l.MaxEntries {
		return fmt.Errorf("image has more than %d entries (at %q)%s", c.l.MaxEntries, name, hint)
	}
	if size < 0 {
		return fmt.Errorf("entry %q has an invalid size %d", name, size)
	}
	if size > c.l.MaxFileSize {
		return fmt.Errorf("entry %q claims %s, over the per-file limit of %s%s", name, progress.HumanBytes(size), progress.HumanBytes(c.l.MaxFileSize), hint)
	}
	c.total += size
	if c.total > c.l.MaxTotalSize {
		return fmt.Errorf("image content exceeds the total limit of %s (at %q)%s", progress.HumanBytes(c.l.MaxTotalSize), name, hint)
	}
	return nil
}
