# syntax=docker/dockerfile:1

# ---- Builder stage ----
# Compiles static binaries for both the api and worker commands.
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache dependencies first for faster incremental builds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO disabled for a fully static binary that runs in a scratch/distroless image.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# ---- Runtime stage ----
# Minimal image: no compiler, no source, no build tools.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /out/api /app/api
COPY --from=builder /out/worker /app/worker

# Run as the non-root user provided by the distroless image.
USER nonroot:nonroot

EXPOSE 8080

# Default to the API; docker-compose overrides the command for the worker.
ENTRYPOINT ["/app/api"]
