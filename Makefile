SOURCE ?= file go_bindata github github_ee bitbucket aws_s3 google_cloud_storage godoc_vfs gitlab gitea
DATABASE ?= postgres mysql redshift cassandra spanner cockroachdb yugabytedb clickhouse mongodb sqlserver firebird neo4j pgx5 rqlite couchbase
DATABASE_TEST ?= $(DATABASE) sqlite sqlite3 sqlcipher duckdb
VERSION ?= $(shell git describe --tags 2>/dev/null | cut -c 2-)
TEST_FLAGS ?=
REPO_OWNER ?= $(shell cd .. && basename "$$(pwd)")
COVERAGE_DIR ?= .coverage

build:
	CGO_ENABLED=0 go build -ldflags='-X main.Version=$(VERSION)' -tags '$(DATABASE) $(SOURCE)' ./cmd/migrate

build-docker:
	CGO_ENABLED=0 go build -a -o build/migrate.linux-386 -ldflags="-s -w -X main.Version=${VERSION}" -tags "$(DATABASE) $(SOURCE)" ./cmd/migrate

build-cli: clean
	-mkdir ./build
	cd ./cmd/migrate && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o ../../build/migrate.linux-amd64 -ldflags='-X main.Version=$(VERSION) -extldflags "-static"' -tags '$(DATABASE) $(SOURCE)' .
	cd ./cmd/migrate && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -a -o ../../build/migrate.linux-armv7 -ldflags='-X main.Version=$(VERSION) -extldflags "-static"' -tags '$(DATABASE) $(SOURCE)' .
	cd ./cmd/migrate && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -a -o ../../build/migrate.linux-arm64 -ldflags='-X main.Version=$(VERSION) -extldflags "-static"' -tags '$(DATABASE) $(SOURCE)' .
	cd ./cmd/migrate && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -a -o ../../build/migrate.darwin-amd64 -ldflags='-X main.Version=$(VERSION) -extldflags "-static"' -tags '$(DATABASE) $(SOURCE)' .
	cd ./cmd/migrate && CGO_ENABLED=0 GOOS=windows GOARCH=386 go build -a -o ../../build/migrate.windows-386.exe -ldflags='-X main.Version=$(VERSION) -extldflags "-static"' -tags '$(DATABASE) $(SOURCE)' .
	cd ./cmd/migrate && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -a -o ../../build/migrate.windows-amd64.exe -ldflags='-X main.Version=$(VERSION) -extldflags "-static"' -tags '$(DATABASE) $(SOURCE)' .
	cd ./build && find . -name 'migrate*' | xargs -I{} tar czf {}.tar.gz {}
	cd ./build && shasum -a 256 * > sha256sum.txt
	cat ./build/sha256sum.txt


clean:
	-rm -r ./build


test-short:
	make test-with-flags --ignore-errors TEST_FLAGS='-short'


# Each module writes its own coverage profile: they are separate builds, so a
# single shared profile would just be overwritten module by module.
test:
	@-rm -r $(COVERAGE_DIR)
	@mkdir -p $(COVERAGE_DIR)
	$(call foreach_module, go test -v -race -covermode atomic \
		-coverprofile "$(CURDIR)/$(COVERAGE_DIR)/$$(echo "$${dir##$(CURDIR)/}" | tr / -).txt" \
		-bench=. -benchmem -timeout 20m ./...)


test-with-flags:
	@echo SOURCE: $(SOURCE)
	@echo DATABASE_TEST: $(DATABASE_TEST)

	$(call foreach_module, go test $(TEST_FLAGS) ./...)


kill-orphaned-docker-containers:
	docker rm -f $(shell docker ps -aq --filter label=org.testcontainers=true)


html-coverage:
	go tool cover -html=$(COVERAGE_DIR)/combined.txt


list-external-deps:
	$(call foreach_module, go list -f '{{join .Deps "\n"}}' ./... | grep -v github.com/$(REPO_OWNER)/migrate | sort -u | xargs go list -f '{{if not .Standard}}{{.ImportPath}}{{end}}')


lint:
	$(call foreach_module, golangci-lint run)


# go mod tidy cannot run per module while the intra-repo requires are absent;
# see RELEASING.md. Syncing the workspace is the equivalent for day-to-day use.
tidy:
	go work sync


restore-import-paths:
	find . -name '*.go' -type f -execdir sed -i '' s%\"github.com/$(REPO_OWNER)/migrate%\"github.com/mattes/migrate%g '{}' \;


rewrite-import-paths:
	find . -name '*.go' -type f -execdir sed -i '' s%\"github.com/mattes/migrate%\"github.com/$(REPO_OWNER)/migrate%g '{}' \;


# example: fswatch -0 --exclude .godoc.pid --event Updated . | xargs -0 -n1 -I{} make docs
docs:
	-make kill-docs
	nohup godoc -play -http=127.0.0.1:6064 </dev/null >/dev/null 2>&1 & echo $$! > .godoc.pid
	cat .godoc.pid


kill-docs:
	@cat .godoc.pid
	kill -9 $$(cat .godoc.pid)
	rm .godoc.pid


open-docs:
	open http://localhost:6064/pkg/github.com/$(REPO_OWNER)/migrate


# Every module is released together at one version, and Go resolves nested
# modules only through prefixed tags, so a release is ~34 tags on one commit.
# See RELEASING.md.
#
# example: make release V=5.0.0
release:
	./scripts/tag-release.sh $(V)

echo-source:
	@echo "$(SOURCE)"

echo-database:
	@echo "$(DATABASE)"


# The repository is a Go workspace with one module per driver. Most targets
# have to run once per module rather than once over ./..., which does not span
# nested modules.
define foreach_module
	@go list -m -f '{{.Dir}}' | while read -r dir; do \
		echo "-- $${dir##$(CURDIR)/}"; \
		( cd "$$dir" && $(1) ) || exit 1; \
	done

endef


.PHONY: build build-docker build-cli clean test-short test test-with-flags html-coverage \
        restore-import-paths rewrite-import-paths list-external-deps release lint tidy \
		docs kill-docs open-docs kill-orphaned-docker-containers echo-source echo-database

SHELL = /bin/sh
RAND = $(shell echo $$RANDOM)
