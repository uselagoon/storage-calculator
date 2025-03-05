SHELL := /bin/bash

# Image URL to use all building/pushing image targets
IMG ?= controller:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

KIND_CLUSTER ?= storage-calculator
KIND_NETWORK ?= storage-controller

KIND_VERSION = v0.25.0
KUBECTL_VERSION := v1.31.0
HELM_VERSION := v3.16.1
GOJQ_VERSION = v0.12.16
KUSTOMIZE_VERSION := v5.4.3

KUBECTL = $(realpath ./local-dev/kubectl)
KIND = $(realpath ./local-dev/kind)
KUSTOMIZE = $(realpath ./local-dev/kustomize)
HELM = $(realpath ./local-dev/helm)
JQ = $(realpath ./local-dev/jq)

ARCH := $(shell uname | tr '[:upper:]' '[:lower:]')


.PHONY: local-dev/kind
local-dev/kind:
ifeq ($(KIND_VERSION), $(shell kind version 2>/dev/null | sed -nE 's/kind (v[0-9.]+).*/\1/p'))
	$(info linking local kind version $(KIND_VERSION))
	ln -sf $(shell command -v kind) ./local-dev/kind
else
ifneq ($(KIND_VERSION), $(shell ./local-dev/kind version 2>/dev/null | sed -nE 's/kind (v[0-9.]+).*/\1/p'))
	$(info downloading kind version $(KIND_VERSION) for $(ARCH))
	mkdir -p local-dev
	rm local-dev/kind || true
	curl -sSLo local-dev/kind https://kind.sigs.k8s.io/dl/$(KIND_VERSION)/kind-$(ARCH)-amd64
	chmod a+x local-dev/kind
endif
endif

.PHONY: local-dev/kustomize
local-dev/kustomize:
ifeq ($(KUSTOMIZE_VERSION), $(shell kustomize version 2>/dev/null | sed -nE 's/(v[0-9.]+).*/\1/p'))
	$(info linking local kustomize version $(KUSTOMIZE_VERSION))
	ln -sf $(shell command -v kind) ./local-dev/kind
else
ifneq ($(KUSTOMIZE_VERSION), $(shell ./local-dev/kustomize version 2>/dev/null | sed -nE 's/(v[0-9.]+).*/\1/p'))
	$(info downloading kustomize version $(KUSTOMIZE_VERSION) for $(ARCH))
	rm local-dev/kustomize || true
	curl -sSL https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F$(KUSTOMIZE_VERSION)/kustomize_$(KUSTOMIZE_VERSION)_$(ARCH)_amd64.tar.gz | tar -xzC local-dev
	chmod a+x local-dev/kustomize
endif
endif

.PHONY: local-dev/kubectl
local-dev/kubectl:
ifeq ($(KUBECTL_VERSION), $(shell kubectl version --client 2>/dev/null | grep Client | sed -E 's/Client Version: (v[0-9.]+).*/\1/'))
	$(info linking local kubectl version $(KUBECTL_VERSION))
	ln -sf $(shell command -v kubectl) ./local-dev/kubectl
else
ifneq ($(KUBECTL_VERSION), $(shell ./local-dev/kubectl version --client 2>/dev/null | grep Client | sed -E 's/Client Version: (v[0-9.]+).*/\1/'))
	$(info downloading kubectl version $(KUBECTL_VERSION) for $(ARCH))
	rm local-dev/kubectl || true
	curl -sSLo local-dev/kubectl https://storage.googleapis.com/kubernetes-release/release/$(KUBECTL_VERSION)/bin/$(ARCH)/amd64/kubectl
	chmod a+x local-dev/kubectl
endif
endif

.PHONY: local-dev/helm
local-dev/helm:
ifeq ($(HELM_VERSION), $(shell helm version --short --client 2>/dev/null | sed -nE 's/(v[0-9.]+).*/\1/p'))
	$(info linking local helm version $(HELM_VERSION))
	ln -sf $(shell command -v helm) ./local-dev/helm
