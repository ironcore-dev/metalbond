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
	rm -rf target/html && cp -ra html target
	cd cmd && go build -ldflags "-X github.com/onmetal/metalbond.METALBOND_VERSION=$(METALBOND_VERSION)" -o ../target/metalbond_amd64

arm64:
	mkdir -p target
	rm -rf target/html && cp -ra html target
	cd cmd && env GOOS=linux GOARCH=arm64 go build -ldflags "-X github.com/onmetal/metalbond.METALBOND_VERSION=$(METALBOND_VERSION)" -o ../target/metalbond_arm64

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

deb:
	docker run --rm -v "$(PWD):/workdir" -e "METALBOND_VERSION=$(METALBOND_VERSION)" -e "ARCHITECTURE=amd64" golang:1.18-bullseye bash -c "cd /workdir && debian/make-deb.sh"
	docker run --rm -v "$(PWD):/workdir" -e "METALBOND_VERSION=$(METALBOND_VERSION)" -e "ARCHITECTURE=arm64" golang:1.18-bullseye bash -c "cd /workdir && debian/make-deb.sh"

unit-test:
	go test -v

fmt: ## Run go fmt against code.
	go fmt ./...

addlicense: ## Add license headers to all go files.
	find . -name '*.go' -exec go run github.com/google/addlicense -c 'OnMetal authors' {} +

.PHONY: checklicense
checklicense: ## Check that every file has a license header present.
	find . -name '*.go' -exec go run github.com/google/addlicense  -check -c 'OnMetal authors' {} +

lint: ## Run golangci-lint against code.
	golangci-lint run ./...

test:
	go test ./... -coverprofile cover.out

check: manifests generate fmt addlicense lint test ## Lint and run tests.

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
