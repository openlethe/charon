FROM golang:1.25.12-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/charon ./cmd/charon

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="Charon" \
      org.opencontainers.image.description="Security-focused MCP policy gateway for Lethe" \
      org.opencontainers.image.source="https://github.com/openlethe/charon" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 charon \
    && adduser -S -D -H -u 10001 -G charon charon \
    && install -d -o charon -g charon -m 0700 /data \
    && install -d -o charon -g charon -m 0755 /app

WORKDIR /app
COPY --from=builder --chown=charon:charon /out/charon /usr/local/bin/charon
COPY --chown=charon:charon init.sh /app/init.sh
RUN chmod 0555 /usr/local/bin/charon /app/init.sh

ENV CHARON_DATA_DIR=/data
ENV CHARON_HTTP=:18484
ENV CHARON_ADMIN_HTTP=/tmp/charon-admin.sock

EXPOSE 18484
USER 10001:10001

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:18484/livez || exit 1

ENTRYPOINT ["/app/init.sh"]
