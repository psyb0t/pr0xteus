# pr0xteus orchestrator — reproducible, vendored multi-stage Go build.

FROM golang:1.26.9-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS builder

WORKDIR /app

ARG APP_NAME=pr0xteus
ARG BUILD_COMMIT=
ARG BUILD_VERSION=dev

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

RUN CGO_ENABLED=0 go build -a \
    -trimpath \
    -ldflags="-s -w -extldflags '-static' -X main.appName=${APP_NAME} -X main.buildCommit=${BUILD_COMMIT} -X main.buildVersion=${BUILD_VERSION}" \
    -mod=vendor \
    -o ./build/pr0xteus ./cmd

# Runtime stays deliberately tiny. BusyBox provides the local wget applet used
# by Compose's healthcheck; it contains no compiler, source, or package cache.
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

RUN apk --no-cache add ca-certificates=20260909-r0
RUN adduser -D -u 1500 -s /bin/sh pr0xteus && \
    install -d -o pr0xteus -g pr0xteus /app

WORKDIR /app

ENV LOG_LEVEL=info \
    LOG_FORMAT=json \
    LOG_ADD_SOURCE=true

COPY --from=builder /app/build/pr0xteus .
RUN chown pr0xteus:pr0xteus /app/pr0xteus

USER pr0xteus

EXPOSE 8000 9091

ENTRYPOINT ["./pr0xteus"]
CMD ["run"]
