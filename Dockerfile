## Minimal runtime Dockerfile (microdnf-only, no torch, wrapper in site-packages)
# Build stage
FROM quay.io/projectquay/golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

RUN dnf install -y 'https://dl.fedoraproject.org/pub/epel/epel-release-latest-8.noarch.rpm' && \
    dnf install -y gcc-c++ libstdc++ libstdc++-devel clang zeromq-devel pkgconfig python3.12-devel python3.12-pip git && \
    dnf clean all

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum
COPY cmd/ cmd/
COPY pkg/ pkg/

# Fetch requirements only (install in runtime)
RUN curl -L https://raw.githubusercontent.com/llm-d/llm-d-kv-cache-manager/v0.3.2/pkg/preprocessing/chat_completions/requirements.txt -o /workspace/requirements.txt

# Prepare Python wrapper file for runtime
RUN mkdir -p /tmp/kv-cache-manager && \
    cd /tmp/kv-cache-manager && \
    git clone --depth 1 --branch v0.3.2 https://github.com/llm-d/llm-d-kv-cache-manager.git . && \
    mkdir -p /workspace/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions && \
    cp pkg/preprocessing/chat_completions/render_jinja_template_wrapper.py /workspace/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions/ && \
    rm -rf /tmp/kv-cache-manager

# Tokenizers static lib
RUN mkdir -p lib
ARG RELEASE_VERSION=v1.22.1
RUN curl -L https://github.com/daulet/tokenizers/releases/download/${RELEASE_VERSION}/libtokenizers.${TARGETOS}-${TARGETARCH}.tar.gz | tar -xz -C lib
RUN ranlib lib/*.a

ENV CGO_ENABLED=1
ENV GOOS=${TARGETOS:-linux}
ENV GOARCH=${TARGETARCH}
ENV PYTHON=python3.12
ENV PYTHONPATH=/usr/lib64/python3.12/site-packages:/usr/lib/python3.12/site-packages

ARG COMMIT_SHA=unknown
ARG BUILD_REF
RUN export CGO_CFLAGS="$(python3.12-config --cflags) -I/workspace/lib" && \
    export CGO_LDFLAGS="$(python3.12-config --ldflags --embed) -L/workspace/lib -ltokenizers -ldl -lm" && \
    go build -a -o bin/epp -ldflags="-extldflags '-L$(pwd)/lib' -X sigs.k8s.io/gateway-api-inference-extension/version.CommitSHA=${COMMIT_SHA} -X sigs.k8s.io/gateway-api-inference-extension/version.BuildRef=${BUILD_REF}" cmd/epp/main.go

# Runtime stage
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/bin/epp /app/epp

USER root
# microdnf only; add EPEL and install Python runtime
RUN curl -L -o /tmp/epel-release.rpm https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm && \
    rpm -i /tmp/epel-release.rpm && \
    rm /tmp/epel-release.rpm && \
    microdnf install -y --setopt=install_weak_deps=0 zeromq python3.12 python3.12-libs python3.12-pip && \
    microdnf clean all && \
    rm -rf /var/cache/yum /var/lib/yum && \
    ln -sf /usr/bin/python3.12 /usr/bin/python3 && \
    ln -sf /usr/bin/python3.12 /usr/bin/python

# Install wrapper as a module in site-packages
RUN mkdir -p /usr/local/lib/python3.12/site-packages/
COPY --from=builder /workspace/llm-d-kv-cache-manager/pkg/preprocessing/chat_completions/render_jinja_template_wrapper.py /usr/local/lib/python3.12/site-packages/

# Python deps (no cache, single target) – filter out torch
ENV PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
COPY --from=builder /workspace/requirements.txt /tmp/requirements.txt
RUN sed '/^torch\b/d' /tmp/requirements.txt > /tmp/requirements.notorch.txt && \
    python3.12 -m pip install --no-cache-dir --upgrade pip setuptools wheel && \
    python3.12 -m pip install --no-cache-dir --target /usr/local/lib/python3.12/site-packages -r /tmp/requirements.notorch.txt && \
    python3.12 -m pip install --no-cache-dir --target /usr/local/lib/python3.12/site-packages PyYAML && \
    rm /tmp/requirements.txt /tmp/requirements.notorch.txt && \
    rm -rf /root/.cache/pip

# Python env
ENV PYTHONPATH="/usr/local/lib/python3.12/site-packages:/usr/lib/python3.12/site-packages"
ENV PYTHON=python3.12
ENV PATH=/usr/bin:/usr/local/bin:$PATH
ENV HF_HOME="/tmp/.cache"

USER 65532:65532

EXPOSE 9002
EXPOSE 9003
EXPOSE 9090
EXPOSE 5557

ENTRYPOINT ["/app/epp"]

