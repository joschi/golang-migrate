# Releasing

This repository is a Go workspace containing one module per driver, plus the
core library, the `dktesting` helpers and the CLI. **Every module is released
together at a single version.** There is one version for the whole repository.

## Why a release is ~34 tags

Go resolves a nested module only through a tag prefixed with the module's
directory. Releasing v5.1.0 therefore means creating, on one commit:

```
v5.1.0                        # the core module, at the repository root
cmd/migrate/v5.1.0
database/postgres/v5.1.0
source/github/v5.1.0
...
```

Pushing a partial set publishes a partial release: modules whose tag is missing
are not resolvable and do not appear on pkg.go.dev. Always push the whole set.

## Intra-repo requires are absent between releases

Look at any driver's `go.mod` and you will not find a require on
`github.com/golang-migrate/migrate/v5`, even though the driver imports it.
That is deliberate.

Go reads the `go.mod` of every required version when it loads the module
graph — including for a module the workspace already supplies. A require on a
version that is not tagged yet therefore fails to resolve and breaks every
build in the workspace, not just the module that declares it. Between releases
there is no newer tag to point at, so the requires stay out and `go.work`
supplies the modules locally.

The release procedure writes them in as part of cutting the release. Requiring
a version that is tagged on the very same commit is fine.

The practical consequences during development:

- Build, test and lint from inside the workspace. `GOWORK=off` cannot resolve
  the intra-repo dependencies.
- `go mod tidy` cannot run in a module that has intra-repo dependencies. Use
  `make tidy` (`go work sync`) instead.
- The `tidy-check` CI job is informational until the first v5 tag exists.
- CI does not run on `release/…` branches. A release commit pins requires to
  tags that do not exist yet, so every job would fail on it; the code it carries
  was already tested on the commits that went into it, and the whole workspace
  is exercised again once the tags exist.

## Cutting a release

1. **Write the requires** on a branch named `release/…`:

   ```
   git switch -c release/5.1.0
   ./scripts/tag-release.sh 5.1.0 --requires
   ```

   This pins every intra-repo require to `v5.1.0`. The workspace will not build
   until the tags exist — that is expected. Review the diff and open a PR.

   The branch name matters: CI skips `release/…` branches, so a release branch
   named anything else lands a PR that cannot go green and cannot be merged.

   To abandon the release and get a working tree back:
   `git checkout -- '**/go.mod'`

2. **Tag the merge commit** once the PR has landed:

   ```
   make release V=5.1.0
   ```

   The script lists all tags, asks for confirmation, then creates and pushes
   them — in two pushes. GitHub creates no push event when more than three tags
   arrive at once, so the module tags go first and the plain `v5.1.0` tag is
   pushed on its own, last. That last push is the one that starts the release
   build; sending all ~34 together would publish the modules to the proxy and
   silently skip it.

   **If it fails part way, run it again.** The module tags are pushed
   atomically, so a failure there publishes nothing. If they land but the
   `v5.1.0` push does not, they stay — no release build has started yet, and
   re-running picks up where it stopped: tags already pointing at the release
   commit are left alone, and re-pushing an unchanged ref does nothing. The
   script only refuses when `v5.1.0` already names a *different* commit, which
   means someone else's release is wearing this version number.

3. GoReleaser fires on the plain `v5.1.0` tag and publishes the CLI binaries,
   the deb packages and the Docker images. Nothing else is needed.

   It is told which tag it is releasing (`GORELEASER_CURRENT_TAG`) rather than
   discovering it: with 33 module tags on the same commit, `git describe` finds
   one of those first. The `Makefile`'s `VERSION` guards against the same thing
   separately — it only affects binaries from `make build*` and the plain
   `Dockerfile`, never the artifacts GoReleaser publishes. `make echo-version`
   prints it.

4. **Write the `go.sum` entries**, once the tags are public:

   ```
   git switch -c chore/tidy-5.1.0
   go list -m -f '{{.Dir}}' | while read -r d; do (cd "$d" && GOWORK=off go mod tidy); done
   ```

   `go mod edit -require` writes `go.mod` and nothing else, so the modules ship
   requiring each other with no matching `go.sum` entry. That costs consumers
   nothing — they compute their own — but until it is done `GOWORK=off go build`
   fails inside each module, which is exactly what the `tidy-check` CI job runs.
   This is a follow-up commit rather than part of the release commit: the tags
   have to exist before anything can be hashed. Once it has landed, drop
   `continue-on-error` from `tidy-check` and the job becomes a real guard.

## What a breaking change costs

Because the modules share one version, a breaking change anywhere forces a
major bump everywhere: `/v6` in every import path, including for users who only
import the core library. Keep driver APIs additive and batch breaking changes
into a planned major.
