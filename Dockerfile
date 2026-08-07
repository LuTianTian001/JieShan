# syntax=docker/dockerfile:1.7

FROM node:22-alpine AS web-build
WORKDIR /src/web

ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable && corepack prepare pnpm@10.15.1 --activate

COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY web/ ./
RUN pnpm run build

FROM golang:1.25-alpine AS api-build
WORKDIR /src

COPY go.* ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/jieshan ./cmd/jieshan

FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 jieshan \
    && adduser -S -D -H -u 10001 -G jieshan jieshan \
    && mkdir -p /app/web /data \
    && chown -R jieshan:jieshan /app /data

WORKDIR /app
COPY --from=api-build --chown=jieshan:jieshan /out/jieshan /app/jieshan
COPY --from=web-build --chown=jieshan:jieshan /src/web/dist /app/web

ENV JIESHAN_LISTEN_ADDR=:4000 \
    JIESHAN_DATA_DIR=/data \
    JIESHAN_DB_PATH=/data/jieshan.sqlite \
    JIESHAN_WEB_DIR=/app/web \
    JIESHAN_LOG_LEVEL=info \
    GOMEMLIMIT=560MiB \
    GOMAXPROCS=2 \
    TZ=Asia/Shanghai

USER jieshan
VOLUME ["/data"]
EXPOSE 4000

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:4000/healthz >/dev/null || exit 1

ENTRYPOINT ["/app/jieshan"]