else
ifneq ($(HELM_VERSION), $(shell ./local-dev/helm version --short --client 2>/dev/null | sed -nE 's/(v[0-9.]+).*/\1/p'))
	$(info downloading helm version $(HELM_VERSION) for $(ARCH))
	rm local-dev/helm || true
	curl -sSL https://get.helm.sh/helm-$(HELM_VERSION)-$(ARCH)-amd64.tar.gz | tar -xzC local-dev --strip-components=1 $(ARCH)-amd64/helm
	chmod a+x local-dev/helm
endif
endif

.PHONY: local-dev/jq
local-dev/jq:
ifeq ($(GOJQ_VERSION), $(shell gojq -v 2>/dev/null | sed -nE 's/gojq ([0-9.]+).*/v\1/p'))
	$(info linking local gojq version $(GOJQ_VERSION))
	ln -sf $(shell command -v gojq) ./local-dev/jq
else
ifneq ($(GOJQ_VERSION), $(shell ./local-dev/jq -v 2>/dev/null | sed -nE 's/gojq ([0-9.]+).*/v\1/p'))
	$(info downloading gojq version $(GOJQ_VERSION) for $(ARCH))
	rm local-dev/jq || true
ifeq ($(ARCH), darwin)
	TMPDIR=$$(mktemp -d) \
		&& curl -sSL https://github.com/itchyny/gojq/releases/download/$(GOJQ_VERSION)/gojq_$(GOJQ_VERSION)_$(ARCH)_arm64.zip -o $$TMPDIR/gojq.zip \
		&& (cd $$TMPDIR && unzip gojq.zip) && cp $$TMPDIR/gojq_$(GOJQ_VERSION)_$(ARCH)_arm64/gojq ./local-dev/jq && rm -rf $$TMPDIR
else
	curl -sSL https://github.com/itchyny/gojq/releases/download/$(GOJQ_VERSION)/gojq_$(GOJQ_VERSION)_$(ARCH)_amd64.tar.gz | tar -xzC local-dev --strip-components=1 gojq_$(GOJQ_VERSION)_$(ARCH)_amd64/gojq
	mv ./local-dev/{go,}jq
endif
	chmod a+x local-dev/jq
endif
endif

.PHONY: local-dev/tools
local-dev/tools: local-dev/kind local-dev/kustomize local-dev/kubectl local-dev/helm local-dev/jq

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.29.0
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
ENVTEST_VERSION ?= latest

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
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

.PHONY: manifests
manifests: controller-gen local-dev/tools ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role webhook paths="./..."

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: test ## Build docker image with the manager.
	docker build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests local-dev/kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.

.PHONY: uninstall
uninstall: manifests local-dev/kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.

.PHONY: deploy
deploy: manifests local-dev/kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: create-kind-cluster
create-kind-cluster: local-dev/tools 
	docker network inspect $(KIND_NETWORK) >/dev/null || docker network create $(KIND_NETWORK) \
		&& export KIND_EXPERIMENTAL_DOCKER_NETWORK=$(KIND_NETWORK) \
 		&& $(KIND) create cluster --wait=60s --name=$(KIND_CLUSTER)

