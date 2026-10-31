# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build

ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X github.com/codewith-aditya/astra-moderation/internal/api.Version=${VERSION}" \
    -o /out/astra-moderation ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ffmpeg ca-certificates tzdata \
    && adduser -D -H -u 10001 app

WORKDIR /app
COPY --from=build /out/astra-moderation /usr/local/bin/astra-moderation
COPY policy.json /app/policy.json

RUN mkdir -p /data && chown -R app:app /data /app
USER app

ENV MOD_ADDR=:8096 \
    MOD_DATA_DIR=/data \
    MOD_POLICY=/app/policy.json

EXPOSE 8096

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8096/health >/dev/null || exit 1

ENTRYPOINT ["astra-moderation"]
