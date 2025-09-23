# Build the manager binary
FROM --platform=$BUILDPLATFORM registry-cn-hangzhou.ack.aliyuncs.com/dev/golang:1.24.1 as builder
ARG TARGETOS
ARG TARGETARCH

ENV GOPROXY=https://proxy.golang.com.cn,https://goproxy.cn,direct
ENV GOPRIVATE=*gitlab.alibaba-inc.com
ENV GOSUMDB=off

WORKDIR /workspace
ADD . /workspace

RUN make build

FROM registry.cn-hangzhou.aliyuncs.com/acs/alpine:3.18-update
WORKDIR /
COPY --from=builder /workspace/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
