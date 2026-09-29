package main

import (
	"errors"
	"os"
	"slices"
	"syscall"
	"testing"
)

func TestExitStatus(t *testing.T) {
	failed := errors.New("mkfs.ext4: context canceled")
	tests := []struct {
		name     string
		err      error
		sig      os.Signal
		wantCode int
		wantMsg  string
	}{
		{"success", nil, nil, 0, ""},
		{"error", failed, nil, 1, "contemper: mkfs.ext4: context canceled"},
		{"interrupted by SIGINT", failed, os.Interrupt, 130, "contemper: interrupted"},
		{"interrupted by SIGTERM", failed, syscall.SIGTERM, 143, "contemper: interrupted"},
		// The command had already finished: the signal interrupted
		// nothing, and its result stands.
		{"signal after success", nil, os.Interrupt, 0, ""},
		{"SIGTERM after success", nil, syscall.SIGTERM, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := exitStatus(tt.err, tt.sig)
			if code != tt.wantCode || msg != tt.wantMsg {
				t.Errorf("exitStatus(%v, %v) = %d, %q; want %d, %q", tt.err, tt.sig, code, msg, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

func TestInterruptSignalsKeepsInheritedIgnores(t *testing.T) {
	tests := []struct {
		name    string
		ignored []os.Signal
		want    []os.Signal
	}{
		{"none ignored", nil, []os.Signal{os.Interrupt, syscall.SIGTERM}},
		// A background job started from a script: the shell ignores
		// SIGINT for it.
		{"SIGINT ignored", []os.Signal{os.Interrupt}, []os.Signal{syscall.SIGTERM}},
		{"SIGTERM ignored", []os.Signal{syscall.SIGTERM}, []os.Signal{os.Interrupt}},
		{"both ignored", []os.Signal{os.Interrupt, syscall.SIGTERM}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := interruptSignals(func(sig os.Signal) bool { return slices.Contains(tt.ignored, sig) })
			if !slices.Equal(got, tt.want) {
				t.Errorf("interruptSignals() = %v, want %v", got, tt.want)
			}
		})
	}
}
