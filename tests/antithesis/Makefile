ANTITHESIS_ROOT :=$(dir $(realpath $(lastword $(MAKEFILE_LIST))))
REPOSITORY_ROOT := $(shell git rev-parse --show-toplevel)
USER_ID := $(shell id -u)
GROUP_ID := $(shell id -g)
ARCH ?= $(shell go env GOARCH)
REF = main
IMAGE_REPOSITORY = ""
IMAGE_TAG = latest

CFG_NODE_COUNT ?= 3

.PHONY: antithesis-build-client-docker-image
antithesis-build-client-docker-image: validate-node-count
	# This is a release image using go build without our tooling. Using the
	# version defined in .go-version is the correct image tag.
	docker build \
		--build-arg GO_IMAGE_TAG=$(shell cat $(REPOSITORY_ROOT)/.go-version) \
		--file $(ANTITHESIS_ROOT)/test-template/Dockerfile \
		--tag etcd-client:latest \
		$(REPOSITORY_ROOT)

.PHONY: antithesis-build-etcd-image
antithesis-build-etcd-image:
	# Use the Workspace Go version from the go directive. This is the base image
	# later in the Dockerfile, we'll set the correct toolchain.
	docker build \
		--build-arg GO_IMAGE_TAG=$(shell go work edit -json | jq .Go) \
		--build-arg REF=$(REF) \
		--tag etcd-server:latest \
		-f ./server/Dockerfile \
		$(ANTITHESIS_ROOT)

.PHONY: antithesis-build-etcd-image-release-3.4
antithesis-build-etcd-image-release-3.4: REF=release-3.4
antithesis-build-etcd-image-release-3.4: antithesis-build-etcd-image

.PHONY: antithesis-build-etcd-image-release-3.5
antithesis-build-etcd-image-release-3.5: REF=release-3.5
antithesis-build-etcd-image-release-3.5: antithesis-build-etcd-image

.PHONY: antithesis-build-etcd-image-release-3.6
antithesis-build-etcd-image-release-3.6: REF=release-3.6
antithesis-build-etcd-image-release-3.6: antithesis-build-etcd-image

.PHONY: antithesis-build-etcd-image-main
antithesis-build-etcd-image-main: REF=main
antithesis-build-etcd-image-main: antithesis-build-etcd-image

.PHONY: antithesis-build-config-image
antithesis-build-config-image:
	docker build -f config/Dockerfile config -t etcd-config:latest \
		--build-arg IMAGE_REPOSITORY=$(IMAGE_REPOSITORY) \
		--build-arg IMAGE_TAG=$(IMAGE_TAG)

.PHONY: antithesis-k8s-up
antithesis-k8s-up: check-k8s-tools
	kind create cluster --name etcd-antithesis
	kind load docker-image etcd-client:latest --name etcd-antithesis
	kind load docker-image etcd-server:latest --name etcd-antithesis
	kubectl apply -f config/manifests
	kubectl wait --for=condition=ready pod -l app=etcd --timeout=120s
	kubectl wait --for=condition=ready pod -l app=etcd-client --timeout=120s

.PHONY: antithesis-run-k8s-traffic
antithesis-run-k8s-traffic:
	kubectl exec deployment/etcd-client -- /opt/antithesis/test/v1/robustness/singleton_driver_traffic

.PHONY: antithesis-run-k8s-validation
antithesis-run-k8s-validation:
	kubectl exec deployment/etcd-client -- /opt/antithesis/test/v1/robustness/finally_validation

.PHONY: antithesis-run-local-traffic
antithesis-run-local-traffic:
	export ETCD_ROBUSTNESS_DATA_PATHS=/tmp/etcddata/etcd-0,/tmp/etcddata/etcd-1,/tmp/etcddata/etcd-2 && export ETCD_ROBUSTNESS_REPORT_PATH=reports/report && export ETCD_ROBUSTNESS_ENDPOINTS=127.0.0.1:12379,127.0.0.1:22379,127.0.0.1:32379 && \
		go run --race ./test-template/robustness/traffic/main.go

.PHONY: antithesis-run-local-validation
antithesis-run-local-validation:
	export ETCD_ROBUSTNESS_DATA_PATHS=/tmp/etcddata/etcd-0,/tmp/etcddata/etcd-1,/tmp/etcddata/etcd-2 && export ETCD_ROBUSTNESS_REPORT_PATH=reports/report && export ETCD_ROBUSTNESS_ENDPOINTS=127.0.0.1:12379,127.0.0.1:22379,127.0.0.1:32379 && \
		go run --race ./test-template/robustness/finally/main.go

.PHONY: antithesis-clean
antithesis-clean: check-k8s-tools
	kind delete cluster --name etcd-antithesis
	rm -rf /tmp/etcddata /tmp/etcdreport

.PHONY: validate-node-count
validate-node-count:
	@if [ "$(CFG_NODE_COUNT)" != "1" ] && [ "$(CFG_NODE_COUNT)" != "3" ]; then \
		echo "CFG_NODE_COUNT must be either 1 or 3 (got $(CFG_NODE_COUNT))"; \
		exit 1; \
	fi

REQUIRED_K8S_TOOLS := kubectl kind

.PHONY: check-k8s-tools
check-k8s-tools:
	@for tool in $(REQUIRED_K8S_TOOLS); do \
		if ! command -v $$tool >/dev/null 2>&1; then \
			echo "Error: '$$tool' is missing.  Please install it before continuing." >&2; \
			exit 1; \
		fi \
	done
