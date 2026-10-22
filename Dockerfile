# syntax=docker/dockerfile:1
#
# Multi-arch build for the Go API + built React UI, served as a single
# binary. Build with `make docker-build` / push with `make docker-push`
# (see the Makefile) rather than invoking `docker build` directly, since
# a real multi-platform result requires buildx with a docker-container
# builder.

# ---- Build the web UI --------------------------------------------------
# Platform-independent output, so pinned to the build machine's platform.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci
COPY web/ ./
RUN npm run build

# ---- Cross-compile the Go server ---------------------------------------
# CGO_ENABLED=0 (modernc.org/sqlite is pure Go), so Go cross-compiles
# directly without emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

# Writable data directory for the SQLite database, created here because the
# shell-less runtime image can't mkdir it.
RUN mkdir -p /data

# ---- Runtime image ------------------------------------------------------
# distroless "static" ships the CA bundle needed for HTTPS calls to
# Discord/YouTube; "nonroot" runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

LABEL org.opencontainers.image.source="https://github.com/danotchuuy/video-queue" \
      org.opencontainers.image.description="Tracks VTuber reaction videos per Discord server: queue, editing pipeline, Patreon/YouTube posting, and viewer suggestions."

COPY --from=go-builder --chown=nonroot:nonroot /app /app
COPY --from=web-builder --chown=nonroot:nonroot /src/web/dist /app/web/dist
COPY --from=go-builder --chown=nonroot:nonroot /data /data

WORKDIR /app
USER nonroot:nonroot

# Mount a volume at /data so the database survives container upgrades.
# Discord/YouTube settings are passed as environment variables (see .env.example).
ENV ADDR=:8080 \
    STATIC_DIR=/app/web/dist \
    DATABASE_PATH=/data/video-queue.db

EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/app/server"]
