// Package disk builds the on-disk artifacts of a contemper bundle: the
// ext4 root filesystem (via mkfs.ext4 + debugfs, never by extracting
// image content to the host filesystem under its real name), the FAT32
// ESP (via go-diskfs), the GPT layout tying them together, and the
// qcow2 conversion.
package disk

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/rootfs"
	"github.com/contemper-project/contemper/internal/subprocess"
)

// debugfs error markers: debugfs exits 0 even when an individual scripted
// command fails, so a failure is detected by scanning its output for one
// of these (found empirically, by exercising debugfs against the error
// cases below) - and, authoritatively, by running e2fsck -fn on the
// result afterward.
var debugfsErrorMarkers = []string{
	"File not found",
	"not found by ext2_lookup",
	"Unbalanced quotes",
	"Usage:",
	"Could not allocate",
	"already exists",
	"Filename too long",
	"ea_set:",
	"No free space in the directory",
}

// maxScriptLine is the longest debugfs script line, in bytes and
// excluding the newline, that PopulateExt4 will emit. debugfs reads its
// -f script with fgets into a BUFSIZ buffer, so a longer line is split
// and the remainder is parsed as a separate command. BUFSIZ is 8192 with
// glibc but 1024 with musl and on macOS, so the limit is set for the
// smallest: 1024 minus the newline and the terminating NUL.
const maxScriptLine = 1022

// mke2fsConfigEnv names the environment variable that points mke2fs at
// its configuration file, replacing the host's /etc/mke2fs.conf.
const mke2fsConfigEnv = "MKE2FS_CONFIG"

// mke2fsConf is the configuration mkfs.ext4 runs with, so that the root
// filesystem does not depend on the e2fsprogs version (or distribution
// patches) of the machine doing the conversion. e2fsprogs 1.47 turned on
// orphan_file and metadata_csum_seed by default, and an older e2fsck in a
// guest's initrd (1.46.x on Enterprise Linux 9 and Ubuntu 22.04) refuses
// such a filesystem at boot. The feature set below is the long-standing
// ext4 default, readable by e2fsck 1.45.5 and by Linux 4.18 and later:
// uninit_bg is left out because metadata_csum supersedes it. Block size,
// inode size and inode ratio are fixed for every filesystem size (no
// size-type overrides; small and floppy are defined only so mke2fs does
// not warn about them), so output is identical across hosts.
const mke2fsConf = `[defaults]
	base_features = sparse_super,large_file,filetype,resize_inode,dir_index,ext_attr
	default_mntopts = acl,user_xattr
	enable_periodic_fsck = 0
	blocksize = 4096
	inode_size = 256
	inode_ratio = 16384

[fs_types]
	ext4 = {
		features = has_journal,extent,huge_file,flex_bg,metadata_csum,64bit,dir_nlink,extra_isize
	}
	small = {
		blocksize = 4096
	}
	floppy = {
		blocksize = 4096
	}
`

// Ext4Options configures PopulateExt4.
type Ext4Options struct {
	Label     string
	SizeBytes int64
	// Progress, if non-nil, receives --verbose host-tool argv lines.
	Progress *progress.Reporter
	// Stage, if non-nil, receives the live phase and progress readout
	// (staging files, writing and checking the filesystem) in TTY
	// progress mode.
	Stage *progress.Stage
}

