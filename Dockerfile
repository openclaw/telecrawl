# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/openclaw/telecrawl/internal/cli.version=${VERSION}" \
    -o /out/telecrawl ./cmd/telecrawl

FROM python:3.12-slim AS python-deps
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential libsqlcipher-dev python3-dev \
    && rm -rf /var/lib/apt/lists/* \
    && python -m venv /opt/telecrawl-venv \
    && /opt/telecrawl-venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/telecrawl-venv/bin/pip install --no-cache-dir opentele2 'telethon>=1.43.2' pycryptodomex sqlcipher3

FROM python:3.12-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git libsqlcipher1 openssh-client tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --home-dir /data --uid 10001 telecrawl
ENV HOME=/data \
    PATH=/opt/telecrawl-venv/bin:${PATH} \
    TELECRAWL_PYTHON=/opt/telecrawl-venv/bin/python
VOLUME ["/data", "/tdata"]
WORKDIR /data
COPY --from=python-deps /opt/telecrawl-venv /opt/telecrawl-venv
COPY --from=build /out/telecrawl /usr/local/bin/telecrawl
USER telecrawl
ENTRYPOINT ["telecrawl"]
CMD ["--help"]
