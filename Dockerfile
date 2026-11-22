# syntax=docker/dockerfile:1
# PROBE — Platform for Remote Operations & Bridge Environment
# Multi-stage build: React frontend → Go binary (with embedded frontend) → minimal runtime.
#
# This image is built by CI, not on the host: .github/workflows/docker.yml
# pushes ghcr.io/falke-ai-circuit/probe:<sha|version> for linux/amd64.
# Deploy pulls that image:
#   docker compose pull && docker compose up -d
# The host compose must reference the image tag, not `build:`.
#
# Everything baked into /src comes from this build context (i.e. the CI
# checkout of one commit). No pre-built tree or committed binary is copied in:
# see .dockerignore.

# ── Stage 1: Frontend ──────────────────────────────────────────
FROM node:22-slim AS frontend
WORKDIR /build
COPY web/package.json web/package-lock.json* ./
RUN npm ci 2>/dev/null || npm install
COPY web/ ./
RUN npm run build

# ── Stage 2: Go binary ─────────────────────────────────────────
FROM golang:1.23-bookworm AS builder
# Provenance of the checkout this image was built from (set by CI).
ARG GIT_SHA=unknown
ARG VERSION=dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /build/dist ./web/dist
ENV CGO_ENABLED=0
RUN go build -trimpath -o /probe ./cmd/probe/

# ── Stage 3: Runtime ───────────────────────────────────────────
FROM debian:bookworm-slim
ARG GIT_SHA=unknown
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/falke-ai-circuit/probe" \
      org.opencontainers.image.revision="$GIT_SHA" \
      org.opencontainers.image.version="$VERSION"
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /probe /app/probe
# Builder toolchain: go + source + module cache so the agent builder can
# cross-compile replica binaries (windows/linux/darwin/android) at runtime.
COPY --from=builder /usr/local/go /usr/local/go
# /src = the single CI checkout this image was built from (source only; the
# runtime builder compiles agents from it). Provenance is stamped beside it.
COPY --from=builder /build /src
COPY --from=builder /go/pkg/mod /go/pkg/mod
RUN printf 'commit=%s\nversion=%s\nbuilt=image\n' "$GIT_SHA" "$VERSION" > /src/BUILD_INFO
ENV GOROOT=/usr/local/go GOPATH=/go GOMODCACHE=/go/pkg/mod PATH="/usr/local/go/bin:$PATH"
RUN mkdir -p /data/runtime /data/logs /data/builds /data/ca
VOLUME /data
EXPOSE 7701
ENTRYPOINT ["/app/probe"]
CMD ["serve", \
     "--addr", "0.0.0.0:7701", \
     "--token", "falke-admin-2026", \
     "--admin-password", "f4lk3.PROBE", \
     "--allowed-cidr", "100.64.0.0/10,10.10.10.0/24,172.16.0.0/12", \
     "--require-api-auth", \
     "--token-ttl", "0", \
     "--registry", "/data/runtime/registry.json", \
     "--operator-db", "/data/runtime/operators.json", \
     "--enrollment-db", "/data/runtime/enrollment.json", \
     "--builder-db", "/data/runtime/builds.json", \
     "--builder-output-dir", "/data/runtime/builds", \
     "--builder-go", "/usr/local/go/bin/go", \
     "--builder-source", "/src/cmd/probe-client/", \
     "--profiles-db", "/data/runtime/profiles.json", \
     "--ca-dir", "/data/runtime/ca", \
     "--audit-log", "/data/logs/audit.jsonl"]