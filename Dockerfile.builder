FROM quay.io/projectquay/golang:1.25

RUN mkdir /app
WORKDIR /app

ARG TYPOS_VERSION=v1.34.0
ARG KIND_VERSION=v0.27.0
ARG GOLANGCI_LINT_VERSION=v2.8.0
ARG KUBECTL_VERSION=v1.35.3
ARG KUSTOMIZE_VERSION=v5.6.0
ARG DOCKER_VERSION=29.3.0
ARG DOCKER_BUILDX_VERSION=v0.32.1

RUN dnf install -y podman && dnf clean all

# Install docker CLI and buildx plugin
RUN ARCH=$(uname -m) && \
    GOARCH=$(echo ${ARCH} | sed 's/x86_64/amd64/; s/aarch64/arm64/') && \
    curl -sSfL https://download.docker.com/linux/static/stable/${ARCH}/docker-${DOCKER_VERSION}.tgz | \
    tar -xz --strip-components=1 -C /usr/local/bin docker/docker && \
    mkdir -p /usr/local/lib/docker/cli-plugins && \
    curl -sSfL -o /usr/local/lib/docker/cli-plugins/docker-buildx \
      "https://github.com/docker/buildx/releases/download/${DOCKER_BUILDX_VERSION}/buildx-${DOCKER_BUILDX_VERSION}.linux-${GOARCH}" && \
    chmod +x /usr/local/lib/docker/cli-plugins/docker-buildx

# Install golangci-lint
RUN GOARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/') && \
    curl -sSfL https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION#v}-linux-${GOARCH}.tar.gz | \
    tar -xz --strip-components=1 -C /usr/local/bin golangci-lint-${GOLANGCI_LINT_VERSION#v}-linux-${GOARCH}/golangci-lint

# Install kubectl
RUN GOARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/') && \
    curl -sSfL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${GOARCH}/kubectl" && \
    chmod +x /usr/local/bin/kubectl

# Install kustomize
RUN GOARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/') && \
    curl -sSfL https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize/${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_${GOARCH}.tar.gz | \
    tar -xz -C /usr/local/bin

# Install kind
RUN GOARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/') && \
    curl -sSfL -o /usr/local/bin/kind https://github.com/kubernetes-sigs/kind/releases/download/${KIND_VERSION}/kind-linux-${GOARCH} && \
    chmod +x /usr/local/bin/kind

# Install typos
RUN curl -sSfL https://github.com/crate-ci/typos/releases/download/${TYPOS_VERSION}/typos-${TYPOS_VERSION}-$(uname -m)-unknown-linux-musl.tar.gz | tar -xz -C /usr/local/bin

# Go caches are mounted as volumes at runtime for persistence across image rebuilds.
# Directories are created with open permissions so non-root users (docker -u) can write.
ENV GOMODCACHE=/go/pkg/mod
ENV GOCACHE=/go/cache
RUN mkdir -p /go/pkg/mod /go/cache && chmod -R 777 /go

RUN { \
      echo '#!/bin/sh'; \
      echo 'export HOME=/tmp'; \
      echo 'exec "$@"'; \
    } > /entrypoint.sh && \
    chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
