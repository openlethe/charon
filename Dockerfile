FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags '-s -w' -o charon ./cmd/charon

FROM alpine:latest

RUN apk add --no-cache ca-certificates sqlite

WORKDIR /app
COPY --from=builder /app/charon /usr/local/bin/charon
COPY init.sh /app/init.sh
RUN chmod +x /app/init.sh

RUN mkdir -p /data && chmod 750 /data

ENV CHARON_DATA_DIR=/data
ENV CHARON_HTTP=":18484"
ENV CHARON_ADMIN_HTTP="/tmp/charon-admin.sock"

EXPOSE 18484

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:18484/health || exit 1

ENTRYPOINT ["/app/init.sh"]