// PopulateExt4 creates an ext4 image at imgPath, sized and labeled per
// opts, and populates it from rfs. It returns non-fatal warnings (e.g.
// unsupported tar entry types). Canceling ctx stops the mkfs.ext4/debugfs/
// e2fsck subprocess currently running (see internal/subprocess) and, for
// the (potentially long) debugfs-script build step in between, is
// noticed at reasonable points without waiting for it to finish.
func PopulateExt4(ctx context.Context, rfs *rootfs.Rootfs, imgPath string, opts Ext4Options) ([]string, error) {
	mkfsPath, err := hostenv.Required("mkfs.ext4")
	if err != nil {
		return nil, err
	}
	debugfsPath, err := hostenv.Required("debugfs")
	if err != nil {
		return nil, err
	}
	e2fsckPath, err := hostenv.Required("e2fsck")
	if err != nil {
		return nil, err
	}

	// debugfs runs with payloadDir as its working directory (see
	// below), so the image path must not depend on the caller's.
	imgPath, err = filepath.Abs(imgPath)
	if err != nil {
		return nil, fmt.Errorf("resolving image path: %w", err)
	}

	f, err := os.Create(imgPath)
	if err != nil {
		return nil, fmt.Errorf("creating %s: %w", imgPath, err)
	}
	if err := f.Truncate(opts.SizeBytes); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sizing %s to %d bytes: %w", imgPath, opts.SizeBytes, err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	payloadDir, err := os.MkdirTemp("", "contemper-ext4-payload-")
	if err != nil {
		return nil, fmt.Errorf("creating payload dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(payloadDir) }()

	confPath := path.Join(payloadDir, "mke2fs.conf")
	if err := os.WriteFile(confPath, []byte(mke2fsConf), 0o600); err != nil {
		return nil, fmt.Errorf("writing mke2fs.conf: %w", err)
	}
	opts.Stage.SetPhase("creating ext4")
	mkfsArgs := []string{"-F", "-L", opts.Label, "-E", "root_owner=0:0", imgPath}
	opts.Progress.VerboseCmd(mkfsPath, mkfsArgs)
	if out, err := runCmdEnv(ctx, "", []string{mke2fsConfigEnv + "=" + confPath}, mkfsPath, mkfsArgs...); err != nil {
		return nil, fmt.Errorf("mkfs.ext4: %w\n%s", err, out)
	}

	opts.Stage.SetPhase("staging")
	script, warnings, err := buildDebugfsScript(ctx, rfs, payloadDir, opts.Stage)
	if err != nil {
		return nil, err
	}
	if err := checkScriptLines(script); err != nil {
		return nil, err
	}

	scriptPath := path.Join(payloadDir, "script.debugfs")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return nil, fmt.Errorf("writing debugfs script: %w", err)
	}

	opts.Stage.SetPhase("writing ext4")
	debugfsArgs := []string{"-w", "-f", scriptPath, imgPath}
	opts.Progress.VerboseCmd(debugfsPath, debugfsArgs)
	out, runErr := runCmdStream(ctx, payloadDir, debugfsPath, debugfsCommandCounter(script, func(pct int) { opts.Stage.SetProgressPercent(float64(pct)) }), debugfsArgs...)
	if marker := findErrorMarker(script, out); marker != "" {
		return nil, fmt.Errorf("debugfs reported an error while populating %s (matched %q):\n%s", imgPath, marker, out)
	}
	if runErr != nil {
		return nil, fmt.Errorf("debugfs: %w\n%s", runErr, out)
	}

	opts.Stage.SetPhase("checking ext4")
	fsckArgs := []string{"-fn", imgPath}
	opts.Progress.VerboseCmd(e2fsckPath, fsckArgs)
	fsckOut, fsckErr := runCmd(ctx, "", e2fsckPath, fsckArgs...)
	if fsckErr != nil {
		return nil, fmt.Errorf("e2fsck -fn found problems in %s:\n%s", imgPath, fsckOut)
	}

	return warnings, nil
}

// runCmd runs an argv-array subprocess, in dir if it is not empty, and
// returns its combined output. Canceling ctx stops it (see
// internal/subprocess).
func runCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	return runCmdEnv(ctx, dir, nil, name, args...)
}

