# Release variables
RELEASE_TAG ?= $(shell git describe --abbrev=0 2>/dev/null)
GORELEASER_VERSION ?= v2.16.0

# Image URL to use all building/pushing image targets
IMAGE_REPO ?= huaweiclouddeveloper
STAGING_REGISTRY ?= ghcr.io/$(IMAGE_REPO)
REGISTRY ?= $(STAGING_REGISTRY)
IMAGE_NAME ?= cce/karpenter/controller
CORE_IMAGE_NAME ?= $(IMAGE_NAME)
CORE_CONTROLLER_IMG ?= $(REGISTRY)/$(CORE_IMAGE_NAME)
IMG ?= $(CORE_CONTROLLER_IMG):$(RELEASE_TAG)

GORELEASER_CONFIG := .goreleaser.yaml


# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

PROVIDER_API_PATH ?= ./pkg/apis/...
PROVIDER_CRD_NAME ?= karpenter.k8s.huawei_ccenodeclasses.yaml
CONTROLLER_RBAC_PATH ?= ./pkg/controllers/...
CONTROLLER_ROLE_NAME ?= manager-role

.PHONY: manifests
manifests: controller-gen ## Generate the provider CRD in the Helm chart.
	"$(CONTROLLER_GEN)" crd paths="$(PROVIDER_API_PATH)" output:crd:artifacts:config="$(HELM_CHART_DIR)/crds"

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind and Helm are pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= karpenter-provider-huawei-test-e2e
KIND_NODE_IMAGE ?= kindest/node:v1.34.3

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@command -v $(HELM) >/dev/null 2>&1 || { \
		echo "Helm is not installed. Please install Helm manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) --image $(KIND_NODE_IMAGE) ;; \
	esac

.PHONY: test-e2e
test-e2e: ## Run the e2e tests. Expected an isolated environment using Kind.
	@trap 'status=$$?; cleanup_status=0; $(MAKE) cleanup-test-e2e || cleanup_status=$$?; if [ "$$status" -ne 0 ]; then exit "$$status"; fi; exit "$$cleanup_status"' EXIT; \
		$(MAKE) setup-test-e2e manifests generate fmt vet; \
		HELM=$(HELM) KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager ./cmd/controller

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/controller

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: print-image
print-image: ## Print the effective controller image reference.
	@echo ${IMG}

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name karpenter-provider-huawei-builder
	$(CONTAINER_TOOL) buildx use karpenter-provider-huawei-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm karpenter-provider-huawei-builder
	rm Dockerfile.cross

##@ Helm

HELM ?= helm
HELM_CHART_DIR ?= charts/karpenter-provider-huawei
HELM_RELEASE_NAME ?= karpenter-provider-huawei
HELM_NAMESPACE ?= karpenter-provider-huawei-system

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart.
	$(HELM) lint $(HELM_CHART_DIR)

.PHONY: helm-package
helm-package: ## Package the Helm chart and regenerate the repository index.
	HELM="$(HELM)" CHART_DIR="$(HELM_CHART_DIR)" ./hack/package-chart.sh

CHART_VERSION ?= $(shell sed -n 's/^version:[[:space:]]*//p' $(HELM_CHART_DIR)/Chart.yaml)

.PHONY: helm-push
helm-push: helm-package ## Package the Helm chart and push it to the OCI registry.
	$(HELM) push "$(HELM_CHART_DIR)-$(CHART_VERSION).tgz" oci://$(STAGING_REGISTRY)/charts

.PHONY: helm-template
helm-template: ## Render Helm chart templates locally.
	$(HELM) template $(HELM_RELEASE_NAME) $(HELM_CHART_DIR) --namespace $(HELM_NAMESPACE) --include-crds

.PHONY: verify-manifests
verify-manifests: controller-gen ## Verify rendered Helm manifests, generated CRDs, and controller RBAC.
	CONTROLLER_GEN="$(CONTROLLER_GEN)" \
	HELM="$(HELM)" \
	CHART_DIR="$(HELM_CHART_DIR)" \
	RELEASE_NAME="$(HELM_RELEASE_NAME)" \
	NAMESPACE="$(HELM_NAMESPACE)" \
	PROVIDER_API_PATH="$(PROVIDER_API_PATH)" \
	PROVIDER_CRD_NAME="$(PROVIDER_CRD_NAME)" \
	CONTROLLER_RBAC_PATH="$(CONTROLLER_RBAC_PATH)" \
	CONTROLLER_ROLE_NAME="$(CONTROLLER_ROLE_NAME)" \
	./hack/verify-manifests.sh

.PHONY: helm-install
helm-install: ## Install the Helm chart to the K8s cluster.
	@base="$${IMG##*/}"; \
	case "$$base" in \
		*:* ) repo="$${IMG%:*}"; tag="$${IMG##*:}" ;; \
		* ) repo="$$IMG"; tag="latest" ;; \
	esac; \
	$(HELM) install $(HELM_RELEASE_NAME) $(HELM_CHART_DIR) \
		--namespace $(HELM_NAMESPACE) \
		--set image.repository="$$repo" \
		--set image.tag="$$tag" \
		--create-namespace

.PHONY: helm-upgrade
helm-upgrade: ## Upgrade the Helm chart on the K8s cluster.
	@base="$${IMG##*/}"; \
	case "$$base" in \
		*:* ) repo="$${IMG%:*}"; tag="$${IMG##*:}" ;; \
		* ) repo="$$IMG"; tag="latest" ;; \
	esac; \
	$(HELM) upgrade $(HELM_RELEASE_NAME) $(HELM_CHART_DIR) \
		--namespace $(HELM_NAMESPACE) \
		--set image.repository="$$repo" \
		--set image.tag="$$tag"

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the Helm chart from the K8s cluster.
	$(HELM) uninstall $(HELM_RELEASE_NAME) --namespace $(HELM_NAMESPACE)

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KIND ?= kind
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GORELEASER ?= $(LOCALBIN)/goreleaser
GO_TOOLCHAIN_VERSION := $(shell go env GOVERSION)

## Tool Versions
CONTROLLER_TOOLS_VERSION ?= v0.20.0

#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.7.2
.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: goreleaser
goreleaser: $(GORELEASER) ## Download goreleaser locally if necessary.
$(GORELEASER): $(LOCALBIN)
	$(call go-install-tool,$(GORELEASER),github.com/goreleaser/goreleaser/v2,$(GORELEASER_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary
# if it doesn't exist for the current tool version and Go toolchain.
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)-$(GO_TOOLCHAIN_VERSION)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)-$(GO_TOOLCHAIN_VERSION)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)-$(GO_TOOLCHAIN_VERSION)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)-$(GO_TOOLCHAIN_VERSION)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef


##@ release:

.PHONY: check-github-token
check-github-token: ## Check if the github token is set
	@if [ -z "${GITHUB_TOKEN}" ]; then echo "GITHUB_TOKEN is not set"; exit 1; fi

.PHONY: check-release-tag
check-release-tag: ## Check if the release tag is set
	@if [ -z "${RELEASE_TAG}" ]; then echo "RELEASE_TAG is not set"; exit 1; fi
	# @if ! [ -z "$$(git status --porcelain)" ]; then echo "Your local git repository contains uncommitted changes, use git clean before proceeding."; exit 1; fi

.PHONY: release
release: check-github-token check-release-tag $(GORELEASER)
	git checkout "${RELEASE_TAG}"
	$(GORELEASER) release --config $(GORELEASER_CONFIG) --clean
