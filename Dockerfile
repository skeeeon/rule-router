# Multi-stage Dockerfile for rule-router applications.
# Build with: docker build --build-arg APP_NAME=<app> -t rule-router/<app> .
# Available apps: rule-router, nats-auth-manager, rule-cli
# Note: http-gateway and rule-scheduler are now features of rule-router
# (enable via config: features.gateway/features.scheduler or env: RR_FEATURES_GATEWAY=true)
#
# Multi-platform: docker buildx build --platform linux/amd64,linux/arm64 ...
# The build stage runs on the build machine's own platform and cross-compiles
# (static binary, CGO off), and the runtime stage runs nothing, so no stage
# needs emulation for the other architecture.

# --- Build stage ---
# Latest stable Go, as in release.yml: the binary ships with this toolchain's
# standard library, so it should carry its security fixes.
FROM --platform=$BUILDPLATFORM golang:1-alpine AS builder

ARG APP_NAME
RUN test -n "$APP_NAME" || (echo "APP_NAME build arg is required" && exit 1)
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

# Cache dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source (config/ contains Go source files needed for compilation)
COPY cmd/ cmd/
COPY internal/ internal/
COPY config/ config/

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /app ./cmd/${APP_NAME}

# --- Runtime stage ---
# Alpine rather than scratch: docker-compose health checks use its wget, and a
# shell helps when debugging on a remote edge box. It already has CA
# certificates and a nobody user (65534), so there is nothing to RUN here.
FROM alpine:3

COPY --from=builder /app /app

USER nobody

STOPSIGNAL SIGTERM

ENTRYPOINT ["/app"]
