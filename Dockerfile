# syntax=docker/dockerfile:1

############################
# Build stage
############################
# Pinned to the build platform so Go cross-compiles instead of running the whole
# toolchain under QEMU emulation, which is roughly 5-10x faster for arm targets.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src

# Dependency layer: cached unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/rtsp-sftp-uploader ./cmd/rtsp-sftp-uploader

############################
# Runtime stage
############################
# Alpine rather than distroless: ffmpeg is required and is packaged here for
# amd64, arm64 and arm/v7 alike.
FROM alpine:3.24

LABEL org.opencontainers.image.title="rtsp-sftp-uploader" \
      org.opencontainers.image.description="Captures a frame from an RTSP camera and uploads it to an SFTP server on an interval" \
      org.opencontainers.image.source="https://github.com/danieldenktmit/rtsp-sftp-uploader" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && addgroup -g 65532 -S nonroot \
 && adduser -u 65532 -S -G nonroot -H -s /sbin/nologin nonroot

COPY --from=build /out/rtsp-sftp-uploader /usr/local/bin/rtsp-sftp-uploader

# CAPTURE_OUTPUT_DIR must be writable; /tmp is a tmpfs or emptyDir in Kubernetes,
# which is what makes a read-only root filesystem possible.
ENV CAPTURE_OUTPUT_DIR=/tmp \
    CAPTURE_INTERVAL=60s \
    HTTP_ADDR=:8080 \
    LOG_FORMAT=json

USER 65532:65532
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/rtsp-sftp-uploader"]
