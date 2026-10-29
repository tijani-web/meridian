# Meridian — GPU Fleet Control Plane Makefile
# All targets documented. Run `make help` for a summary.
# On Windows, use `.\make.ps1` or install GNU make via winget.

SHELL := /bin/bash

# ============================================================
# Variables
# ============================================================
MODULE         := github.com/tijani-web/meridian
GO             := go
GOFLAGS        ?=
GOTEST         := $(GO) test -race -count=1
GOLANGCI_LINT  := golangci-lint
KIND           := kind
KUBECTL        := kubectl
HELM           := helm
DOCKER         := docker

# Image registry (CI builds and pushes; local builds use local tag)
REGISTRY       := ghcr.io/tijani-web
IMG_MANAGER    := $(REGISTRY)/meridian-manager
IMG_SCHEDULER  := $(REGISTRY)/meridian-scheduler
IMG_AGENT      := $(REGISTRY)/meridian-agent
IMG_ROUTER     := $(REGISTRY)/meridian-router
TAG            ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Kind cluster
KIND_CLUSTER   := meridian
KIND_CONFIG    := deploy/kind/config.yaml
KUBECONFIG_OUT := $(shell pwd)/kubeconfig-meridian

# Envtest
ENVTEST        := $(shell pwd)/bin/setup-envtest
ENVTEST_K8S_VER := 1.37.x

# ============================================================
# Default
# ============================================================
.DEFAULT_GOAL := help

.PHONY: help
help: ## Print this help message
	@echo "Meridian — GPU Fleet Control Plane"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} \
	     /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-25s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ============================================================
# Go: Generate, Build, Test, Lint
# ============================================================
.PHONY: generate
generate: controller-gen ## Run all code generators (deepcopy, CRD manifests, RBAC)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	$(CONTROLLER_GEN) crd rbac:roleName=meridian-manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac output:webhook:artifacts:config=config/webhook

.PHONY: manifests
manifests: controller-gen ## Regenerate CRD and RBAC manifests
	$(CONTROLLER_GEN) crd rbac:roleName=meridian-manager-role webhook paths="./..." \
	  output:crd:artifacts:config=config/crd/bases \
	  output:rbac:artifacts:config=config/rbac \
	  output:webhook:artifacts:config=config/webhook

.PHONY: install
install: ## Install CRDs into the K8s cluster using server-side apply
	$(KUBECTL) apply --server-side -f config/crd/bases/

.PHONY: uninstall
uninstall: ## Uninstall CRDs from the K8s cluster
	$(KUBECTL) delete -f config/crd/bases/

.PHONY: build
build: ## Build all binaries
	$(GO) build $(GOFLAGS) -o bin/meridian-manager     ./cmd/manager
	$(GO) build $(GOFLAGS) -C cmd/scheduler -o ../../bin/meridian-scheduler .
	$(GO) build $(GOFLAGS) -o bin/meridian-agent       ./cmd/agent
	$(GO) build $(GOFLAGS) -o bin/meridian-router      ./cmd/router
	$(GO) build $(GOFLAGS) -o bin/meridianctl          ./cmd/meridianctl

.PHONY: test
test: ## Run unit tests with race detector
	$(GOTEST) $$(go list ./... | grep -v /test/envtest)

.PHONY: test-envtest
test-envtest: envtest ## Run integration tests against a real apiserver+etcd via envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VER) --bin-path $(shell pwd)/bin/k8s -p path)" \
	$(GOTEST) -timeout 5m ./test/envtest/...

.PHONY: test-e2e
test-e2e: ## Run e2e tests on a real kind cluster (requires make kind-up first)
	KUBECONFIG=$(KUBECONFIG_OUT) $(GOTEST) -timeout 30m -v ./test/e2e/...

.PHONY: test-race
test-race: ## Explicit race-detector run for CI
	$(GOTEST) -race ./...

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run --timeout 10m ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Run gofmt
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Run go mod tidy
	$(GO) mod tidy

.PHONY: verify
verify: generate manifests tidy ## Verify generated files are up-to-date (for CI drift check)
	git diff --exit-code || (echo "Generated files are not up-to-date. Run 'make generate manifests tidy' and commit." && exit 1)

