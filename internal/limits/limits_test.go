package limits

import (
	"context"
	"strings"
	"testing"
)

func TestCounter(t *testing.T) {
	l := Limits{MaxFileSize: 100, MaxTotalSize: 150, MaxEntries: 3}
	cases := []struct {
		name    string
		sizes   []int64
		wantErr string
	}{
		{"ok", []int64{100, 50}, ""},
		{"file", []int64{101}, "per-file limit"},
		{"total", []int64{100, 51}, "total limit"},
		{"entries", []int64{0, 0, 0, 0}, "more than 3 entries"},
		{"negative", []int64{-1}, "invalid size"},
	}
	for _, c := range cases {
		cnt := NewCounter(l)
		var err error
		for _, s := range c.sizes {
			if err = cnt.Check("e", s); err != nil {
				break
			}
		}
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr) || (c.name != "negative" && !strings.Contains(err.Error(), "--max-rootfs-size"))) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
}

func TestContext(t *testing.T) {
	if got := FromContext(context.Background()); got != Default() {
		t.Errorf("default = %+v", got)
	}
	l := Default().WithMaxSize(5)
	if got := FromContext(NewContext(context.Background(), l)); got.MaxTotalSize != 5 || got.MaxFileSize != 5 {
		t.Errorf("got %+v", got)
	}
}
