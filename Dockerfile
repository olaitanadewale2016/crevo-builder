# ==========================================
# Stage 1: Build the Go Builder Binary
# ==========================================
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY src/go.mod src/go.sum ./
RUN go mod download

# Copy source code
COPY src/ ./

# Build optimized binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o crevo-builder app.go

# ==========================================
# Stage 2: Production Runtime with Node.js & npm
# ==========================================
FROM node:20-alpine

WORKDIR /app

# Install runtime utilities & git (required for npm dependencies & builds)
RUN apk add --no-cache ca-certificates git curl bash

# Create required runtime directories
RUN mkdir -p storage/workspaces storage/logs prompts

# Copy compiled binary from builder stage
COPY --from=builder /app/crevo-builder /app/crevo-builder

# Copy required AI prompts
COPY src/prompts/ /app/prompts/

# Make binary executable
RUN chmod +x /app/crevo-builder

# Default environment variables
ENV HOST=0.0.0.0
ENV PORT=8080
ENV CREVO_DEBUG=false

EXPOSE 8080

# Run with dynamic PORT support for Render/Koyeb/Fly/Railway
CMD ["sh", "-c", "/app/crevo-builder --host 0.0.0.0 --port ${PORT:-8080} --key ${CREVO_KEY:-${WEBBY_KEY:-$SERVER_KEY}}"]
