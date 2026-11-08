# syntax=docker/dockerfile:1.7
FROM golang:1.25.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG SERVICE
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

FROM alpine:3.22.2
RUN apk add --no-cache ca-certificates && addgroup -g 10001 app && adduser -D -H -u 10001 -G app app
COPY --from=build --chown=10001:10001 /out/service /app/service
USER 10001:10001
EXPOSE 8080 8081 9090
HEALTHCHECK --interval=5s --timeout=3s --start-period=90s --retries=12 CMD wget -q -O /dev/null "http://127.0.0.1:${HEALTH_PORT:-8080}/readyz" || exit 1
ENTRYPOINT ["/app/service"]
