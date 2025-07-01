# Build Stage: using Go 1.24.1 image
FROM quay.io/projectquay/golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

# Install build tools
RUN dnf install -y gcc-c++ libstdc++ libstdc++-devel clang && dnf clean all

WORKDIR /workspace

# FOR LOCAL BUILDING
# This is to build from ../ with llm-d-kv-cache-manager checked out at ../
# Copy the Go Modules manifests
COPY llm-d-inference-scheduler/go.mod go.mod
COPY llm-d-inference-scheduler/go.sum go.sum

# FOR LOCAL BUILDING
# This is to build from ../ with llm-d-kv-cache-manager checked out at ../
# Copy the go source
COPY llm-d-inference-scheduler/cmd/ cmd/
COPY llm-d-inference-scheduler/pkg/ pkg/

# FOR LOCAL BUILDING
# This is to build from ../ with llm-d-kv-cache-manager checked out at ../
# Copy the local kv-cache-manager for the replace directive
COPY llm-d-kv-cache-manager/ llm-d-kv-cache-manager/
RUN go mod edit -replace github.com/llm-d/llm-d-kv-cache-manager=./llm-d-kv-cache-manager

# HuggingFace tokenizer bindings
RUN mkdir -p lib
RUN curl -L https://github.com/daulet/tokenizers/releases/download/v1.20.2/libtokenizers.${TARGETOS}-${TARGETARCH}.tar.gz | tar -xz -C lib
RUN ranlib lib/*.a

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make image-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
ENV CGO_ENABLED=1
ENV GOOS=${TARGETOS:-linux}
ENV GOARCH=${TARGETARCH}
RUN go build -a -o bin/epp -ldflags="-extldflags '-L$(pwd)/lib'" cmd/epp/main.go

# Use ubi9 as a minimal base image to package the manager binary
# Refer to https://catalog.redhat.com/software/containers/ubi9/ubi-minimal/615bd9b4075b022acc111bf5 for more details
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/bin/epp /app/epp
USER 65532:65532

# expose gRPC, health and metrics ports
EXPOSE 9002
EXPOSE 9003
EXPOSE 9090

ENTRYPOINT ["/app/epp"]
