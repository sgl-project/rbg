# CRD Upgrader image for rbgs
#
# This image contains all CRD files and a Go binary (compiled from the same
# toolchain and dependency tree as the manager) that applies CRDs during
# Helm upgrade. This replaces the previous kubectl-based approach, eliminating
# the pre-built kubectl binary and its pinned internal dependencies (the
# source of the residual CVEs in the crd-upgrade image).

FROM --platform=$BUILDPLATFORM registry-cn-hangzhou.ack.aliyuncs.com/dev/golang:1.26.6 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
ADD . /workspace

# Build a static binary (CGO disabled for musl/alpine compatibility).
# -mod vendor uses the same vendored dependency tree as the manager binary.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -mod vendor -o crd-upgrader ./tools/crd-upgrade/

# ---
# Final image: Alpine with the compiled binary and CRD files.
FROM registry.cn-hangzhou.aliyuncs.com/acs/alpine:3.18-update
WORKDIR /
COPY --from=builder /workspace/crd-upgrader /usr/local/bin/crd-upgrader
COPY config/crd/bases /rbgs/crds
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/crd-upgrader"]
