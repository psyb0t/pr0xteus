# pr0xteus orchestrator — reproducible, vendored multi-stage Go build.

FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS builder

WORKDIR /app

ARG APP_NAME=pr0xteus
ARG BUILD_COMMIT=

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

RUN CGO_ENABLED=0 go build -a \
    -trimpath \
    -ldflags="-s -w -extldflags '-static' -X main.appName=${APP_NAME} -X main.buildCommit=${BUILD_COMMIT}" \
    -mod=vendor \
    -o ./build/pr0xteus ./cmd

# Runtime stays deliberately tiny. BusyBox provides the local wget applet used
# by Compose's healthcheck; it contains no compiler, source, or package cache.
FROM alpine:3.20.6@sha256:de4fe7064d8f98419ea6b49190df1abbf43450c1702eeb864fe9ced453c1cc5f

ARG PR0XTEUS_BUILT_CELL_IMAGE=psyb0t/pr0xteus:cell-latest

RUN apk --no-cache add ca-certificates=20260413-r0
RUN adduser -D -u 1500 -s /bin/sh pr0xteus && \
    install -d -o pr0xteus -g pr0xteus /app

WORKDIR /app

ENV LOG_LEVEL=info \
    LOG_FORMAT=json \
    LOG_ADD_SOURCE=true \
    PR0XTEUS_BUILT_CELL_IMAGE=${PR0XTEUS_BUILT_CELL_IMAGE}

COPY --from=builder /app/build/pr0xteus .
RUN chown pr0xteus:pr0xteus /app/pr0xteus

USER pr0xteus

EXPOSE 8000 9091

ENTRYPOINT ["./pr0xteus"]
CMD ["run"]
