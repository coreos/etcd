REPOSITORY_ROOT := $(shell git rev-parse --show-toplevel)

.PHONY: all
all: build
include $(REPOSITORY_ROOT)/tests/robustness/Makefile

.PHONY: build
build:
	GO_BUILD_FLAGS="${GO_BUILD_FLAGS} -v -mod=readonly" ./scripts/build.sh

.PHONY: install-benchmark
install-benchmark: build
ifeq (, $(shell command -v benchmark))
	@echo "Installing etcd benchmark tool..."
	go install -v ./tools/benchmark
else
	@echo "benchmark tool already installed..."
endif

.PHONY: bench-put
bench-put: build install-benchmark
	@echo "Running benchmark: put $(ARGS)"
	./scripts/benchmark_test.sh put $(ARGS)

PLATFORMS=linux-amd64 linux-386 linux-arm linux-arm64 linux-ppc64le linux-s390x darwin-amd64 darwin-arm64 windows-amd64 windows-arm64

.PHONY: build-all
build-all:
	@for platform in $(PLATFORMS); do \
		$(MAKE) build-$${platform}; \
	done

.PHONY: build-%
build-%:
	GOOS=$$(echo $* | cut -d- -f 1) GOARCH=$$(echo $* | cut -d- -f 2) GO_BUILD_FLAGS="${GO_BUILD_FLAGS} -v -mod=readonly" ./scripts/build.sh

.PHONY: tools
tools:
	GO_BUILD_FLAGS="${GO_BUILD_FLAGS} -v -mod=readonly" ./scripts/build_tools.sh

# Tests

GO_TEST_FLAGS?=

.PHONY: test
test:
	PASSES="unit integration release e2e" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-unit
test-unit:
	PASSES="unit" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-integration
test-integration:
	PASSES="integration" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-e2e
test-e2e: build
	PASSES="e2e" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-grpcproxy-integration
test-grpcproxy-integration:
	PASSES="grpcproxy_integration" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-grpcproxy-e2e
test-grpcproxy-e2e: build
	PASSES="grpcproxy_e2e" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-e2e-release
test-e2e-release: build
	PASSES="release e2e" ./scripts/test.sh $(GO_TEST_FLAGS)

# When we release the first 3.8.0-alpha.0, we can remove `VERSION="3.8.99"` below.
.PHONY: test-release
test-release:
	PASSES="release_tests" VERSION="3.8.99" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-robustness
test-robustness:
	PASSES="robustness" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-coverage
test-coverage:
	COVERDIR=covdir PASSES="build cov" ./scripts/test.sh $(GO_TEST_FLAGS)

.PHONY: test-devcontainer
test-devcontainer:
	./scripts/test/devcontainer.sh

.PHONY: upload-coverage-report
upload-coverage-report:
	return_code=0; \
	$(MAKE) test-coverage || return_code=$$?; \
	COVERDIR=covdir ./scripts/codecov_upload.sh; \
	exit $$return_code

.PHONY: fuzz
fuzz: 
	./scripts/fuzzing.sh

# Static analysis
.PHONY: verify
verify: verify-bom verify-lint verify-dep verify-shellcheck verify-mod-tidy \
	verify-shellws verify-proto-annotations verify-genproto verify-yamllint \
	verify-markdown-marker verify-go-versions verify-gomodguard \
	verify-go-workspace verify-grpc-experimental

.PHONY: fix
fix: fix-mod-tidy fix-dep fix-bom fix-lint fix-yamllint sync-toolchain-directive \
	update-go-workspace fix-shell-ws

.PHONY: verify-bom
verify-bom:
	PASSES="bom" ./scripts/test.sh

.PHONY: fix-bom
fix-bom:
	./scripts/fix/bom.sh

.PHONY: verify-dep
verify-dep:
	PASSES="dep" ./scripts/test.sh

.PHONY: fix-dep
fix-dep: fix-mod-tidy
	./scripts/fix/dep.sh

.PHONY: verify-lint
verify-lint: install-golangci-lint
	PASSES="lint" ./scripts/test.sh

