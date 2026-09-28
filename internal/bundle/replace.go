package bundle

import (
	"fmt"
	"os"
	"path/filepath"
)

// Stage creates a fresh, empty directory next to dir (dir's parent, so a
// later Commit's rename lands on the same filesystem) for convert to
// assemble a bundle into before anything is visible at dir itself. The
// caller must remove the returned directory if it never reaches Commit
// (a deferred os.RemoveAll is safe to run even after a successful
// Commit, since by then the directory has been renamed away).
func Stage(dir string) (string, error) {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // G301: same as the bundle dir itself; see CheckDest/Commit
		return "", fmt.Errorf("creating %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("creating a staging directory for %s: %w", dir, err)
	}
	// The bundle is meant to stay readable by whatever later reads or
	// deploys it (see the MkdirAll this replaced, and bundle.Write's own
	// 0o644), but MkdirTemp always creates 0o700; match the final
	// directory's mode before anything is written into it.
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // G302: bundle output must stay world-readable
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("preparing staging directory for %s: %w", dir, err)
	}
	return tmp, nil
}

// CheckDest reports whether dir is safe for Commit to replace: it
// doesn't exist yet, is empty, or is recognizably a previous contemper
// bundle (a contemper.json file directly inside it). Anything else -
// a non-empty directory that isn't a bundle, or a path that isn't a
// directory at all - is left untouched, and CheckDest returns an error
// describing why. Convert calls this before doing any of the expensive
// work of building a new bundle, so a bad --out fails fast; Commit
// checks again on its own before actually replacing anything.
func CheckDest(dir string) error {
	empty, isBundle, err := destStatus(dir)
	if err != nil {
		return err
	}
	if !empty && !isBundle {
		return fmt.Errorf("%s already exists, is not empty, and has no contemper.json, so it does not look like a previous bundle; refusing to replace it", dir)
	}
	return nil
}

// Commit atomically replaces dir with the fully-populated staging
// directory tmp (as returned by Stage): once Commit returns nil, dir is
// tmp (renamed, not copied) and tmp no longer exists.
//
// Replacing dir is refused - leaving both dir and tmp untouched - unless
// dir doesn't exist, is empty, or is recognizably a previous contemper
// bundle (see CheckDest); this is what keeps a convert into an existing
// --out from ever deleting a directory the user didn't ask it to
// replace.
//
// When dir is empty or missing, the rename is a single, atomic
// directory rename (POSIX rename(2) replaces an empty directory in
// place). When dir is a non-empty bundle, dir is first moved aside to a
// unique sibling name, tmp is renamed onto dir, and only then is the
// old bundle removed - so a failure between those two renames still
// leaves a complete bundle at dir (either the old one, restored, or the
// new one). No renameat2/RENAME_EXCHANGE is used, so this works
// unchanged on macOS.
func Commit(tmp, dir string) error {
	empty, isBundle, err := destStatus(dir)
	if err != nil {
		return err
	}
	if !empty && !isBundle {
		return fmt.Errorf("%s already exists, is not empty, and has no contemper.json, so it does not look like a previous bundle; refusing to replace it", dir)
	}

	if empty {
		if err := os.Rename(tmp, dir); err != nil {
			return fmt.Errorf("replacing %s: %w", dir, err)
		}
		return nil
	}

	oldDir, err := reserveSiblingName(filepath.Dir(dir), filepath.Base(dir)+".old")
	if err != nil {
		return fmt.Errorf("preparing to replace %s: %w", dir, err)
	}
	if err := os.Rename(dir, oldDir); err != nil {
		return fmt.Errorf("moving previous bundle %s aside: %w", dir, err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if rerr := os.Rename(oldDir, dir); rerr != nil {
			return fmt.Errorf("replacing %s failed (%w), and restoring the previous bundle failed too (%w); the previous bundle is preserved at %s, the new one at %s", dir, err, rerr, oldDir, tmp)
		}
		return fmt.Errorf("replacing %s: %w", dir, err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		return fmt.Errorf("wrote the new bundle to %s, but could not remove the previous bundle left at %s: %w", dir, oldDir, err)
	}
	return nil
}

// destStatus reports whether dir doesn't exist or is empty (either is
// safe to replace outright), and whether it looks like a previous
// contemper bundle (safe to replace via the move-aside dance). A dir
// that exists, is non-empty, and isn't a bundle reports
// empty=false, isBundle=false; the caller turns that into a refusal.
func destStatus(dir string) (empty, isBundle bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, false, nil
		}
		return false, false, fmt.Errorf("checking %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return true, false, nil
	}
	if _, err := os.Stat(filepath.Join(dir, "contemper.json")); err == nil {
		return false, true, nil
	}
	return false, false, nil
}

// reserveSiblingName returns a path next to parent/base that doesn't
// exist yet and is unique among concurrent callers, using the same
// create-then-remove trick as os.MkdirTemp to pick the name (there is
// necessarily a brief window where nothing occupies the path, but two
// converts racing for the same --out and bundle name are not otherwise
// made safe by this package).
func reserveSiblingName(parent, base string) (string, error) {
	tmp, err := os.MkdirTemp(parent, "."+base+"-*")
	if err != nil {
		return "", err
	}
	if err := os.Remove(tmp); err != nil {
		return "", err
	}
	return tmp, nil
}
