# syntax=docker/dockerfile:1

# Build stage — always runs on the host platform for speed;
# cross-compiles to the target platform via GOOS/GOARCH.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /src

# Cache module downloads separately from source code.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 ensures a fully static binary with no libc dependency.
# GOARM is derived from TARGETVARIANT (e.g. "v7" → "7"); default to empty
# so it is ignored for non-ARM platforms.
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOARM=${TARGETVARIANT#v} \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /hue-exporter \
      .

# ---- Final stage: scratch + CA certs ----
# scratch gives the smallest possible image; we add only the TLS CA
# certificate bundle so the binary can verify HTTPS connections when a
# proper bridge certificate is configured.
FROM scratch

# Copy CA certificates from the builder so HTTPS works at runtime.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /hue-exporter /hue-exporter

EXPOSE 9366

ENTRYPOINT ["/hue-exporter"]
