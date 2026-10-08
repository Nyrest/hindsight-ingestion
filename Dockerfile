# syntax=docker/dockerfile:1

# Stage 1: build the React frontend with Bun.
FROM --platform=$BUILDPLATFORM oven/bun:1 AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/bun.lock ./
RUN bun install --frozen-lockfile
COPY frontend/ ./
# vite writes to ../backend/internal/web/dist
RUN mkdir -p ../backend/internal/web && bun run build

# Stage 2: build the Go backend with the frontend embedded.
FROM golang:1.26-alpine AS backend
RUN apk add --no-cache gcc musl-dev
WORKDIR /src/backend
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend /src/backend/internal/web/dist ./internal/web/dist
# CGO is required by the SQLite driver (gorm.io/driver/sqlite).
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/hindsight-ingestion ./cmd/server

# Stage 3: minimal runtime.
FROM alpine:3.23
# BusyBox supplies wget for the health check.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -S -D -u 10001 -h /data app \
    && mkdir -p /data && chown app /data
COPY --from=backend /out/hindsight-ingestion /usr/local/bin/hindsight-ingestion
COPY LICENSE /usr/share/doc/hindsight-ingestion/LICENSE
LABEL org.opencontainers.image.licenses="MIT"
USER app
ENV LISTEN_ADDR=:8080 \
    DB_TYPE=sqlite \
    DB_DSN=/data/hindsight-ingestion.db
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/hindsight-ingestion"]
