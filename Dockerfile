# Build Stage: using Go 1.24.1 image
FROM quay.io/projectquay/golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

# Install build tools
# The builder is based on UBI8, so we need epel-release-8.
RUN dnf install -y 'https://dl.fedoraproject.org/pub/epel/epel-release-latest-8.noarch.rpm' && \
    dnf install -y gcc-c++ libstdc++ libstdc++-devel clang zeromq-devel pkgconfig python3.12-devel python3.12-pip && \
    dnf clean all

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/

# Install Python dependencies for chat completions preprocessing
# Download requirements from the kv-cache-manager module
RUN mkdir -p /tmp/req && \
    curl -L https://raw.githubusercontent.com/llm-d/llm-d-kv-cache-manager/v0.3.2/pkg/preprocessing/chat_completions/requirements.txt -o /tmp/req/requirements.txt && \
    python3.12 -m pip install --upgrade pip setuptools wheel && \
    python3.12 -m pip install -r /tmp/req/requirements.txt && \
    rm -rf /tmp/req

# Set up Python environment variables needed for the build
ENV PYTHONPATH=/usr/lib64/python3.12/site-packages:/usr/lib/python3.12/site-packages
ENV PYTHON=python3.12

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
RUN export CGO_CFLAGS="$(python3.12-config --cflags) -I/workspace/lib" && \
    export CGO_LDFLAGS="$(python3.12-config --ldflags --embed) -L/workspace/lib -ltokenizers -ldl -lm" && \
    go build -a -o bin/epp -ldflags="-extldflags '-L$(pwd)/lib' -X sigs.k8s.io/gateway-api-inference-extension/version.CommitSHA=${COMMIT_SHA} -X sigs.k8s.io/gateway-api-inference-extension/version.BuildRef=${BUILD_REF}" cmd/epp/main.go

# Use alpine as a minimal base image to package the manager binary
# Using alpine instead of UBI9 to avoid blob permission issues
FROM alpine:latest
WORKDIR /
COPY --from=builder /workspace/bin/epp /app/epp

# Note: Python chat completions preprocessing is handled by llm-d-kv-cache-manager v0.3.2
# The module is now available upstream and doesn't require local copies

# Install zeromq runtime library and Python 3.12 runtime needed for chat completions preprocessing
# The final image is UBI9, so we need epel-release-9.
USER root
RUN microdnf install -y dnf && \
    dnf install -y 'https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm' && \
    dnf install -y zeromq python3.12 python3.12-libs python3.12-pip && \
    dnf clean all && \
    rm -rf /var/cache/dnf /var/lib/dnf

# Copy Python dependencies from builder stage (includes torch, transformers, etc)
# These are installed during build, no need to reinstall in runtime
COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages

# Set Hugging Face cache directory to writable location for non-root user
ENV HF_HOME="/tmp/.cache"

USER 65532:65532

# expose gRPC, health and metrics ports
EXPOSE 9002
EXPOSE 9003
EXPOSE 9090

# expose port for KV-Events ZMQ SUB socket
EXPOSE 5557

ENTRYPOINT ["/app/epp"]
