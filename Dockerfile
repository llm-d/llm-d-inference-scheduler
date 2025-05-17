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

## llm-d internal repos pull config
ARG GIT_NM_USER
ARG NM_TOKEN
### use git token
RUN echo -e "machine github.com\n\tlogin ${GIT_NM_USER}\n\tpassword ${NM_TOKEN}" >> ~/.netrc
ENV GOPRIVATE=github.com/llm-d
ENV GIT_TERMINAL_PROMPT=1

# Copy the Go Modules manifests
COPY go.mod go.sum ./

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY internal/ internal/

# Copy the vendor directory
COPY vendor/ vendor/

# Build
ENV GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=1
ENV GOOS=${TARGETOS:-linux}
ENV GOARCH=${TARGETARCH}

# Check the contents of the /workspace/lib directory to ensure the library is there
RUN echo "Listing contents of /workspace/lib:" && ls -l /workspace/lib

# Ensure the libtokenizers is found by the linker
RUN go build -tags cgo -a -o bin/epp -mod=vendor -ldflags="-extldflags '-L/workspace/lib -L/usr/local/lib'" cmd/epp/main.go cmd/epp/health.go
# RUN go build -a -o bin/epp -ldflags="-extldflags '-L$(pwd)/lib'" cmd/epp/main.go cmd/epp/health.go

RUN rm -rf ~/.netrc # remove git token

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM registry.access.redhat.com/ubi9/ubi:latest
WORKDIR /
COPY --from=builder /workspace/bin/epp /app/epp
USER 65532:65532

# expose gRPC, health, and metrics ports
EXPOSE 9002
EXPOSE 9003
EXPOSE 9090

ENTRYPOINT ["/app/epp"]

