FROM golang:1.26-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/codex-agent-identity-sidecar ./cmd/sidecar

FROM debian:bookworm-slim
LABEL org.opencontainers.image.source="https://github.com/simplez2/cpa-codex-agent-identity" \
      org.opencontainers.image.title="Codex Agent Identity sidecar" \
      org.opencontainers.image.description="Encrypted Codex Agent Identity and PAT sidecar for CLIProxyAPI" \
      org.opencontainers.image.licenses="MIT"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 sidecar \
    && useradd --uid 65532 --gid 65532 --no-create-home --shell /usr/sbin/nologin sidecar \
    && mkdir /data \
    && chown 65532:65532 /data
COPY --from=builder --chown=65532:65532 /out/codex-agent-identity-sidecar /codex-agent-identity-sidecar
VOLUME ["/data"]
EXPOSE 8787
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/codex-agent-identity-sidecar", "healthcheck"]
ENTRYPOINT ["/codex-agent-identity-sidecar"]
