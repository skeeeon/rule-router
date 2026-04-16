# Multi-stage Dockerfile for rule-router applications.
# Build with: docker build --build-arg APP_NAME=<app> -t rule-router/<app> .
# Available apps: rule-router, nats-auth-manager, rule-cli
# Note: http-gateway and rule-scheduler are now features of rule-router
# (enable via config: features.gateway/features.scheduler or env: RR_FEATURES_GATEWAY=true)

# --- Build stage ---
FROM golang:1.26-alpine AS builder

ARG APP_NAME
RUN test -n "$APP_NAME" || (echo "APP_NAME build arg is required" && exit 1)

WORKDIR /build

# Cache dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source (config/ contains Go source files needed for compilation)
COPY cmd/ cmd/
COPY internal/ internal/
COPY config/ config/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o /app ./cmd/${APP_NAME}

# --- Runtime stage ---
FROM alpine:3.21

RUN adduser -D -u 65534 appuser

COPY --from=builder /app /app

USER appuser

STOPSIGNAL SIGTERM

ENTRYPOINT ["/app"]
