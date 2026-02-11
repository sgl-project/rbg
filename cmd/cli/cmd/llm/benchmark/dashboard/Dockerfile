# Build the benchmark-dashboard binary
FROM golang:1.24 as builder
ARG TARGETOS
ARG TARGETARCH

ARG GOPROXY
ARG GOPRIVATE
ARG GOSUMDB

ENV GOPROXY=${GOPROXY} \
    GOPRIVATE=${GOPRIVATE} \
    GOSUMDB=${GOSUMDB}

WORKDIR /workspace

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY cmd/cli/cmd/llm/benchmark/dashboard/ ./dashboard/

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -a -o benchmark-dashboard ./dashboard/

# Use distroless as minimal base image
FROM alpine:3.22

WORKDIR /
COPY --from=builder /workspace/benchmark-dashboard /benchmark-dashboard
USER 65532:65532

ENTRYPOINT ["/benchmark-dashboard"]
