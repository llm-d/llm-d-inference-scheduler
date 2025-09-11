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
COPY gateway-api-inference-extension/ gateway-api-inference-extension/
COPY llm-d-kv-cache-manager/ llm-d-kv-cache-manager/

# Install Python dependencies for chat completions preprocessing
COPY llm-d-kv-cache-manager/pkg/preprocessing/chat_completions/requirements.txt ./requirements.txt
RUN python3.12 -m pip install --upgrade pip setuptools wheel && \
    python3.12 -m pip install -r ./requirements.txt

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

# Set up Python environment variables needed for the build
ENV PYTHONPATH=/workspace/pkg/preprocessing/chat_completions:/usr/lib64/python3.12/site-packages:/usr/lib/python3.12/site-packages
ENV PYTHON=python3.12
ARG COMMIT_SHA=unknown
ARG BUILD_REF
RUN export CGO_CFLAGS="$(python3.12-config --cflags) -I/workspace/lib" && \
    export CGO_LDFLAGS="$(python3.12-config --ldflags --embed) -L/workspace/lib -ltokenizers -ldl -lm" && \
    go build -a -o bin/epp -ldflags="-extldflags '-L$(pwd)/lib' -X sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics.CommitSHA=${COMMIT_SHA} -X sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics.BuildRef=${BUILD_REF}" cmd/epp/main.go

# Use ubi9 as a minimal base image to package the manager binary
# Refer to https://catalog.redhat.com/software/containers/ubi9/ubi-minimal/615bd9b4075b022acc111bf5 for more details
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/bin/epp /app/epp

# Copy Python wrapper files needed for chat template processing to a standard location
RUN mkdir -p /opt/chat_completions/
COPY --from=builder /workspace/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions/*.py /opt/chat_completions/

# Install zeromq runtime library and Python 3.12 runtime needed by the manager.
# The final image is UBI9, so we need epel-release-9.
USER root
RUN microdnf install -y dnf && \
    dnf install -y 'https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm' && \
    dnf install -y zeromq python3.12 python3.12-libs python3.12-pip && \
    dnf clean all

# Copy Python dependencies from builder stage
COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=builder /usr/lib/python3.12/site-packages /usr/lib/python3.12/site-packages
COPY --from=builder /usr/lib64/python3.12/site-packages /usr/lib64/python3.12/site-packages

# Install Python dependencies for chat completions preprocessing in runtime
COPY --from=builder /workspace/requirements.txt /tmp/requirements.txt
RUN python3.12 -m pip install --upgrade pip setuptools wheel && \
    python3.12 -m pip install -r /tmp/requirements.txt && \
    rm /tmp/requirements.txt

# Set PYTHONPATH to include the chat completions wrapper directory
ENV PYTHONPATH="/opt/chat_completions:${PYTHONPATH}"

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