# ============================================================
# Images
# ============================================================
.PHONY: docker-build
docker-build: ## Build all Docker images (local)
	$(DOCKER) build -t $(IMG_MANAGER):$(TAG)   -f build/manager/Dockerfile   .
	$(DOCKER) build -t $(IMG_SCHEDULER):$(TAG) -f build/scheduler/Dockerfile .
	$(DOCKER) build -t $(IMG_AGENT):$(TAG)     -f build/agent/Dockerfile     .
	$(DOCKER) build -t $(IMG_ROUTER):$(TAG)    -f build/router/Dockerfile    .

.PHONY: docker-push
docker-push: docker-build ## Push images (CI only — do not run locally unless testing)
	$(DOCKER) push $(IMG_MANAGER):$(TAG)
	$(DOCKER) push $(IMG_SCHEDULER):$(TAG)
	$(DOCKER) push $(IMG_AGENT):$(TAG)
	$(DOCKER) push $(IMG_ROUTER):$(TAG)

# ============================================================
# Kind cluster
# ============================================================
.PHONY: kind-up
kind-up: ## Create the local kind cluster with topology labels
	$(KIND) create cluster --name $(KIND_CLUSTER) --config $(KIND_CONFIG) --kubeconfig $(KUBECONFIG_OUT)
	@echo "Cluster ready. Export: export KUBECONFIG=$(KUBECONFIG_OUT)"
	@echo "Apply topology labels..."
	KUBECONFIG=$(KUBECONFIG_OUT) ./deploy/kind/label-nodes.sh

.PHONY: kind-down
kind-down: ## Delete the local kind cluster
	$(KIND) delete cluster --name $(KIND_CLUSTER)
	@rm -f $(KUBECONFIG_OUT)

.PHONY: kind-load
kind-load: docker-build ## Load images into kind (avoids registry push during dev)
	$(KIND) load docker-image $(IMG_MANAGER):$(TAG)   --name $(KIND_CLUSTER)
	$(KIND) load docker-image $(IMG_SCHEDULER):$(TAG) --name $(KIND_CLUSTER)
	$(KIND) load docker-image $(IMG_AGENT):$(TAG)     --name $(KIND_CLUSTER)
	$(KIND) load docker-image $(IMG_ROUTER):$(TAG)    --name $(KIND_CLUSTER)

# ============================================================
# Helm
# ============================================================
.PHONY: helm-lint
helm-lint: ## Lint the Meridian Helm chart
	$(HELM) lint charts/meridian

.PHONY: helm-install
helm-install: ## Install Meridian via Helm on the active cluster
	$(HELM) upgrade --install meridian charts/meridian \
	  --namespace meridian-system --create-namespace \
	  --set image.tag=$(TAG) \
	  --kubeconfig $(KUBECONFIG_OUT) \
	  --wait

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall Meridian from the cluster
	$(HELM) uninstall meridian --namespace meridian-system --kubeconfig $(KUBECONFIG_OUT)

# ============================================================
# Benchmarks
# ============================================================
.PHONY: bench
bench: ## Run all benchmarks (requires kind-up and KWOK deployed)
	@echo "Running scheduler/queue benchmarks..."
	./bench/scripts/run-scheduler-bench.sh
	@echo "Running fault tolerance benchmarks..."
	./bench/scripts/run-fault-bench.sh
	@echo "Running router benchmarks..."
	./bench/scripts/run-router-bench.sh
	@echo "Generating plots..."
	./bench/scripts/plot-results.sh

# ============================================================
# Tools
# ============================================================
CONTROLLER_GEN := $(shell pwd)/bin/controller-gen

.PHONY: controller-gen
controller-gen: ## Download controller-gen if not present
	@test -f $(CONTROLLER_GEN) || GOBIN=$(shell pwd)/bin $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

.PHONY: envtest
envtest: ## Download setup-envtest if not present
	@test -f $(ENVTEST) || GOBIN=$(shell pwd)/bin $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: golangci-lint-install
golangci-lint-install: ## Install golangci-lint if not present
	@which golangci-lint || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin

# ============================================================
# CI check (stub until Phase 01 fills these in)
# ============================================================
.PHONY: ci
ci: vet lint test verify helm-lint ## Run all CI checks locally

# ============================================================
# Cleanup
# ============================================================
.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/ dist/
