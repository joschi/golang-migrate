#!/usr/bin/env bash
#
# Release every module in the workspace at one version.
#
# Go resolves a nested module only through a tag prefixed with its directory,
# so releasing this repository means creating one tag per module on a single
# commit: v5.1.0, database/postgres/v5.1.0, source/github/v5.1.0, ...
#
# Usage:
#   scripts/tag-release.sh 5.1.0 --requires   # step 1, on a release/… branch
#   scripts/tag-release.sh 5.1.0              # step 2, after that lands
#
# Step 1 writes the intra-repo requires into every go.mod. They are absent
# between releases on purpose: go reads the go.mod of every required version
# when it loads the module graph, so a require on a tag that does not exist yet
# breaks every build in the workspace. go.work covers development; this step
# makes the modules resolvable for everyone else. Requiring a version that is
# tagged on the very same commit is fine.
#
# Step 2 creates and pushes the tags.
set -euo pipefail

cd "$(dirname "$0")/.."

V=${1:-}
MODE=${2:-tag}
if [[ ! $V =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "usage: $0 <version> [--requires]   (e.g. $0 5.1.0)" >&2
	exit 1
fi

# Every module in the workspace. A process substitution would hide a `go list`
# failure from set -e, leaving DIRS empty and the run a silent no-op -- which in
# tag mode means `git push origin` with no refspec, i.e. pushing the branch.
if ! modules=$(go list -m -f '{{.Dir}}') || [[ -z $modules ]]; then
	echo "error: cannot list the workspace modules; nothing was changed" >&2
	exit 1
fi
# avoid mapfile: macOS still ships bash 3.2
DIRS=()
while IFS= read -r line; do [[ -n $line ]] && DIRS+=("$line"); done <<<"$modules"
ROOT=$PWD

relpath() { echo "${1#"$ROOT"}" | sed 's|^/||'; }

# Every driver the CLI can build, taken from the build files themselves rather
# than the Makefile's default lists, which deliberately omit the cgo drivers.
ALL_TAGS=$(awk '/^\/\/go:build /{print $2}' cmd/migrate/internal/cli/build_*.go | sort -u | tr '\n' ' ')

if [[ $MODE == "--requires" ]]; then
	# Discover every module's dependencies before editing any go.mod. `go mod
	# edit` never loads the module graph, but `go list` does -- and the moment
	# one go.mod requires the not-yet-existing v$V, `go list` fails in every
	# module, not just that one. Editing as we go would abort the run after the
	# first module and leave the release half-written.
	RELS=()
	DEPS=()
	for i in "${!DIRS[@]}"; do
		dir=${DIRS[$i]}
		rel=$(relpath "$dir")
		[[ -z $rel ]] && continue # the core module depends on nothing in-repo

		# `go list -m` prints every module in the workspace, not just this one
		self=$(awk '/^module /{print $2; exit}' "$dir/go.mod")
		# Ask go which module owns each dependency rather than matching path
		# prefixes by hand, and drop the module's own packages. The build tags
		# matter: every driver the CLI imports sits behind one, so without them
		# cmd/migrate looks like it depends on the core module alone. Capture
		# before filtering: piping go list straight into grep would let pipefail
		# report the grep's success and hide a failed go list.
		if ! all=$(cd "$dir" && go list -deps -test -tags "$ALL_TAGS" \
			-f '{{if .Module}}{{.Module.Path}}{{end}}' ./...); then
			echo "error: go list failed in $rel; no go.mod was modified" >&2
			exit 1
		fi
		RELS[$i]=$rel
		DEPS[$i]=$(printf '%s\n' "$all" |
			{ grep '^github.com/golang-migrate/migrate' || true; } |
			{ grep -vx "$self" || true; } | sort -u)
	done

	for i in "${!DEPS[@]}"; do
		flags=()
		for mod in ${DEPS[$i]}; do flags+=(-require="$mod@v$V"); done
		if [[ ${#flags[@]} -gt 0 ]]; then
			(cd "${DIRS[$i]}" && go mod edit "${flags[@]}")
		fi
		echo "-- ${RELS[$i]}: ${DEPS[$i]//$'\n'/ }"
	done
	echo
	echo "Intra-repo requires pinned to v$V."
	echo
	echo "NOTE: the workspace will not build until those tags exist, because go"
	echo "reads the go.mod of every required version. That is expected. Commit"
	echo "this on a release/… branch -- CI skips those, and a PR from any other"
	echo "branch cannot go green. Once it has merged, tag the merge commit:"
	echo "  $0 $V"
	echo "To get back to a working tree without releasing: git checkout -- '**/go.mod'"
	exit 0
fi

echo "Tagging every module at v$V:"
TAGS=()
for dir in "${DIRS[@]}"; do
	rel=$(relpath "$dir")
	if [[ -z $rel ]]; then
		TAGS+=("v$V") # core module, tagged at the repository root
	else
		TAGS+=("$rel/v$V")
	fi
done

printf '  %s\n' "${TAGS[@]}"
read -r -p "Create and push these ${#TAGS[@]} tags? [y/N] " reply
[[ $reply == [yY] ]] || { echo "aborted"; exit 1; }

for tag in "${TAGS[@]}"; do
	git tag "$tag"
done

# GitHub creates no push event when more than three tags arrive in one push, so
# the root tag -- the one the goreleaser job watches -- goes in a push of its
# own, last. The prefixed module tags go first; only `v*` matches the tag filter
# in .github/workflows/ci.yaml, so their push triggers nothing by design.
# Interrupted between the two, the modules are published but unreleased, which
# is retriable; the reverse order would build a release before they resolve.
nested=()
for tag in "${TAGS[@]}"; do
	[[ $tag == "v$V" ]] || nested+=("$tag")
done
# an empty array under set -u is an unbound variable before bash 4.4
if [[ ${#nested[@]} -gt 0 ]]; then
	git push origin "${nested[@]}"
fi
git push origin "v$V"
