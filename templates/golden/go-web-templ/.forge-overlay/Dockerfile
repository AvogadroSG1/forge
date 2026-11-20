# syntax=docker/dockerfile:1.29.0@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183

# Two images from one pinned toolchain:
#   --target runtime  the application, a static binary on a shell-less base
#   --target sandbox  the Go toolchain plus shell tools, for agents and CI
# See CONTAINER.md for usage and for how to refresh the pins below.
#
# Every image is pinned as tag@digest: the tag is for people, the digest is
# what gets pulled. Digests are multi-platform index digests, so the same pin
# serves linux/amd64 and linux/arm64. The Go and golangci-lint versions MUST
# match mise.toml.
ARG GO_IMAGE=golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280
ARG LINT_IMAGE=golangci/golangci-lint:v2.15.0-alpine@sha256:2ba875b990f1d8501ad4ec19be1ea0701e316ec3a4b764f7b129f62f01318bbf
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
# The templ generator. MUST match the templ tool in mise.toml and the module in
# go.mod. Declared here so the two stages that install it share one pin.
ARG TEMPL_VERSION=v0.3.1118

# --- lint: source of the pinned golangci-lint binary -------------------------
FROM ${LINT_IMAGE} AS lint

# --- tools: pinned Go tooling, verified by the Go checksum database ----------
FROM ${GO_IMAGE} AS tools
ARG GOPLS_VERSION=v0.24.0
ARG TEMPL_VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOBIN=/out go install "golang.org/x/tools/gopls@${GOPLS_VERSION}" \
    && CGO_ENABLED=0 GOBIN=/out go install "github.com/a-h/templ/cmd/templ@${TEMPL_VERSION}"

# --- sandbox: toolchain image for agents, built for the target platform ------
FROM ${GO_IMAGE} AS sandbox
# apk packages are not version-pinned: Alpine removes superseded versions from
# its repositories, so exact pins break rebuilds. The digest-pinned base fixes
# the Alpine release, which bounds the drift.
# hadolint ignore=DL3018
RUN apk add --no-cache bash coreutils curl git jq \
    && addgroup -g 10001 agent \
    && adduser -D -u 10001 -G agent -h /home/agent -s /bin/bash agent \
    && mkdir -p /workspace \
    && chown agent:agent /workspace
COPY --from=tools /out/gopls /usr/local/bin/gopls
COPY --from=tools /out/templ /usr/local/bin/templ
COPY --from=lint /usr/bin/golangci-lint /usr/local/bin/golangci-lint
ENV CGO_ENABLED=0 \
    GOPATH=/home/agent/go \
    GOCACHE=/home/agent/.cache/go-build \
    PATH=/home/agent/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
USER 10001:10001
WORKDIR /workspace
CMD ["bash"]

# --- templ: the same pinned generator, built for the build machine ----------
FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS templ
ARG TEMPL_VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOBIN=/out go install "github.com/a-h/templ/cmd/templ@${TEMPL_VERSION}"

# --- build: runs on the build machine and cross-compiles for the target ------
FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify
COPY . .
# Regenerate *_templ.go from the .templ sources, so the image never ships the
# checked-in generated code when a template has changed since it was written.
COPY --from=templ /out/templ /usr/local/bin/templ
RUN templ generate
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/web

# --- runtime: the default target; nothing but the binary ---------------------
FROM ${RUNTIME_IMAGE} AS runtime
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/app /app
# Configuration comes from the environment. PORT is the only setting with a
# default; override it at run time, never by rebuilding.
ENV PORT=8080
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app"]
