FROM golang:1.27.1-alpine3.23 AS builder

ARG BUILD_VERSION=dev
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 go build \
    -buildvcs=false \
    -trimpath \
    -ldflags="-s -w -X main.version=${BUILD_VERSION}" \
    -o /out/ha-influxdb-mcp \
    ./cmd/ha-influxdb-mcp

FROM alpine:3.22

ARG BUILD_VERSION=dev
ARG BUILD_ARCH=unknown

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/ha-influxdb-mcp /usr/local/bin/ha-influxdb-mcp

LABEL \
    io.hass.name="Home Assistant InfluxDB MCP" \
    io.hass.description="Read-only ChatGPT access to Home Assistant history in InfluxDB" \
    io.hass.version="${BUILD_VERSION}" \
    io.hass.type="app" \
    io.hass.arch="${BUILD_ARCH}"

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/ha-influxdb-mcp"]
