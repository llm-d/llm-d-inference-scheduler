FROM quay.io/fedora/fedora:latest

RUN mkdir /app
WORKDIR /app


RUN dnf install -y \
    binutils zeromq-devel gcc-c++ \
    python3-devel python3-pip golang golangci-lint && \
    dnf clean all


COPY go.mod .
COPY go.sum .

# Set GOMODCACHE to a shared location accessible by any user
ENV GOMODCACHE=/go/pkg/mod
ENV GOCACHE=/go/cache

# Python deps for kv-cache-manager, if specific versions cannot be installed then the latest ones will be fetched
RUN go mod download && \
    sh -c 'KV_CACHE_PKG=$(go list -m -f "{{.Dir}}" github.com/llm-d/llm-d-kv-cache-manager 2>/dev/null); \
            pip install --quiet -r "$KV_CACHE_PKG/pkg/preprocessing/chat_completions/requirements.txt" || \
            sed "s/[=<>!].*//" "$KV_CACHE_PKG/pkg/preprocessing/chat_completions/requirements.txt" | pip install --quiet -r /dev/stdin;' && \
    chmod -R a+rw /go


COPY Dockerfile.epp .

# Download the HuggingFace tokenizer bindings.
RUN sh -c 'TOKENIZER_VERSION="$(grep "^ARG RELEASE_VERSION=" Dockerfile.epp | cut -d= -f2)"; \
        curl -L https://github.com/daulet/tokenizers/releases/download/${TOKENIZER_VERSION}/libtokenizers.linux-$(uname -m).tar.gz | tar -xz -C /usr/local/lib' && \
    ranlib /usr/local/lib/*.a && \
    rm Dockerfile.epp

RUN { \
      echo '#!/bin/sh'; \
      echo 'export HOME=/tmp'; \
      echo 'export CGO_CFLAGS="$(python3-config --cflags)"'; \
      echo 'export CGO_LDFLAGS="$(python3-config --ldflags --embed) -ltokenizers -ldl -lm"'; \
      echo 'export KV_CACHE_PKG="$(go list -m -f "{{.Dir}}/pkg/preprocessing/chat_completions" github.com/llm-d/llm-d-kv-cache-manager 2>/dev/null || echo "")"'; \
      echo 'export PYTHONPATH="${PYTHONPATH}:${KV_CACHE_PKG}"'; \
      echo 'exec "$@"'; \
    } > /entrypoint.sh && \
    chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
