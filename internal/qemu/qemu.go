// Package qemu implements `deploy --to local-qemu`: booting a contemper
// bundle's disk under a host-discovered qemu-system-* binary with UEFI
// firmware, snapshot=on so the bundle disk stays pristine.
package qemu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/subprocess"
)

// archInfo carries everything about booting a given architecture that
// isn't host-dependent.
type archInfo struct {
	systemBinary     string
	machine          string
	pflashCandidates []firmware
	biosCandidates   []string
}

// firmware is a UEFI code image for pflash, optionally paired with the
// variable-store template it ships with. When vars is set and exists, the
// VM gets a writable copy of it as the second pflash unit, which is what
// the distro OVMF/AAVMF builds expect. Homebrew's edk2 images boot
// code-only.
type firmware struct {
	code string
	vars string
}

var archTable = map[string]archInfo{
	"arm64": {
		systemBinary: "qemu-system-aarch64",
		machine:      "virt",
		pflashCandidates: []firmware{
			{code: "/opt/homebrew/share/qemu/edk2-aarch64-code.fd"},
			{code: "/opt/homebrew/opt/qemu/share/qemu/edk2-aarch64-code.fd"},
			{code: "/usr/local/share/qemu/edk2-aarch64-code.fd"},
			{code: "/usr/local/opt/qemu/share/qemu/edk2-aarch64-code.fd"},
			{code: "/usr/share/AAVMF/AAVMF_CODE.fd", vars: "/usr/share/AAVMF/AAVMF_VARS.fd"},
			{code: "/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw", vars: "/usr/share/edk2/aarch64/vars-template-pflash.raw"},
		},
		// QEMU_EFI.fd is not padded to the 64 MiB pflash size, so it can
		// only be loaded with -bios.
		biosCandidates: []string{
			"/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
		},
	},
	"amd64": {
		systemBinary: "qemu-system-x86_64",
		machine:      "q35",
		pflashCandidates: []firmware{
			{code: "/opt/homebrew/share/qemu/edk2-x86_64-code.fd"},
			{code: "/opt/homebrew/opt/qemu/share/qemu/edk2-x86_64-code.fd"},
			{code: "/usr/local/share/qemu/edk2-x86_64-code.fd"},
			{code: "/usr/local/opt/qemu/share/qemu/edk2-x86_64-code.fd"},
			// Debian/Ubuntu (4M builds are the only ones on Ubuntu 24.04+).
			{code: "/usr/share/OVMF/OVMF_CODE_4M.fd", vars: "/usr/share/OVMF/OVMF_VARS_4M.fd"},
			{code: "/usr/share/OVMF/OVMF_CODE.fd", vars: "/usr/share/OVMF/OVMF_VARS.fd"},
			// Fedora.
			{code: "/usr/share/edk2/ovmf/OVMF_CODE.fd", vars: "/usr/share/edk2/ovmf/OVMF_VARS.fd"},
		},
		// Combined code+vars images, which can't be mapped read-only.
		biosCandidates: []string{
			"/usr/share/ovmf/OVMF.fd",
			"/usr/share/qemu/OVMF.fd",
		},
	},
}

