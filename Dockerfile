FROM golang:1.22-bullseye AS builder

WORKDIR /workspace
COPY cmd cmd
COPY html html
COPY pb pb
COPY .git .git
COPY Makefile .
COPY go.mod .
COPY go.sum .
COPY *.go ./

RUN make amd64

FROM debian:bullseye-slim

RUN apt-get update && apt-get install -y iproute2 ethtool wget adduser inetutils-ping && rm -rf /var/lib/apt/lists/*
COPY --from=builder /workspace/target/metalbond_amd64 /usr/sbin/metalbond
COPY --from=builder /workspace/target/html /usr/share/metalbond/html

RUN echo -e "254\tmetalbond" >> "/etc/iproute2/rt_protos"
