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

## Cutting a release

1. **Write the requires** on a release branch:

   ```
   ./scripts/tag-release.sh 5.1.0 --requires
   ```

   This pins every intra-repo require to `v5.1.0`. The workspace will not build
   until the tags exist — that is expected. Review the diff and open a PR.

   To abandon the release and get a working tree back:
   `git checkout -- '**/go.mod'`

2. **Tag the merge commit** once the PR has landed:

   ```
   make release V=5.1.0
   ```

   The script lists all tags, asks for confirmation, then creates and pushes
   them.

3. GoReleaser fires on the plain `v5.1.0` tag and publishes the CLI binaries,
   the deb packages and the Docker images. Nothing else is needed.

## What a breaking change costs

Because the modules share one version, a breaking change anywhere forces a
major bump everywhere: `/v6` in every import path, including for users who only
import the core library. Keep driver APIs additive and batch breaking changes
into a planned major.
