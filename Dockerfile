FROM --platform=$BUILDPLATFORM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/codex-agent-identity-sidecar ./cmd/sidecar

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
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
