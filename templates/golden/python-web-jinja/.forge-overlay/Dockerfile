# syntax=docker/dockerfile:1.29.0@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183

# Two images from one pinned toolchain:
#   --target runtime  the application: the locked virtualenv on slim Python
#   --target sandbox  Python, uv, ruff, pyright and shell tools, for agents and CI
# See CONTAINER.md for usage and for how to refresh the pins below.
#
# Every image is pinned as tag@digest: the tag is for people, the digest is
# what gets pulled. Digests are multi-platform index digests, so the same pin
# serves linux/amd64 and linux/arm64. The Python, uv, ruff, Node and pyright
# versions MUST match mise.toml. Nothing here is cross-compiled: wheels are
# platform-specific, so every stage runs on the target platform.
ARG PYTHON_IMAGE=python:3.15.2-slim-trixie@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
ARG UV_IMAGE=ghcr.io/astral-sh/uv:0.13.9@sha256:d3b601f0c7b118bc1753febf7c41cc80981df678445e0254284eecf84db79a7d
ARG RUFF_IMAGE=ghcr.io/astral-sh/ruff:0.17.5@sha256:3abcaeec575bf06baac8e28f2462e75d7093a7740ecf61c5f833b0a827abac10
ARG NODE_IMAGE=node:26.13.1-trixie-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc

# --- uv: source of the pinned uv binary --------------------------------------
FROM ${UV_IMAGE} AS uv

# --- ruff: source of the pinned ruff binary ----------------------------------
FROM ${RUFF_IMAGE} AS ruff

# --- tools: pinned pyright, installed from npm into a copyable prefix --------
FROM ${NODE_IMAGE} AS tools
ARG PYRIGHT_VERSION=1.1.414
RUN --mount=type=cache,target=/root/.npm \
    npm install --global --prefix /out "pyright@${PYRIGHT_VERSION}"

# --- sandbox: toolchain image for agents, built for the target platform ------
FROM ${PYTHON_IMAGE} AS sandbox
# apt packages are not version-pinned: Debian removes superseded versions from
# its archive, so exact pins break rebuilds. The digest-pinned base fixes the
# Debian release, which bounds the drift. libatomic1 is the shared library the
# copied Node binary links against.
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash curl git jq libatomic1 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 agent \
    && useradd --uid 10001 --gid agent --home-dir /home/agent --create-home --shell /bin/bash agent \
    && mkdir -p /workspace \
    && chown agent:agent /workspace
COPY --from=uv /uv /uvx /usr/local/bin/
COPY --from=ruff /ruff /usr/local/bin/ruff
COPY --from=tools /usr/local/bin/node /usr/local/bin/node
COPY --from=tools /out/ /usr/local/pyright/
ENV PATH=/usr/local/pyright/bin:$PATH \
    UV_PYTHON_DOWNLOADS=never \
    UV_CACHE_DIR=/home/agent/.cache/uv \
    UV_LINK_MODE=copy
USER 10001:10001
WORKDIR /workspace
CMD ["bash"]

# --- build: installs the locked dependencies and the project -----------------
FROM ${PYTHON_IMAGE} AS build
COPY --from=uv /uv /uvx /usr/local/bin/
ENV UV_PROJECT_ENVIRONMENT=/app/.venv \
    UV_COMPILE_BYTECODE=1 \
    UV_PYTHON_DOWNLOADS=never \
    UV_LINK_MODE=copy
WORKDIR /src
COPY pyproject.toml uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --locked --no-dev --no-install-project
COPY . .
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --locked --no-dev --no-editable

# --- runtime: the default target; the virtualenv and nothing else ------------
FROM ${PYTHON_IMAGE} AS runtime
# The pinned base image lags Debian security fixes, so they are applied here;
# the digest still fixes the Python build and the Debian release. pip and its
# vendored libraries are removed because the runtime never installs anything.
# hadolint ignore=DL3005,DL3008,DL3009
RUN apt-get update \
    && apt-get upgrade -y --no-install-recommends \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip uninstall --yes pip setuptools wheel
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /app/.venv /app/.venv
# Configuration comes from the environment. HOST and PORT are the only
# settings with defaults; override them at run time, never by rebuilding.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HOST=0.0.0.0 \
    PORT=8080
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/app/.venv/bin/app"]