func firstExisting(paths []string) string {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func firstExistingFirmware(fws []firmware) (firmware, bool) {
	for _, fw := range fws {
		if firstExisting([]string{fw.code}) != "" {
			return fw, true
		}
	}
	return firmware{}, false
}

// VolumeAttachment is one declared volume's qcow2 disk file, attached as
// virtio-blk with serial=<name> so the guest can find it under
// /sys/block/*/serial the same way it would on a real provider (see
// target.SerialPattern).
type VolumeAttachment struct {
	Name string
	Path string
}

// Options configures Deploy.
type Options struct {
	Arch          string
	DiskPath      string
	DiskFormat    string // "qcow2" or "raw"
	SerialLogPath string // if empty, a temp file is used
	// WorkDir holds per-boot scratch files (the writable UEFI variable
	// store). If empty, firmware is booted code-only.
	WorkDir string
	// Volumes attaches one virtio-blk drive per entry, in order, after
	// the root disk.
	Volumes []VolumeAttachment
	Expect  string
	Timeout time.Duration
	// Progress, if non-nil, receives a "booting" line, the qemu argv
	// (--verbose only), the live serial console, and a final match/error
	// line.
	Progress *progress.Reporter
}

// BuildArgs returns the qemu-system-* argv (excluding argv[0]) for opts,
// and the path firmware/disk resolution used. Split out from Deploy so
// the argument-building logic can be unit-tested without qemu installed.
func BuildArgs(opts Options) (args []string, err error) {
	info, ok := archTable[opts.Arch]
	if !ok {
		return nil, fmt.Errorf("no qemu configuration for arch %q", opts.Arch)
	}
	return buildArgs(info, opts)
}

// accelInfo picks the acceleration backend for a guest of arch: TCG
// whenever arch is not the host's own (neither HVF nor KVM can run a
// foreign architecture), otherwise HVF on darwin, KVM if /dev/kvm can be
// opened (existing isn't enough: CI runners often have the device
// without granting access to it), TCG otherwise.
func accelInfo(arch string) (accel, cpu string) {
	switch {
	case arch != runtime.GOARCH:
		return "tcg", "max"
	case runtime.GOOS == "darwin":
		return "hvf", "host"
	case kvmUsable():
		return "kvm", "host"
	default:
		return "tcg", "max"
	}
}

func kvmUsable() bool {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func buildArgs(info archInfo, opts Options) (args []string, err error) {
	args = append(args, "-M", info.machine)

	accel, cpu := accelInfo(opts.Arch)
	args = append(args, "-accel", accel, "-cpu", cpu)

	args = append(args, "-m", "1G", "-smp", "2")

	if fw, ok := firstExistingFirmware(info.pflashCandidates); ok {
		args = append(args, "-drive", fmt.Sprintf("if=pflash,format=raw,unit=0,readonly=on,file=%s", optValue(fw.code)))
		if fw.vars != "" && opts.WorkDir != "" && firstExisting([]string{fw.vars}) != "" {
			vars := filepath.Join(opts.WorkDir, "efivars.fd")
			if err := copyFile(fw.vars, vars); err != nil {
				return nil, fmt.Errorf("copying UEFI variable store: %w", err)
			}
			args = append(args, "-drive", fmt.Sprintf("if=pflash,format=raw,unit=1,file=%s", optValue(vars)))
		}
	} else if bios := firstExisting(info.biosCandidates); bios != "" {
		args = append(args, "-bios", bios)
	} else {
		return nil, fmt.Errorf("no UEFI firmware found for %s; install qemu (brew install qemu) or the appropriate *-efi/OVMF package", opts.Arch)
	}

	format := opts.DiskFormat
	if format == "" {
		format = "qcow2"
	}
	if format != "qcow2" && format != "raw" {
		return nil, fmt.Errorf("unsupported disk format %q (want qcow2 or raw)", format)
	}
	args = append(args, "-drive", fmt.Sprintf("file=%s,if=virtio,format=%s,snapshot=on", optValue(opts.DiskPath), format))
	// serial= is a property of the virtio-blk device, not of -drive
	// (current QEMU rejects it there), so each volume is a backend-only
	// drive plus an explicit device carrying the serial.
	for i, v := range opts.Volumes {
		id := fmt.Sprintf("vol%d", i)
		args = append(args,
			"-drive", fmt.Sprintf("file=%s,if=none,id=%s,format=qcow2", optValue(v.Path), id),
			"-device", fmt.Sprintf("virtio-blk-pci,drive=%s,serial=%s", id, optValue(v.Name)))
	}
	args = append(args, "-netdev", "user,id=net0", "-device", "virtio-net-pci,netdev=net0")
	args = append(args, "-nographic", "-monitor", "none", "-serial", "file:"+opts.SerialLogPath)

	return args, nil
}

// optValue escapes s for use as a value in a qemu option string such as
// -drive's, where a comma separates options and a literal comma is
// written as two. Without this, a comma in a path (a bundle directory
// named after an image, say) would end the value and start a new option.
func optValue(s string) string {
	return strings.ReplaceAll(s, ",", ",,")
}

// copyFile copies the UEFI variable store template (src, one of a fixed
// list of firmware paths - see pflashCandidates) into this boot's
// per-instance work dir (dst) as a private scratch file only this
// process and the qemu child it spawns need to read.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600) //nolint:gosec // G703: dst is always opts.WorkDir (a dir we created) joined with a fixed filename, never user input
}

// Deploy boots the disk described by opts under qemu. With opts.Expect
// set, it returns nil as soon as that string appears on the serial
// console (killing qemu), or an error (with the log tail) on timeout.
// Without it, Deploy waits for qemu to exit on its own.
//
// Canceling ctx stops qemu (SIGTERM, then a kill if it hasn't exited
// after a short grace period - see internal/subprocess) and, either way,
// Deploy's own temp files (a generated serial log or qemu work dir; see
// opts.SerialLogPath and opts.WorkDir) are still removed before it
// returns, the same as on any other error. Unless the expected output
// had already appeared, Deploy then returns ctx's error, even if qemu
// itself exited cleanly. Run from a terminal, Ctrl-C
// also reaches qemu directly (it shares contemper's foreground process
// group), which is how a user normally stops a deploy; ctx only matters
// on its own when contemper is stopped some other way (SIGTERM, or no
// controlling terminal).
func Deploy(ctx context.Context, opts Options) error {
	rep := opts.Progress

	info, ok := archTable[opts.Arch]
	if !ok {
		rep.Fail("deploy", fmt.Sprintf("no qemu configuration for arch %q", opts.Arch), "")
		return fmt.Errorf("no qemu configuration for arch %q", opts.Arch)
	}
	binPath, err := hostenv.Required(info.systemBinary)
	if err != nil {
		rep.Fail("deploy", err.Error(), "")
		return err
	}

	if opts.SerialLogPath == "" {
		f, err := os.CreateTemp("", "contemper-serial-*.log")
		if err != nil {
			return fmt.Errorf("creating serial log: %w", err)
		}
		opts.SerialLogPath = f.Name()
		_ = f.Close()
		// Nothing reports this path to the user, so it is scratch: the
		// console has already been streamed to the progress output.
		defer func() { _ = os.Remove(f.Name()) }()
	}

	if opts.WorkDir == "" {
		dir, err := os.MkdirTemp("", "contemper-qemu-*")
		if err != nil {
			return fmt.Errorf("creating qemu work dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		opts.WorkDir = dir
	}

	args, err := BuildArgs(opts)
	if err != nil {
		rep.Fail("deploy", err.Error(), "")
		return err
	}

	accel, _ := accelInfo(opts.Arch)
	rep.Line("🚀", "booting "+filepath.Base(opts.DiskPath), fmt.Sprintf("UEFI/%s · 1 GiB", strings.ToUpper(accel)))
	rep.VerboseCmd(binPath, args)

	cmd := subprocess.Command(ctx, binPath, args...)
	var toolErr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &toolErr)
	if err := cmd.Start(); err != nil {
		rep.Fail("deploy", fmt.Sprintf("starting %s failed", binPath), err.Error())
		return fmt.Errorf("starting %s: %w", binPath, err)
	}

	stopTail := tailToWriter(opts.SerialLogPath, rep.Writer())
	defer stopTail()

	if opts.Expect == "" {
		err := cmd.Wait()
		// Stopping qemu the usual way, with Ctrl-C on the terminal, also
		// interrupts contemper and cancels ctx; qemu may still exit 0
		// on its own before ctx's SIGTERM reaches it, so report the
		// interruption rather than a clean exit (or qemu's own error).
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			rep.Fail("deploy", "qemu exited with an error", toolErr.String())
		}
		return err
	}

	// exited is closed once QEMU has exited and been reaped; exitErr is
	// its result, safe to read after that.
	exited := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(exited) }()
	waitErr := waitForExpect(opts.SerialLogPath, opts.Expect, opts.Timeout, exited, func() error { return exitErr })
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	// Reap QEMU before returning: until it has exited it still holds its
	// disk images' locks, and an immediate redeploy would fail.
	<-exited
	stopTail()
	if ctxErr := ctx.Err(); waitErr != nil && ctxErr != nil {
		return ctxErr
	}
	if errors.Is(waitErr, errExitedEarly) {
		rep.Fail("deploy", waitErr.Error(), toolErr.String())
		return waitErr
	}
	if waitErr != nil {
		rep.Fail("deploy", waitErr.Error(), "")
		return waitErr
	}
	rep.Finish("matched "+strconv.Quote(opts.Expect), "on the serial console")
	return nil
}

