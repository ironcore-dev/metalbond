FROM --platform=$BUILDPLATFORM golang:1.22-bullseye AS builder

ARG DEBIAN_FRONTEND=noninteractive
ARG GOARCH=''

RUN apt-get update && apt-get install -y libpcap-dev

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    go mod download

COPY cmd cmd
COPY html html
COPY pb pb
COPY *.go ./

ARG TARGETOS
ARG TARGETARCH

# Build
ARG METALBOND_VERSION
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -buildvcs=false -ldflags "-X github.com/ironcore-dev/metalbond.METALBOND_VERSION=$METALBOND_VERSION" -o metalbond cmd/cmd.go

FROM debian:bullseye-slim

RUN apt-get update && apt-get install -y iproute2 ethtool wget adduser inetutils-ping libpcap-dev && rm -rf /var/lib/apt/lists/*
COPY --from=builder /workspace/metalbond /usr/sbin/metalbond
COPY --from=builder /workspace/html /usr/share/metalbond/html

RUN echo -e "254\tmetalbond" >> "/etc/iproute2/rt_protos"
