#!/usr/bin/env bash
# Runs every native Go fuzz target (a FuzzXxx function in a _test.go file)
# for a fixed time each. Targets are discovered with `go test -list`, so a
# new one needs no change here, in the Makefile or in the workflow.
#
# Usage: hack/fuzz.sh [FUZZTIME]
#
# FUZZTIME is a Go duration (default 30s), applied to each target. All
# targets run even when one fails; the exit status is non-zero if any
# did. A failing input is written under the package's testdata/fuzz/
# directory, where `go test` then runs it as a regression test: commit it
# together with the fix.
set -u

fuzztime=${1:-30s}
failed=()
ran=0

# A failure to list the packages is fatal: nothing would run, and the
# script must not look like a pass.
pkgs=$(go list ./...) || {
	echo "go list ./... failed" >&2
	exit 1
}

for pkg in $pkgs; do
	# `go test -list` prints the matching test names, then an "ok" line.
	# A package that does not compile fails here; that counts as a
	# failure, not as a package without targets.
	if ! listing=$(go test -list '^Fuzz' "$pkg" 2>&1); then
		echo "=== $pkg: go test -list failed"
		echo "$listing"
		failed+=("$pkg (go test -list failed)")
		continue
	fi
	for target in $(echo "$listing" | grep '^Fuzz' || true); do
		ran=$((ran + 1))
		echo "=== $pkg $target ($fuzztime)"
		if ! go test -run='^$' -fuzz="^${target}\$" -fuzztime="$fuzztime" "$pkg"; then
			failed+=("$pkg $target")
		fi
	done
done

echo "ran $ran fuzz targets, ${#failed[@]} failed"
for f in ${failed[@]+"${failed[@]}"}; do
	echo "FAILED: $f"
done
if [ "$ran" -eq 0 ]; then
	echo "no fuzz targets found" >&2
	exit 1
fi
[ ${#failed[@]} -eq 0 ]
