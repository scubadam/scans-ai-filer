# Multi-stage, multi-arch Dockerfile for scans-ai-filer
# Compiles a single zero-dependency static Go binary on distroless (~10MB total).

FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -extldflags '-static'" -o /scans-ai-filer ./cmd/filer

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Scans AI Filer" \
      org.opencontainers.image.description="Privacy-first AI document archivist for scanned documents" \
      org.opencontainers.image.source="https://github.com/scubadam/scans-ai-filer" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later"

WORKDIR /

COPY --from=builder /scans-ai-filer /scans-ai-filer

USER nonroot:nonroot

ENTRYPOINT ["/scans-ai-filer"]