// runCmdEnv is runCmd with extra KEY=VALUE environment entries added to
// the inherited environment.
func runCmdEnv(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := subprocess.Command(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// modeBits returns the ext4 st_mode value (type bits | permission bits)
// for a tar header, so `sif ... mode ...` never drops the file's type.
func modeBits(hdr *tar.Header) uint32 {
	perm := uint32(hdr.Mode) & 07777 //nolint:gosec // G115: masked to 12 bits, which survive int64->uint32 truncation unchanged regardless of hdr.Mode's range
	var typeBits uint32
	switch hdr.Typeflag {
	case tar.TypeDir:
		typeBits = 040000
	case tar.TypeSymlink:
		typeBits = 0120000
	case tar.TypeChar:
		typeBits = 020000
	case tar.TypeBlock:
		typeBits = 060000
	case tar.TypeFifo:
		typeBits = 010000
	default: // tar.TypeReg and friends
		typeBits = 0100000
	}
	return typeBits | perm
}

// xattrSchilyPrefix is the PAX record namespace GNU tar (and Go's
// archive/tar) uses for extended attributes: a record
// "SCHILY.xattr.user.foo" = "bar" represents the xattr "user.foo" with
// value "bar".
const xattrSchilyPrefix = "SCHILY.xattr."

// xattrs returns hdr's extended attributes keyed by their bare name
// (the "SCHILY.xattr." prefix stripped). It merges both PAXRecords, the
// field archive/tar documents callers should use, and the older,
// deprecated Xattrs field, which archive/tar's Reader populates
// alongside PAXRecords from the same records but which some other tar
// producers may set on its own.
func xattrs(hdr *tar.Header) map[string]string {
	var attrs map[string]string
	for k, v := range hdr.PAXRecords {
		name, ok := strings.CutPrefix(k, xattrSchilyPrefix)
		if !ok {
			continue
		}
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs[name] = v
	}
	for k, v := range hdr.Xattrs { //nolint:staticcheck // fallback for tar producers that only set the deprecated field
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs[k] = v
	}
	return attrs
}

// buildDebugfsScript writes every regular file's content under
// payloadDir/fNNNNNN (a name that cannot collide with, or be confused
// for, the image's own paths), and returns a debugfs script that
// recreates the full merged rootfs - content, ownership, mode, mtime,
// symlinks, device nodes and hardlinks - inside an ext4 image. It checks
// ctx periodically (the same cadence as the stage progress readout) so
// an interrupt during this (potentially long, pure-Go) step is noticed
// without waiting for it to finish.
func buildDebugfsScript(ctx context.Context, rfs *rootfs.Rootfs, payloadDir string, stage *progress.Stage) (string, []string, error) {
	paths := make([]string, 0, len(rfs.Index))
	for p := range rfs.Index {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// A hardlink's target inode needs its final links_count set once, to
	// 1 (itself) plus however many TypeLink entries point at it.
	linksCount := map[string]int{}
	for _, p := range paths {
		e := rfs.Index[p]
		if e.Header.Typeflag == tar.TypeLink {
			target := normalize(e.Header.Linkname)
			if linksCount[target] == 0 {
				linksCount[target] = 1
			}
			linksCount[target]++
		}
	}

	var b strings.Builder
	var warnings []string
	fileN := 0
	xattrN := 0

	// Hardlinks are emitted in a second pass, after every other entry
	// (in particular, every regular file that might be a hardlink's
	// target) has already been created.
	var hardlinks []string

	for i, p := range paths {
		if i%64 == 0 {
			if stage != nil {
				stage.SetProgressCount(int64(i), int64(len(paths)), "files")
			}
			if err := ctx.Err(); err != nil {
				return "", nil, err
			}
		}
		e := rfs.Index[p]
		hdr := e.Header

		qp, err := quoteArg(p)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", p, err)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			fmt.Fprintf(&b, "mkdir %s\n", qp)
			writeAttrs(&b, qp, hdr)
			if err := writeXattrs(&b, payloadDir, &xattrN, qp, hdr, p, &warnings); err != nil {
				return "", nil, fmt.Errorf("%s: %w", p, err)
			}

		case tar.TypeReg:
			hostPath, err := writePayload(rfs, e, payloadDir, fileN)
			if err != nil {
				return "", nil, fmt.Errorf("%s: %w", p, err)
			}
			fileN++
			qh, err := quoteArg(hostPath)
			if err != nil {
				return "", nil, err
			}
			fmt.Fprintf(&b, "write %s %s\n", qh, qp)
			writeAttrs(&b, qp, hdr)
			if err := writeXattrs(&b, payloadDir, &xattrN, qp, hdr, p, &warnings); err != nil {
				return "", nil, fmt.Errorf("%s: %w", p, err)
			}

		case tar.TypeSymlink:
			qt, err := quoteArg(hdr.Linkname)
			if err != nil {
				return "", nil, fmt.Errorf("%s: symlink target: %w", p, err)
			}
			fmt.Fprintf(&b, "symlink %s %s\n", qp, qt)
			writeAttrs(&b, qp, hdr)
			if err := writeXattrs(&b, payloadDir, &xattrN, qp, hdr, p, &warnings); err != nil {
				return "", nil, fmt.Errorf("%s: %w", p, err)
			}

		case tar.TypeLink:
			target := normalize(hdr.Linkname)
			if err := checkHardlinkTarget(rfs, p, target); err != nil {
				return "", nil, err
			}
			qt, err := quoteArg(target)
			if err != nil {
				return "", nil, fmt.Errorf("%s: hardlink target: %w", p, err)
			}
			dir, _ := splitPath(p)
			qd, err := quoteArg(dir)
			if err != nil {
				return "", nil, fmt.Errorf("%s: %w", p, err)
			}
			// Unlike write/mkdir/symlink, debugfs's ln (make_link) does
			// not grow a directory whose last block is already full: it
			// fails with "No free space in the directory" and exits 0.
			// expand_dir guarantees room for one more entry regardless of
			// block size or name length, at the cost of at most one extra
			// directory block per hardlink.
			hardlinks = append(hardlinks, fmt.Sprintf("expand_dir %s\nln %s %s\n", qd, qt, qp))

		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			dir, base := splitPath(p)
			qd, err := quoteArg(dir)
			if err != nil {
				return "", nil, err
			}
			qb, err := quoteArg(base)
			if err != nil {
				return "", nil, err
			}
			var t string
			switch hdr.Typeflag {
			case tar.TypeChar:
				t = "c"
			case tar.TypeBlock:
				t = "b"
			case tar.TypeFifo:
				t = "p"
			}
			fmt.Fprintf(&b, "cd %s\n", qd)
			if t == "p" {
				fmt.Fprintf(&b, "mknod %s %s\n", qb, t)
			} else {
				fmt.Fprintf(&b, "mknod %s %s %d %d\n", qb, t, hdr.Devmajor, hdr.Devminor)
			}
			b.WriteString("cd \"/\"\n")
			writeAttrs(&b, qp, hdr)
			if err := writeXattrs(&b, payloadDir, &xattrN, qp, hdr, p, &warnings); err != nil {
				return "", nil, fmt.Errorf("%s: %w", p, err)
			}

		default:
			warnings = append(warnings, fmt.Sprintf("%s: unsupported tar entry type %d, skipped", p, hdr.Typeflag))
		}
	}

	for _, line := range hardlinks {
		b.WriteString(line)
	}

	for p, n := range linksCount {
		qp, err := quoteArg(p)
		if err != nil {
			return "", nil, err
		}
		fmt.Fprintf(&b, "sif %s links_count %d\n", qp, n)
	}

	if stage != nil {
		stage.SetProgressCount(int64(len(paths)), int64(len(paths)), "files")
	}
	return b.String(), warnings, nil
}

func writeAttrs(b *strings.Builder, quotedPath string, hdr *tar.Header) {
	fmt.Fprintf(b, "sif %s mode 0%o\n", quotedPath, modeBits(hdr))
	fmt.Fprintf(b, "sif %s uid %d\n", quotedPath, hdr.Uid)
	fmt.Fprintf(b, "sif %s gid %d\n", quotedPath, hdr.Gid)
	// The leading '@' forces integer parsing: debugfs's string_to_time
	// otherwise tries strptime on a bare number, first with
	// "%Y%m%d%H%M%S" and then with "%Y%m%d%H%M", before falling back to
	// parsing it as a plain integer. A Unix time whose digits happen to
	// parse under either format is stored as that (wrong) date instead of
	// the intended one - silently in most cases, since the result is
	// still a valid ext4 timestamp; only when it lands beyond ext4's
	// representable range does e2fsck -fn catch it, reporting "beyond
	// 2310-04-04 are likely pre-1970".
	fmt.Fprintf(b, "sif %s mtime @%d\n", quotedPath, hdr.ModTime.Unix())
}

// xattrNamespaces are the Linux extended attribute namespaces ext4
// stores; every attribute name starts with one of them.
var xattrNamespaces = []string{"user.", "trusted.", "security.", "system."}

// validXattrName reports whether name may be passed to debugfs: it has a
// known namespace prefix, which also means it cannot start with '-' and
// be read as an option.
func validXattrName(name string) bool {
	for _, ns := range xattrNamespaces {
		if len(name) > len(ns) && strings.HasPrefix(name, ns) {
			return true
		}
	}
	return false
}

// writeXattrs appends one "ea_set -f <value-file> <path> <name>" debugfs
// command per extended attribute on hdr, so file capabilities
// (security.capability) and SELinux labels (security.selinux) survive
// into the ext4 root, not just mode/uid/gid/mtime. Values are arbitrary
// bytes (security.capability is binary), and a debugfs script has no
// escape sequence for that, so each value is written to its own file
// under payloadDir and passed via "-f" rather than inlined. *xattrN
// numbers those files, distinct from writePayload's regular-file
// content numbering. An attribute whose name has no known namespace
// prefix is skipped with a warning (appended to *warnings, naming
// entryPath), not passed to debugfs.
func writeXattrs(b *strings.Builder, payloadDir string, xattrN *int, quotedPath string, hdr *tar.Header, entryPath string, warnings *[]string) error {
	attrs := xattrs(hdr)
	if len(attrs) == 0 {
		return nil
	}
	names := make([]string, 0, len(attrs))
	for name := range attrs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !validXattrName(name) {
			*warnings = append(*warnings, fmt.Sprintf("%s: extended attribute %q has no known namespace prefix, skipped", entryPath, name))
			continue
		}
		hostName := fmt.Sprintf("x%06d", *xattrN)
		*xattrN++
		if err := os.WriteFile(path.Join(payloadDir, hostName), []byte(attrs[name]), 0o600); err != nil {
			return fmt.Errorf("xattr %s: writing value payload: %w", name, err)
		}
		qh, err := quoteArg(hostName)
		if err != nil {
			return err
		}
		qn, err := quoteArg(name)
		if err != nil {
			return fmt.Errorf("xattr %s: %w", name, err)
		}
		fmt.Fprintf(b, "ea_set -f %s %s %s\n", qh, quotedPath, qn)
	}
	return nil
}

// writePayload copies a regular file's content to payloadDir under an
// index-numbered name, never its real name, and returns that name
// (relative to payloadDir, which is debugfs's working directory).
func writePayload(rfs *rootfs.Rootfs, e *rootfs.Entry, payloadDir string, n int) (string, error) {
	rc, err := rfs.Open(e)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	name := fmt.Sprintf("f%06d", n)
	out, err := os.Create(path.Join(payloadDir, name))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("writing payload for %s: %w", e.Path, err)
	}
	// Checked, not deferred-and-ignored: this file becomes part of the
	// ext4 image via a debugfs script right after we return, so a write
	// error surfaced only at Close (e.g. a delayed flush failure) must
	// not be silently swallowed.
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("writing payload for %s: %w", e.Path, err)
	}
	return name, nil
}

