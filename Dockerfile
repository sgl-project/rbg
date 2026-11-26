# Build the manager binary
FROM --platform=$BUILDPLATFORM registry-cn-hangzhou.ack.aliyuncs.com/dev/golang:1.26.6 AS builder
ARG TARGETOS
ARG TARGETARCH

ARG GOPROXY
ARG GOPRIVATE
ARG GOSUMDB

ENV GOPROXY=${GOPROXY} \
    GOPRIVATE=${GOPRIVATE} \
    GOSUMDB=${GOSUMDB}

WORKDIR /workspace
ADD . /workspace

RUN make build
RUN make build-cli

# The manager's preStop hook runs /bin/sleep; keep a sleep binary if you change the base image.
FROM registry.cn-hangzhou.aliyuncs.com/acs/alpine:3.18-update
WORKDIR /
COPY --from=builder /workspace/bin/manager .
COPY --from=builder /workspace/bin/kubectl-rbg .
USER 65532:65532

ENTRYPOINT ["/manager"]
