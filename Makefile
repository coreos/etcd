# run from repository root



# Example:
#   make build
#   make clean
#   make docker-clean
#   make docker-start
#   make docker-kill
#   make docker-remove

UNAME := $(shell uname)
XARGS = xargs
ARCH ?= $(shell go env GOARCH)

# -r is only necessary on GNU xargs.
ifeq ($(UNAME), Linux)
XARGS += -r
endif
XARGS += rm -r

.PHONY: build
build:
	GO_BUILD_FLAGS="-v" ./build.sh
	./bin/etcd --version
	./bin/etcdctl version
	./bin/etcdutl version

clean:
	rm -f ./codecov
	rm -rf ./covdir
	rm -f ./bin/Dockerfile-release*
	rm -rf ./bin/etcd*
	rm -rf ./default.etcd
	rm -rf ./tests/e2e/default.etcd
	rm -rf ./release
	rm -rf ./coverage/*.err ./coverage/*.out
	rm -rf ./tests/e2e/default.proxy
	find ./ -name "127.0.0.1:*" -o -name "localhost:*" -o -name "*.log" -o -name "agent-*" -o -name "*.coverprofile" -o -name "testname-proxy-*" | $(XARGS)

docker-clean:
	docker images
	docker image prune --force

docker-start:
	service docker restart

docker-kill:
	docker kill `docker ps -q` || true

docker-remove:
	docker rm --force `docker ps -a -q` || true
	docker rmi --force `docker images -q` || true



GO_VERSION ?= 1.16.3
ETCD_VERSION ?= $(shell git rev-parse --short HEAD || echo "GitNotFound")

TEST_SUFFIX = $(shell date +%s | base64 | head -c 15)
TEST_OPTS ?= PASSES='unit'

TMP_DIR_MOUNT_FLAG = --tmpfs=/tmp:exec
ifdef HOST_TMP_DIR
	TMP_DIR_MOUNT_FLAG = --mount type=bind,source=$(HOST_TMP_DIR),destination=/tmp
endif


TMP_DOCKERFILE:=$(shell mktemp)

# Example:
#   GO_VERSION=1.14.3 make build-docker-test
#   make build-docker-test
#
#   gcloud auth configure-docker
#   GO_VERSION=1.14.3 make push-docker-test
#   make push-docker-test
#
#   gsutil -m acl ch -u allUsers:R -r gs://artifacts.etcd-development.appspot.com
#   make pull-docker-test

build-docker-test:
	$(info GO_VERSION: $(GO_VERSION))
	@sed 's|REPLACE_ME_GO_VERSION|$(GO_VERSION)|g' ./tests/Dockerfile > $(TMP_DOCKERFILE)
	docker build \
	  --network=host \
	  --tag gcr.io/etcd-development/etcd-test:go$(GO_VERSION) \
	  --file $(TMP_DOCKERFILE) .

push-docker-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker push gcr.io/etcd-development/etcd-test:go$(GO_VERSION)

pull-docker-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker pull gcr.io/etcd-development/etcd-test:go$(GO_VERSION)



# Example:
#   make build-docker-test
#   make compile-with-docker-test
#   make compile-setup-gopath-with-docker-test

compile-with-docker-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker run \
	  --rm \
	  --mount type=bind,source=`pwd`,destination=/go/src/go.etcd.io/etcd \
	  gcr.io/etcd-development/etcd-test:go$(GO_VERSION) \
	  /bin/bash -c "GO_BUILD_FLAGS=-v GOOS=linux GOARCH=amd64 ./build.sh && ./bin/etcd --version"

compile-setup-gopath-with-docker-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker run \
	  --rm \
	  --mount type=bind,source=`pwd`,destination=/etcd \
	  gcr.io/etcd-development/etcd-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && ETCD_SETUP_GOPATH=1 GO_BUILD_FLAGS=-v GOOS=linux GOARCH=amd64 ./build.sh && ./bin/etcd --version && rm -rf ./gopath"



# Example:
#
# Local machine:
#   TEST_OPTS="PASSES='fmt'" make test
#   TEST_OPTS="PASSES='fmt bom dep build unit'" make test
#   TEST_OPTS="PASSES='build unit release integration_e2e functional'" make test
#   TEST_OPTS="PASSES='build grpcproxy'" make test
#
# Example (test with docker):
#   make pull-docker-test
#   TEST_OPTS="PASSES='fmt'" make docker-test
#   TEST_OPTS="VERBOSE=2 PASSES='unit'" make docker-test
#
# Travis CI (test with docker):
#   TEST_OPTS="PASSES='fmt bom dep build unit'" make docker-test
#
# Semaphore CI (test with docker):
#   TEST_OPTS="PASSES='build unit release integration_e2e functional'" make docker-test
#   HOST_TMP_DIR=/tmp TEST_OPTS="PASSES='build unit release integration_e2e functional'" make docker-test
#   TEST_OPTS="GOARCH=386 PASSES='build unit integration_e2e'" make docker-test
#
# grpc-proxy tests (test with docker):
#   TEST_OPTS="PASSES='build grpcproxy'" make docker-test
#   HOST_TMP_DIR=/tmp TEST_OPTS="PASSES='build grpcproxy'" make docker-test

.PHONY: test
test:
	$(info TEST_OPTS: $(TEST_OPTS))
	$(info log-file: test-$(TEST_SUFFIX).log)
	$(TEST_OPTS) ./test.sh 2>&1 | tee test-$(TEST_SUFFIX).log
	! egrep "(--- FAIL:|FAIL:|DATA RACE|panic: test timed out|appears to have leaked)" -B50 -A10 test-$(TEST_SUFFIX).log

test-smoke:
	$(info log-file: test-$(TEST_SUFFIX).log)
	PASSES="fmt build unit" ./test.sh 2<&1 | tee test-$(TEST_SUFFIX).log

test-full:
	$(info log-file: test-$(TEST_SUFFIX).log)
	PASSES="fmt build release unit integration functional e2e grpcproxy" ./test.sh 2<&1 | tee test-$(TEST_SUFFIX).log

docker-test:
	$(info GO_VERSION: $(GO_VERSION))
	$(info ETCD_VERSION: $(ETCD_VERSION))
	$(info TEST_OPTS: $(TEST_OPTS))
	$(info log-file: test-$(TEST_SUFFIX).log)
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`,destination=/go/src/go.etcd.io/etcd \
	  gcr.io/etcd-development/etcd-test:go$(GO_VERSION) \
	  /bin/bash -c "$(TEST_OPTS) ./test.sh 2>&1 | tee test-$(TEST_SUFFIX).log"
	! egrep "(--- FAIL:|FAIL:|DATA RACE|panic: test timed out|appears to have leaked)" -B50 -A10 test-$(TEST_SUFFIX).log

docker-test-coverage:
	$(info GO_VERSION: $(GO_VERSION))
	$(info ETCD_VERSION: $(ETCD_VERSION))
	$(info log-file: docker-test-coverage-$(TEST_SUFFIX).log)
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`,destination=/go/src/go.etcd.io/etcd \
	  gcr.io/etcd-development/etcd-test:go$(GO_VERSION) \
	  /bin/bash ./scripts/codecov_upload.sh docker-test-coverage-$(TEST_SUFFIX).log \
	! egrep "(--- FAIL:|FAIL:|DATA RACE|panic: test timed out|appears to have leaked)" -B50 -A10 docker-test-coverage-$(TEST_SUFFIX).log



# Example:
#   make compile-with-docker-test
#   ETCD_VERSION=v3-test make build-docker-release-main
#   ETCD_VERSION=v3-test make push-docker-release-main
#   gsutil -m acl ch -u allUsers:R -r gs://artifacts.etcd-development.appspot.com

build-docker-release-main:
	$(info ETCD_VERSION: $(ETCD_VERSION))
	cp ./Dockerfile-release.$(ARCH) ./bin/Dockerfile-release.$(ARCH)
	docker build \
	  --network=host \
	  --tag gcr.io/etcd-development/etcd:$(ETCD_VERSION) \
	  --file ./bin/Dockerfile-release.$(ARCH) \
	  ./bin
	rm -f ./bin/Dockerfile-release.$(ARCH)

	docker run \
	  --rm \
	  gcr.io/etcd-development/etcd:$(ETCD_VERSION) \
	  /bin/sh -c "/usr/local/bin/etcd --version && /usr/local/bin/etcdctl version && /usr/local/bin/etcdutl version"

push-docker-release-main:
	$(info ETCD_VERSION: $(ETCD_VERSION))
	docker push gcr.io/etcd-development/etcd:$(ETCD_VERSION)



# Example:
#   make build-docker-test
#   make compile-with-docker-test
#   make build-docker-static-ip-test
#
#   gcloud auth configure-docker
#   make push-docker-static-ip-test
#
#   gsutil -m acl ch -u allUsers:R -r gs://artifacts.etcd-development.appspot.com
#   make pull-docker-static-ip-test
#
#   make docker-static-ip-test-certs-run
#   make docker-static-ip-test-certs-metrics-proxy-run

build-docker-static-ip-test:
	$(info GO_VERSION: $(GO_VERSION))
	@sed 's|REPLACE_ME_GO_VERSION|$(GO_VERSION)|g' ./tests/docker-static-ip/Dockerfile > $(TMP_DOCKERFILE)
	docker build \
	  --network=host \
	  --tag gcr.io/etcd-development/etcd-static-ip-test:go$(GO_VERSION) \
	  --file ./tests/docker-static-ip/Dockerfile \
	  $(TMP_DOCKERFILE)

push-docker-static-ip-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker push gcr.io/etcd-development/etcd-static-ip-test:go$(GO_VERSION)

pull-docker-static-ip-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker pull gcr.io/etcd-development/etcd-static-ip-test:go$(GO_VERSION)

docker-static-ip-test-certs-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-static-ip/certs,destination=/certs \
	  gcr.io/etcd-development/etcd-static-ip-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs/run.sh && rm -rf m*.etcd"

docker-static-ip-test-certs-metrics-proxy-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-static-ip/certs-metrics-proxy,destination=/certs-metrics-proxy \
	  gcr.io/etcd-development/etcd-static-ip-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-metrics-proxy/run.sh && rm -rf m*.etcd"



# Example:
#   make build-docker-test
#   make compile-with-docker-test
#   make build-docker-dns-test
#
#   gcloud auth configure-docker
#   make push-docker-dns-test
#
#   gsutil -m acl ch -u allUsers:R -r gs://artifacts.etcd-development.appspot.com
#   make pull-docker-dns-test
#
#   make docker-dns-test-insecure-run
#   make docker-dns-test-certs-run
#   make docker-dns-test-certs-gateway-run
#   make docker-dns-test-certs-wildcard-run
#   make docker-dns-test-certs-common-name-auth-run
#   make docker-dns-test-certs-common-name-multi-run
#   make docker-dns-test-certs-san-dns-run

build-docker-dns-test:
	$(info GO_VERSION: $(GO_VERSION))
	@sed 's|REPLACE_ME_GO_VERSION|$(GO_VERSION)|g' ./tests/docker-dns/Dockerfile > $(TMP_DOCKERFILE)
	docker build \
	  --network=host \
	  --tag gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  --file ./tests/docker-dns/Dockerfile \
	  $(TMP_DOCKERFILE)

	docker run \
	  --rm \
	  --dns 127.0.0.1 \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "/etc/init.d/bind9 start && cat /dev/null >/etc/hosts && dig etcd.local"

push-docker-dns-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker push gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION)

pull-docker-dns-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker pull gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION)

docker-dns-test-insecure-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns/insecure,destination=/insecure \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /insecure/run.sh && rm -rf m*.etcd"

docker-dns-test-certs-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns/certs,destination=/certs \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs/run.sh && rm -rf m*.etcd"

docker-dns-test-certs-gateway-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns/certs-gateway,destination=/certs-gateway \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-gateway/run.sh && rm -rf m*.etcd"

docker-dns-test-certs-wildcard-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns/certs-wildcard,destination=/certs-wildcard \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-wildcard/run.sh && rm -rf m*.etcd"

docker-dns-test-certs-common-name-auth-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns/certs-common-name-auth,destination=/certs-common-name-auth \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-common-name-auth/run.sh && rm -rf m*.etcd"

docker-dns-test-certs-common-name-multi-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns/certs-common-name-multi,destination=/certs-common-name-multi \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-common-name-multi/run.sh && rm -rf m*.etcd"

docker-dns-test-certs-san-dns-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns/certs-san-dns,destination=/certs-san-dns \
	  gcr.io/etcd-development/etcd-dns-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-san-dns/run.sh && rm -rf m*.etcd"


# Example:
#   make build-docker-test
#   make compile-with-docker-test
#   make build-docker-dns-srv-test
#   gcloud auth configure-docker
#   make push-docker-dns-srv-test
#   gsutil -m acl ch -u allUsers:R -r gs://artifacts.etcd-development.appspot.com
#   make pull-docker-dns-srv-test
#   make docker-dns-srv-test-certs-run
#   make docker-dns-srv-test-certs-gateway-run
#   make docker-dns-srv-test-certs-wildcard-run

build-docker-dns-srv-test:
	$(info GO_VERSION: $(GO_VERSION))
	@sed 's|REPLACE_ME_GO_VERSION|$(GO_VERSION)|g' > $(TMP_DOCKERFILE)
	docker build \
	  --network=host \
	  --tag gcr.io/etcd-development/etcd-dns-srv-test:go$(GO_VERSION) \
	  --file ./tests/docker-dns-srv/Dockerfile \
	  $(TMP_DOCKERFILE)

	docker run \
	  --rm \
	  --dns 127.0.0.1 \
	  gcr.io/etcd-development/etcd-dns-srv-test:go$(GO_VERSION) \
	  /bin/bash -c "/etc/init.d/bind9 start && cat /dev/null >/etc/hosts && dig +noall +answer SRV _etcd-client-ssl._tcp.etcd.local && dig +noall +answer SRV _etcd-server-ssl._tcp.etcd.local && dig +noall +answer m1.etcd.local m2.etcd.local m3.etcd.local"

push-docker-dns-srv-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker push gcr.io/etcd-development/etcd-dns-srv-test:go$(GO_VERSION)

pull-docker-dns-srv-test:
	$(info GO_VERSION: $(GO_VERSION))
	docker pull gcr.io/etcd-development/etcd-dns-srv-test:go$(GO_VERSION)

docker-dns-srv-test-certs-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns-srv/certs,destination=/certs \
	  gcr.io/etcd-development/etcd-dns-srv-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs/run.sh && rm -rf m*.etcd"

docker-dns-srv-test-certs-gateway-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns-srv/certs-gateway,destination=/certs-gateway \
	  gcr.io/etcd-development/etcd-dns-srv-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-gateway/run.sh && rm -rf m*.etcd"

docker-dns-srv-test-certs-wildcard-run:
	$(info GO_VERSION: $(GO_VERSION))
	$(info HOST_TMP_DIR: $(HOST_TMP_DIR))
	$(info TMP_DIR_MOUNT_FLAG: $(TMP_DIR_MOUNT_FLAG))
	docker run \
	  --rm \
	  --tty \
	  --dns 127.0.0.1 \
	  $(TMP_DIR_MOUNT_FLAG) \
	  --mount type=bind,source=`pwd`/bin,destination=/etcd \
	  --mount type=bind,source=`pwd`/tests/docker-dns-srv/certs-wildcard,destination=/certs-wildcard \
	  gcr.io/etcd-development/etcd-dns-srv-test:go$(GO_VERSION) \
	  /bin/bash -c "cd /etcd && /certs-wildcard/run.sh && rm -rf m*.etcd"



# Example:
#   make build-functional
#   make build-docker-functional
#   make push-docker-functional
#   make pull-docker-functional

build-functional:
	$(info GO_VERSION: $(GO_VERSION))
	$(info ETCD_VERSION: $(ETCD_VERSION))
	./tests/functional/build
	./bin/etcd-agent -help || true && \
	  ./bin/etcd-proxy -help || true && \
	  ./bin/etcd-runner --help || true && \
	  ./bin/etcd-tester -help || true

build-docker-functional:
	$(info GO_VERSION: $(GO_VERSION))
	$(info ETCD_VERSION: $(ETCD_VERSION))
	@sed 's|REPLACE_ME_GO_VERSION|$(GO_VERSION)|g' > $(TMP_DOCKERFILE)
	docker build \
	  --network=host \
	  --tag gcr.io/etcd-development/etcd-functional:go$(GO_VERSION) \
	  --file ./tests/functional/Dockerfile \
	  .
	@mv ./tests/functional/Dockerfile.bak ./tests/functional/Dockerfile

	docker run \
	  --rm \
	  gcr.io/etcd-development/etcd-functional:go$(GO_VERSION) \
	  /bin/bash -c "./bin/etcd --version && \
	   ./bin/etcd-failpoints --version && \
	   ./bin/etcdctl version && \
	   ./bin/etcdutl version && \
	   ./bin/etcd-agent -help || true && \
	   ./bin/etcd-proxy -help || true && \
	   ./bin/etcd-runner --help || true && \
	   ./bin/etcd-tester -help || true && \
	   ./bin/benchmark --help || true"

push-docker-functional:
	$(info GO_VERSION: $(GO_VERSION))
	$(info ETCD_VERSION: $(ETCD_VERSION))
	docker push gcr.io/etcd-development/etcd-functional:go$(GO_VERSION)

pull-docker-functional:
	$(info GO_VERSION: $(GO_VERSION))
	$(info ETCD_VERSION: $(ETCD_VERSION))
	docker pull gcr.io/etcd-development/etcd-functional:go$(GO_VERSION)