// findErrorMarker returns the first debugfsErrorMarkers entry found in
// debugfs's output, or "" if there is none. debugfs echoes every script
// line it runs as "debugfs: <line>", and those lines carry image paths,
// so they are skipped: otherwise a file named, say, "already exists"
// would read as a failure.
func findErrorMarker(script, out string) string {
	echoed := map[string]bool{}
	for _, line := range strings.Split(script, "\n") {
		echoed["debugfs: "+line] = true
	}
	for _, line := range strings.Split(out, "\n") {
		if echoed[strings.TrimRight(line, "\r")] {
			continue
		}
		for _, marker := range debugfsErrorMarkers {
			if strings.Contains(line, marker) {
				return marker
			}
		}
	}
	return ""
}

// checkHardlinkTarget fails unless the hardlink at p points at an entry
// that exists in the merged rootfs and is not a directory. A later
// layer can delete or replace a hardlink's target while keeping the link
// itself; the merged view then no longer has the content the link
// shared, so the conversion stops with an explicit error instead of a
// debugfs failure. A hardlink to a directory would corrupt the
// filesystem.
func checkHardlinkTarget(rfs *rootfs.Rootfs, p, target string) error {
	e, ok := rfs.Index[target]
	if !ok {
		return fmt.Errorf("%s: hardlink target %s is not in the merged filesystem (a later layer removed it); its content can't be recovered", p, target)
	}
	if e.Header.Typeflag == tar.TypeDir {
		return fmt.Errorf("%s: hardlink target %s is a directory", p, target)
	}
	return nil
}

