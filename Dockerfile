ARG PROXY_RUNTIME=0
FROM golang:1.26-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
COPY src/web ./src/web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/zcode2api .

FROM docker.io/metacubex/mihomo:v1.19.33@sha256:9211361a3ed85305383288d38c05e85862d6d5a590c10007c37fc059cca82e9c AS mihomo-runtime

FROM debian:bookworm-slim AS runtime-base
ARG CAPTCHA_RUNTIME=0
ARG GRAMMAR_RUNTIME=0
WORKDIR /app
COPY captcha_node/package.json captcha_node/package-lock.json ./captcha_node/
COPY grammar_python/requirements.txt ./grammar_python/
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && case "$CAPTCHA_RUNTIME" in \
         0) ;; \
         1) apt-get install -y --no-install-recommends nodejs npm \
           && npm --prefix /app/captcha_node ci --omit=dev --ignore-scripts --no-audit --no-fund \
           && npm cache clean --force ;; \
         *) echo 'CAPTCHA_RUNTIME must be 0 or 1' >&2; exit 1 ;; \
       esac \
    && case "$GRAMMAR_RUNTIME" in \
         0) ;; \
         1) apt-get install -y --no-install-recommends python3 python3-venv \
           && python3 -m venv /app/grammar_python/.venv \
           && /app/grammar_python/.venv/bin/python -m pip install --no-cache-dir --only-binary=:all: -r /app/grammar_python/requirements.txt ;; \
         *) echo 'GRAMMAR_RUNTIME must be 0 or 1' >&2; exit 1 ;; \
       esac \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/zcode2api /usr/local/bin/zcode2api
ENV ZCODE2API_DATA_DIR=/app/data \
    ZCODE2API_HOST=0.0.0.0 \
    ZCODE2API_WEB_PORT=6789 \
    ZCODE2API_GATEWAY_PORT=6780
VOLUME /app/data
EXPOSE 6789 6780
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s \
  CMD ["zcode2api", "--healthcheck", "auto"]
CMD ["zcode2api", "--root", "/app"]

FROM runtime-base AS runtime-0

FROM runtime-base AS runtime-1
COPY --from=mihomo-runtime /mihomo /usr/local/bin/mihomo

FROM runtime-${PROXY_RUNTIME} AS runtime