.PHONY: fix-lint
fix-lint: install-golangci-lint
	PASSES="lint_fix" ./scripts/test.sh

.PHONY: verify-shellcheck
verify-shellcheck:
	PASSES="shellcheck" ./scripts/test.sh

.PHONY: verify-mod-tidy
verify-mod-tidy:
	PASSES="mod_tidy" ./scripts/test.sh

.PHONY: fix-mod-tidy
fix-mod-tidy:
	./scripts/fix/mod-tidy.sh

.PHONY: verify-shellws
verify-shellws:
	PASSES="shellws" ./scripts/test.sh

.PHONY: fix-shell-ws
fix-shell-ws:
	./scripts/fix/shell_ws.sh

.PHONY: verify-proto-annotations
verify-proto-annotations:
	PASSES="proto_annotations" ./scripts/test.sh

.PHONY: verify-genproto
verify-genproto:
	PASSES="genproto" ./scripts/test.sh

.PHONY: verify-yamllint
verify-yamllint:
ifeq (, $(shell command -v yamllint))
	@echo "Installing yamllint..."
	tmpdir=$$(mktemp -d); \
	trap "rm -rf $$tmpdir" EXIT; \
	python3 -m venv $$tmpdir; \
	$$tmpdir/bin/python3 -m pip install yamllint; \
	$$tmpdir/bin/yamllint --config-file tools/.yamllint .
else
	@echo "yamllint already installed..."
	yamllint --config-file tools/.yamllint .
endif

.PHONY: verify-markdown-marker
verify-markdown-marker:
	PASSES="markdown_marker" ./scripts/test.sh

.PHONY: fix-yamllint
fix-yamllint:
	./scripts/fix/yamllint.sh

.PHONY: run-govulncheck
run-govulncheck:
	PASSES="govuln" ./scripts/test.sh

.PHONY: verify-container-scan
verify-container-scan:
	./scripts/container_scan.sh

# Tools

.PHONY: install-golangci-lint
install-golangci-lint:
	./scripts/verify_golangci-lint_version.sh

.PHONY: install-lazyfs
install-lazyfs: bin/lazyfs
bin/lazyfs:
	rm /tmp/lazyfs -rf
	git clone --depth 1 --branch 0.2.0 https://github.com/dsrhaslab/lazyfs /tmp/lazyfs
	cd /tmp/lazyfs/libs/libpcache; ./build.sh
	cd /tmp/lazyfs/lazyfs; ./build.sh
	mkdir -p ./bin
	cp /tmp/lazyfs/lazyfs/build/lazyfs ./bin/lazyfs

# Cleanup
.PHONY: clean
clean:
	rm -f ./codecov
	rm -rf ./covdir
	rm -f ./bin/Dockerfile-release
	rm -rf ./bin/etcd*
	rm -rf ./bin/lazyfs
	rm -rf ./bin/python
	rm -rf ./default.etcd
	rm -rf ./tests/e2e/default.etcd
	rm -rf ./release
	rm -rf ./coverage/*.err ./coverage/*.out
	rm -rf ./tests/e2e/default.proxy
	rm -rf ./bin/shellcheck*
	find ./ -name "127.0.0.1:*" -o -name "localhost:*" -o -name "*.log" -o -name "agent-*" -o -name "*.coverprofile" -o -name "testname-proxy-*" -delete

.PHONY: verify-go-versions
verify-go-versions:
	./scripts/verify_go_versions.sh

.PHONY: verify-gomodguard
verify-gomodguard:
	PASSES="gomodguard" ./scripts/test.sh

.PHONY: verify-go-workspace
verify-go-workspace:
	PASSES="go_workspace" ./scripts/test.sh

.PHONY: verify-grpc-experimental
verify-grpc-experimental:
	./scripts/verify_grpc_experimental.sh

.PHONY: sync-toolchain-directive
sync-toolchain-directive:
	./scripts/fix/sync_go_toolchain_directive.sh

.PHONY: markdown-diff-lint
markdown-diff-lint:
	./scripts/markdown_diff_lint.sh

.PHONY: update-go-workspace
update-go-workspace:
	./scripts/update_go_workspace.sh
