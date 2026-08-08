# Stage 1: Build stage
FROM golang:1.26-alpine AS builder

# Install build dependencies (needed if CGO or native extensions are used)
RUN apk add --no-cache git gcc musl-dev libc-dev

WORKDIR /app

# Copy dependency definitions first to leverage Docker layer caching
COPY go.mod go.sum* ./
RUN go mod download || true

# Copy source code
COPY . .

# Build the binary
# Disable CGO for a pure static binary, or set CGO_ENABLED=1 if C libraries are required
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o go-bench-suite

# Stage 2: Runtime stage
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -g bench bench
USER bench

WORKDIR /opt/bench
COPY --from=builder /app/go-bench-suite /opt/bench/go-bench-suite
USER bench

ENTRYPOINT ["./go-bench-suite"]