# generate-broker-certs will generate a ca, server and client certificate used for the test suite
.PHONY: generate-broker-certs
generate-broker-certs:
	@mkdir -p local-dev/certificates
	openssl x509 -enddate -noout -in local-dev/certificates/ca.crt > /dev/null 2>&1 || \
    (openssl genrsa -out local-dev/certificates/ca.key 4096 && \
	openssl req -x509 -new -nodes -key local-dev/certificates/ca.key -sha256 -days 1826 -out local-dev/certificates/ca.crt -subj '/CN=lagoon Root CA/C=IO/ST=State/L=City/O=lagoon' && \
    openssl ecparam -name prime256v1 -genkey -noout -out local-dev/certificates/tls.key && \
	chmod +r local-dev/certificates/tls.key && \
	echo "subjectAltName = IP:172.17.0.1" > local-dev/certificates/extfile.cnf && \
	openssl req -new -nodes -out local-dev/certificates/tls.csr -key local-dev/certificates/tls.key -subj '/CN=broker/C=IO/ST=State/L=City/O=lagoon' && \
    openssl x509 -req -in local-dev/certificates/tls.csr -CA local-dev/certificates/ca.crt -extfile local-dev/certificates/extfile.cnf -CAkey local-dev/certificates/ca.key -CAcreateserial -out local-dev/certificates/tls.crt -days 730 -sha256 && \
    openssl ecparam -name prime256v1 -genkey -noout -out local-dev/certificates/clienttls.key && \
	chmod +r local-dev/certificates/clienttls.key && \
	openssl req -new -nodes -out local-dev/certificates/clienttls.csr -key local-dev/certificates/clienttls.key -subj '/CN=client/C=IO/ST=State/L=City/O=lagoon' && \
    openssl x509 -req -in local-dev/certificates/clienttls.csr -CA local-dev/certificates/ca.crt -CAkey local-dev/certificates/ca.key -CAcreateserial -out local-dev/certificates/clienttls.crt -days 730 -sha256)

.PHONY: regenerate-broker-certs
regenerate-broker-certs:
	@mkdir -p local-dev/certificates
	@rm local-dev/certificates/ca.key || true && \
	rm local-dev/certificates/ca.crt || true && \
	$(MAKE) generate-broker-certs

# Create a kind cluster locally and run the test e2e test suite against it
.PHONY: kind/test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up locally
kind/test-e2e: create-kind-cluster kind/re-test-e2e
	
.PHONY: local-kind/test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up locally
kind/re-test-e2e:
	export KIND_PATH=$(KIND) && \
	export KUBECTL_PATH=$(KUBECTL) && \
	export KIND_CLUSTER=$(KIND_CLUSTER) && \
	$(KIND) export kubeconfig --name=$(KIND_CLUSTER) && \
	$(MAKE) test-e2e

.PHONY: clean
kind/clean:
	$(KIND) delete cluster --name=$(KIND_CLUSTER) && docker network rm $(KIND_NETWORK)

# Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
.PHONY: test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up inside github action.
test-e2e: local-dev/tools generate-broker-certs
	($(KUBECTL) create namespace storage-calculator-system || echo "namespace exists") && \
	($(KUBECTL) -n storage-calculator-system delete secret lagoon-broker-tls || echo "lagoon-broker-tls doesn't exist, ignoring") && \
	$(KUBECTL) -n storage-calculator-system create secret generic lagoon-broker-tls --from-file=tls.crt=local-dev/certificates/clienttls.crt --from-file=tls.key=local-dev/certificates/clienttls.key --from-file=ca.crt=local-dev/certificates/ca.crt && \
	go test ./test/e2e/ -v -ginkgo.v

.PHONY: kind/set-kubeconfig
kind/set-kubeconfig:
	export KIND_CLUSTER=$(KIND_CLUSTER) && \
	$(KIND) export kubeconfig --name=$(KIND_CLUSTER)

.PHONY: kind/logs-controller
kind/logs-controller:
	export KIND_CLUSTER=$(KIND_CLUSTER) && \
	$(KIND) export kubeconfig --name=$(KIND_CLUSTER) && \
	$(KUBECTL) -n storage-calculator-system logs -f \
		$$($(KUBECTL) -n storage-calculator-system  get pod -l control-plane=controller-manager -o jsonpath="{.items[0].metadata.name}") \
		-c manager
##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# find or download controller-gen
# download controller-gen if necessary
controller-gen:
ifeq (, $(shell which controller-gen))
	@{ \
	set -e ;\
	CONTROLLER_GEN_TMP_DIR=$$(mktemp -d) ;\
	cd $$CONTROLLER_GEN_TMP_DIR ;\
	go mod init tmp ;\
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5 ;\
	rm -rf $$CONTROLLER_GEN_TMP_DIR ;\
	}
CONTROLLER_GEN=$(GOBIN)/controller-gen
else
CONTROLLER_GEN=$(shell which controller-gen)
endif