// tailToWriter streams appended bytes from the file at path to w as they
// are written (best-effort; a missing or not-yet-created file is
// retried), until the returned stop function is called.
func tailToWriter(path string, w io.Writer) (stop func()) {
	done := make(chan struct{})
	go func() {
		var offset int64
		for {
			select {
			case <-done:
				return
			default:
			}
			f, err := os.Open(path)
			if err == nil {
				if _, err := f.Seek(offset, io.SeekStart); err == nil {
					n, _ := io.Copy(w, f)
					offset += n
				}
				_ = f.Close()
			}
			select {
			case <-done:
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// errExitedEarly reports that QEMU exited before the expected output
// appeared, so waiting out the timeout would be pointless.
var errExitedEarly = errors.New("qemu exited before the expected output appeared")

// waitForExpect polls the serial log for expect until it appears, the
// timeout passes, or QEMU exits (exited is closed; exitErr then returns
// its result).
func waitForExpect(logPath, expect string, timeout time.Duration, exited <-chan struct{}, exitErr func() error) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		data, _ := os.ReadFile(logPath)
		if bytes.Contains(data, []byte(expect)) {
			return nil
		}
		select {
		case <-exited:
			err := exitErr()
			// One last look: the match may have been written just
			// before QEMU exited.
			data, _ = os.ReadFile(logPath)
			if bytes.Contains(data, []byte(expect)) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("%w (%w) waiting for %q", errExitedEarly, err, expect)
			}
			return fmt.Errorf("%w waiting for %q", errExitedEarly, expect)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %q on the serial console; log tail:\n%s",
				timeout, expect, tail(data, 4000))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func tail(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	return string(data[len(data)-n:])
}