// checkScriptLines fails if any line of script is longer than
// maxScriptLine. Paths, symlink targets and xattr names come from the
// image, so without this bound a long enough one would be split by
// debugfs into lines it parses as commands of their own.
func checkScriptLines(script string) error {
	for i, line := range strings.Split(script, "\n") {
		if len(line) > maxScriptLine {
			return fmt.Errorf("debugfs script line %d is %d bytes, longer than the %d-byte limit; an image path, symlink target or xattr name is too long (line starts %.120q)",
				i+1, len(line), maxScriptLine, line)
		}
	}
	return nil
}

// quoteArg quotes s for a debugfs script. debugfs supports double-quoted
// filespecs but no escape sequence within them, so a name containing a
// double quote, a newline or a NUL cannot be represented at all. A NUL
// would end the argument early: debugfs reads each script line into a C
// string, so everything after it, including the closing quote, is lost.
func quoteArg(s string) (string, error) {
	if strings.ContainsRune(s, '"') {
		return "", fmt.Errorf("path contains a double quote, which a debugfs script cannot represent: %q", s)
	}
	if strings.ContainsAny(s, "\n\r") {
		return "", fmt.Errorf("path contains a newline, which a debugfs script cannot represent: %q", s)
	}
	if strings.ContainsRune(s, 0) {
		return "", fmt.Errorf("path contains a NUL byte, which a debugfs script cannot represent: %q", s)
	}
	return `"` + s + `"`, nil
}

func normalize(name string) string {
	name = strings.TrimPrefix(name, "./")
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return path.Clean(name)
}

// splitPath splits an absolute path into its parent directory (never
// empty; "/" for a top-level entry) and base name.
func splitPath(p string) (dir, base string) {
	dir, base = path.Split(strings.TrimSuffix(p, "/"))
	dir = path.Clean(dir)
	return dir, base
}
