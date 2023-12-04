# Image URL to use all building/pushing image targets
IMG ?= controller:latest
BUILDARGS ?=

DEB_BUILD_CONTAINER ?= golang:1.20-bookworm

ifneq ("$(wildcard ./version)","")
	METALBOND_VERSION?=$(shell cat ./version)
else ifeq ($(shell git describe --exact-match --tags 2> /dev/null),)
	METALBOND_VERSION?=$(shell git rev-parse --short HEAD)
else
	METALBOND_VERSION?=$(shell (git describe --exact-match --tags 2> /dev/null | cut -dv -f2))
endif

ARCHITECTURE=$(shell dpkg --print-architecture)

all:
	make $(ARCHITECTURE)
	cp ./target/metalbond_$(ARCHITECTURE) ./target/metalbond

amd64:
	mkdir -p target
	rm -rf target/html && cp -a html target
	cd cmd && go build -buildvcs=false -ldflags "-X github.com/onmetal/metalbond.METALBOND_VERSION=$(METALBOND_VERSION)" -o ../target/metalbond_amd64

arm64:
	mkdir -p target
	rm -rf target/html && cp -ra html target
	cd cmd && env GOOS=linux GOARCH=arm64 go build -buildvcs=false -ldflags "-X github.com/onmetal/metalbond.METALBOND_VERSION=$(METALBOND_VERSION)" -o ../target/metalbond_arm64

tarball:
	mkdir -p target
	rsync -a ./* target/metalbond-$(METALBOND_VERSION)/ --exclude target/
#	echo $(METALBOND_VERSION) > target/metalbond-$(METALBOND_VERSION)/version
	cd target && tar -czf metalbond_$(METALBOND_VERSION).orig.tar.gz metalbond-$(METALBOND_VERSION)
	rm -rf target/metalbond-$(METALBOND_VERSION)/

run-server: all
	cd target && ./metalbond server \
		--listen [::]:4711 \
		--http [::]:4712 \
		--keepalive 3

run-client1: all
	cd target && sudo ./metalbond client \
		--server [::1]:4711 \
		--keepalive 2 \
		--subscribe 23 \
		--announce 23#2001:db8:1::/48#2001:db8::cafe \
		--announce 23#192.168.0.0/16#2001:db8::cafe \
		--install-routes 23#100 \
		--tun overlay-tun

run-client1b: all
	cd target && ./metalbond client \
		--server [::1]:4711 \
		--keepalive 2 \
		--subscribe 23 \
		--announce 23#2001:db8:1::/48#2001:db8::cafe \
		--announce 23#192.168.0.0/16#2001:db8::cafe

run-client2: all
	cd target && ./metalbond client \
		--server [::1]:4711 \
		--keepalive 2 \
		--subscribe 23 \
		--subscribe 42 \
		--announce 23#2001:db8:1::/48#2001:db8::cafb \
		--announce 23#2001:db8:2::/48#2001:db8::2:beef \
		--announce 23#2001:db8:3::/48#2001:db8::3:beef \
		--announce 23#2001:db8:4::/48#2001:db8::4:beef \
		--announce 23#2001:db8:4::/48#2001:db8::4a:beef \
		--announce 23#2001:db8:4::/48#2001:db8::4b:beef \
		--announce 23#2001:db8:4::/48#2001:db8::4c:beef \
		--announce 42#10.0.0.0/8#2001:db8::beef

.PHONY: proto deb
proto:
	protoc -I ./pb --go_out=. ./pb/metalbond.proto

clean:
	rm -rf target

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build $(BUILDARGS) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

deb:
	docker run --rm -v "$(PWD):/workdir" -e "METALBOND_VERSION=$(METALBOND_VERSION)" -e "ARCHITECTURE=amd64" $(DEB_BUILD_CONTAINER)  bash -c "cd /workdir && debian/make-deb.sh"
	docker run --rm -v "$(PWD):/workdir" -e "METALBOND_VERSION=$(METALBOND_VERSION)" -e "ARCHITECTURE=arm64" $(DEB_BUILD_CONTAINER)  bash -c "cd /workdir && debian/make-deb.sh"

unit-test:
	go test -v

.PHONY: fmt
fmt: goimports ## Run goimports against code.
	$(GOIMPORTS) -w .

.PHONY: add-license
add-license: addlicense ## Add license headers to all go files.
	find . -name '*.go' -exec $(ADDLICENSE) -f hack/license-header.txt {} +

.PHONY: check-license
check-license: addlicense ## Check that every file has a license header present.
	find . -name '*.go' -exec $(ADDLICENSE) -check -c 'IronCore authors' {} +

.PHONY: lint
lint: golangci-lint ## Run golangci-lint on the code.
	$(GOLANGCI_LINT) run ./...

test:
	go test ./... -coverprofile cover.out

check: manifests generate fmt check-license lint test ## Lint and run tests.

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Tools

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
ADDLICENSE ?= $(LOCALBIN)/addlicense
GOIMPORTS ?= $(LOCALBIN)/goimports
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

## Tool Versions
ADDLICENSE_VERSION ?= v1.1.1
GOIMPORTS_VERSION ?= v0.13.0
GOLANGCI_LINT_VERSION ?= v1.55.2

.PHONY: addlicense
addlicense: $(ADDLICENSE) ## Download addlicense locally if necessary.
$(ADDLICENSE): $(LOCALBIN)
	test -s $(LOCALBIN)/addlicense || GOBIN=$(LOCALBIN) go install github.com/google/addlicense@$(ADDLICENSE_VERSION)

.PHONY: goimports
goimports: $(GOIMPORTS) ## Download goimports locally if necessary.
$(GOIMPORTS): $(LOCALBIN)
	test -s $(LOCALBIN)/goimports || GOBIN=$(LOCALBIN) go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	test -s $(LOCALBIN)/golangci-lint || GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
