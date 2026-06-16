# Build context is the HyNode directory:
# docker build -t hynode:latest .
FROM golang:1.24.7-bookworm AS build
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY="${GOPROXY}" GOSUMDB=off go mod download
COPY . .
RUN GOPROXY="${GOPROXY}" GOSUMDB=off go build -trimpath -tags "with_quic with_acme" -ldflags="-s -w" -o /out/hynode ./cmd/hynode

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/* \
    && useradd --system --home-dir /var/lib/hynode --create-home hynode
COPY --from=build /out/hynode /usr/local/bin/hynode
COPY config.example.yaml /etc/hynode/config.yaml
USER hynode
VOLUME ["/var/lib/hynode"]
EXPOSE 9090
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -f http://127.0.0.1:9090/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/hynode", "run", "-c", "/etc/hynode/config.yaml"]
