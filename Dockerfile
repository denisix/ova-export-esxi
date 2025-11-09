# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build the application
RUN make build

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/build/ova-esxi-uploader /usr/local/bin/ova-esxi-uploader

# Copy documentation
COPY --from=builder /app/README.md /app/

# Change ownership
RUN chown -R appuser:appgroup /app

# Switch to non-root user
USER appuser

# Create volume for upload sessions and temp files
VOLUME ["/app/data"]
WORKDIR /app/data

# Expose common ports (if needed for web interface in future)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ova-esxi-uploader --help || exit 1

# Default command
ENTRYPOINT ["ova-esxi-uploader"]
CMD ["--help"]