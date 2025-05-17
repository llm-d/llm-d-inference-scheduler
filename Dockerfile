# Build Stage: using Go 1.24.1 image
FROM quay.io/projectquay/golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

# Install build tools
RUN dnf install -y gcc gcc-c++ glibc-devel libstdc++-devel kernel-headers && \
    dnf remove -y clang && \
    dnf clean all
ENV CC=gcc
ENV CXX=g++

WORKDIR /workspace

# === GitHub credentials for private modules
ARG GIT_NM_USER
ARG NM_TOKEN
RUN echo -e "machine github.com\n\tlogin ${GIT_NM_USER}\n\tpassword ${NM_TOKEN}" >> ~/.netrc
ENV GOPRIVATE=github.com/llm-d
ENV GIT_TERMINAL_PROMPT=1

# === Copy go.mod and go.sum early to leverage cache
COPY go.mod go.sum ./

# === Module download cache setup
ENV GOMODCACHE=/go/pkg/mod
ENV GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=1
ENV GOOS=${TARGETOS:-linux}
ENV GOARCH=${TARGETARCH}

# === Cache the module download
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# === Copy the full source code
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY internal/ internal/

# === HuggingFace tokenizer bindings
RUN mkdir -p lib && \
    curl -L https://github.com/daulet/tokenizers/releases/download/v1.20.2/libtokenizers.${TARGETOS}-${TARGETARCH}.tar.gz | tar -xz -C lib && \
    ranlib lib/*.a

# === Build
RUN go build -tags cgo -a -o bin/epp -ldflags="-extldflags '-L$(pwd)/lib'" cmd/epp/main.go cmd/epp/health.go

# === Cleanup sensitive info
RUN rm -rf ~/.netrc

# Final stage: UBI runtime
FROM registry.access.redhat.com/ubi9/ubi:latest
WORKDIR /
COPY --from=builder /workspace/bin/epp /app/epp
USER 65532:65532

EXPOSE 9002
EXPOSE 9003
EXPOSE 9090

ENTRYPOINT ["/app/epp"]
