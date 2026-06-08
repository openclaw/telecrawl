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

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git openssh-client tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --home-dir /data --uid 10001 telecrawl
ENV HOME=/data
VOLUME ["/data", "/tdata"]
WORKDIR /data
COPY --from=build /out/telecrawl /usr/local/bin/telecrawl
USER telecrawl
ENTRYPOINT ["telecrawl"]
CMD ["--help"]
