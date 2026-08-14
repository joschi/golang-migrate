# Development, Testing and Contributing

  1. Make sure you have a running Docker daemon
     (Install for [MacOS](https://docs.docker.com/docker-for-mac/))
  1. Use a version of Go that supports [modules](https://golang.org/cmd/go/#hdr-Modules__module_versions__and_more) (e.g. Go 1.11+)
  1. Fork this repo and `git clone` somewhere to `$GOPATH/src/github.com/golang-migrate/migrate`
      * Ensure that [Go modules are enabled](https://golang.org/cmd/go/#hdr-Preliminary_module_support) (e.g. your repo path or the `GO111MODULE` environment variable are set correctly)
  1. Install [golangci-lint](https://github.com/golangci/golangci-lint#install)
  1. Run the linter: `make lint`
  1. Confirm tests are working: `make test-short`
  1. Write awesome code ...
  1. `make test` to run all tests against all database versions
  1. Push code and open Pull Request

## One module per driver

The repository is a [Go workspace](https://go.dev/ref/mod#workspaces): the core
library, each source driver, each database driver, `dktesting` and the CLI are
all separate modules, so importing one driver does not pull in the dependencies
of the others. `go.work` ties them together for development.

Two things follow from that, and they surprise people:

  * **Work from inside the workspace.** `GOWORK=off` cannot resolve the
    intra-repo dependencies, because the requires between modules are only
    written at release time. See [RELEASING.md](RELEASING.md).
  * **`go list ./...` does not cross module boundaries.** To do something for
    every module, iterate: `go list -m -f '{{.Dir}}'`. The Makefile targets
    already do this.

Adding a driver means adding a directory with its own `go.mod`, a
`cmd/migrate/internal/cli/build_<driver>.go` behind a build tag, an entry in
`go.work`, and the driver's scheme in `internal/otelconv`.

Some more helpful commands:

  * You can specify which database/ source tests to run:
    `make test-short SOURCE='file go_bindata' DATABASE='postgres cassandra'`
  * After `make test`, run `make html-coverage` which opens a shiny test coverage overview.
  * `make build-cli` builds the CLI in directory `build/`.
  * `make lint` runs golangci-lint over every module.
  * `make tidy` syncs the workspace (`go mod tidy` cannot run per module; see
    [RELEASING.md](RELEASING.md)).
  * `make list-external-deps` lists all external dependencies for each module
  * `make docs && make open-docs` opens godoc in your browser, `make kill-docs` kills the godoc server.
    Repeatedly call `make docs` to refresh the server.
  * Set the `DOCKER_API_VERSION` environment variable to the latest supported version if you get errors regarding the docker client API version being too new.
