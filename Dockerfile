# syntax=docker/dockerfile:1
# PROBE — Platform for Remote Operations & Bridge Environment
# Multi-stage build: React frontend → Go binary (with embedded frontend) → minimal runtime.
#
# Deploy:
#   docker compose up -d --build
# Or standalone:
#   docker build -t falke-probe . && docker run -d -p 7701:7701 falke-probe

# ── Stage 1: Frontend ──────────────────────────────────────────
FROM node:22-slim AS frontend
WORKDIR /build
COPY web/package.json web/package-lock.json* ./
RUN npm ci 2>/dev/null || npm install
COPY web/ ./
RUN npm run build

# ── Stage 2: Go binary ─────────────────────────────────────────
FROM golang:1.23-bookworm AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /build/dist ./web/dist
ENV CGO_ENABLED=0
RUN go build -trimpath -o /probe ./cmd/probe/

# ── Stage 3: Runtime ───────────────────────────────────────────
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /probe /app/probe
# Builder toolchain: go + source + module cache so the agent builder can
# cross-compile replica binaries (windows/linux/darwin/android) at runtime.
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /build /src
COPY --from=builder /go/pkg/mod /go/pkg/mod
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