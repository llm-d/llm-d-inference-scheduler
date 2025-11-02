# Build Stage: using Go 1.24.1 image
FROM quay.io/projectquay/golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

# Install build tools
# The builder is based on UBI8, so we need epel-release-8.
RUN dnf install -y 'https://dl.fedoraproject.org/pub/epel/epel-release-latest-8.noarch.rpm' && \
    dnf install -y gcc-c++ libstdc++ libstdc++-devel clang zeromq-devel pkgconfig && \
    dnf clean all

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/

# HuggingFace tokenizer bindings
RUN mkdir -p lib
# Ensure that the RELEASE_VERSION matches the one used in the imported llm-d-kv-cache-manager version
ARG RELEASE_VERSION=v1.22.1
RUN curl -L https://github.com/daulet/tokenizers/releases/download/${RELEASE_VERSION}/libtokenizers.${TARGETOS}-${TARGETARCH}.tar.gz | tar -xz -C lib
RUN ranlib lib/*.a

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make image-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
ENV CGO_ENABLED=1
ENV GOOS=${TARGETOS:-linux}
ENV GOARCH=${TARGETARCH}
ARG COMMIT_SHA=unknown
ARG BUILD_REF
RUN go build -a -o bin/epp -ldflags="-extldflags '-L$(pwd)/lib' -X sigs.k8s.io/gateway-api-inference-extension/version.CommitSHA=${COMMIT_SHA} -X sigs.k8s.io/gateway-api-inference-extension/version.BuildRef=${BUILD_REF}" cmd/epp/main.go

# Use ubi9 as a minimal base image to package the manager binary
# Refer to https://catalog.redhat.com/software/containers/ubi9/ubi-minimal/615bd9b4075b022acc111bf5 for more details
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/bin/epp /app/epp

# Install zeromq runtime library needed by the manager.
# The final image is UBI9, so we need epel-release-9.
USER root
RUN microdnf install -y dnf && \
    dnf install -y 'https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm' && \
    dnf install -y zeromq && \
    dnf clean all && \
    rm -rf /var/cache/dnf /var/lib/dnf

USER 65532:65532

# expose gRPC, health and metrics ports
EXPOSE 9002
EXPOSE 9003
EXPOSE 9090

# expose port for KV-Events ZMQ SUB socket
EXPOSE 5557

ENTRYPOINT ["/app/epp"]
