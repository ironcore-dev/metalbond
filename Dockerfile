FROM golang:1.18-bullseye as builder

WORKDIR /workspace

COPY . .
RUN make

FROM debian:bullseye-slim
WORKDIR /opt/metalbond

COPY --from=builder /workspace/target/metalbond /opt/metalbond/bin/metalbond
COPY --from=builder /workspace/target/html /opt/metalbond/html

ENTRYPOINT [ "/opt/metalbond/bin/metalbond" ]