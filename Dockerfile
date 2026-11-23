# ==========================================
# Stage 1: Build the Go Builder Binary
# ==========================================
FROM golang:alpine AS builder

WORKDIR /app

ENV GOTOOLCHAIN=auto

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY src/go.mod src/go.sum ./
RUN go mod download

# Copy source code
COPY src/ ./

# Build optimized binary
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o crevo-builder app.go

# ==========================================
# Stage 2: Production Runtime with Node.js & npm
# ==========================================
FROM node:20-alpine

WORKDIR /app

# Install runtime utilities & git (required for npm dependencies & builds; coreutils provides GNU cp for instant hardlinks)
RUN apk add --no-cache ca-certificates git curl bash coreutils

# Install global build and validation tools (instant zero-network execution)
RUN npm install -g typescript esbuild vite

# Create required runtime directories
RUN mkdir -p storage/workspaces storage/logs prompts

# Pre-install workspace template dependencies into /opt/template-cache/node_modules during Docker build
# CRITICAL: The cache directory's basename MUST literally be "node_modules" because Node.js ESM
# module resolution resolves symlinks via realpath and ONLY searches parent folders named "node_modules".
# Stored in /opt so persistent disk mounts at /app/storage never shadow this cache.
COPY template-package.json /opt/template-cache/package.json
RUN cd /opt/template-cache && \
    npm install --include=dev --no-audit --no-fund && \
    npm cache clean --force && \
    mkdir -p /app/storage/workspaces/.shared_cache && \
    ln -sf /opt/template-cache/node_modules /app/storage/workspaces/.shared_cache/node_modules && \
    node /opt/template-cache/node_modules/vite/bin/vite.js --version && \
    mkdir -p /tmp/smoke-ws/src && \
    ln -s /opt/template-cache/node_modules /tmp/smoke-ws/node_modules && \
    printf '{"name":"smoke","private":true,"type":"module"}\n' > /tmp/smoke-ws/package.json && \
    printf '<!doctype html><html><head><meta charset="UTF-8"/></head><body><div id="root"></div><script type="module" src="/src/main.tsx"></script></body></html>\n' > /tmp/smoke-ws/index.html && \
    printf "import { defineConfig } from 'vite';\nimport react from '@vitejs/plugin-react';\nimport tailwindcss from '@tailwindcss/vite';\nexport default defineConfig({ base: './', plugins: [react(), tailwindcss()], build: { reportCompressedSize: false } });\n" > /tmp/smoke-ws/vite.config.ts && \
    printf '@import "tailwindcss";\n' > /tmp/smoke-ws/src/index.css && \
    printf "import React from 'react';\nimport { createRoot } from 'react-dom/client';\nimport { Sparkles } from 'lucide-react';\nimport './index.css';\ncreateRoot(document.getElementById('root') as HTMLElement).render(<div className='p-4 text-sm'><Sparkles /></div>);\n" > /tmp/smoke-ws/src/main.tsx && \
    cd /tmp/smoke-ws && \
    NODE_OPTIONS="--max-old-space-size=256" node /tmp/smoke-ws/node_modules/vite/bin/vite.js build --logLevel error && \
    test -f /tmp/smoke-ws/dist/index.html && \
    rm -rf /tmp/smoke-ws

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

# Render Free Tier memory optimization (512MB RAM ceiling)
ENV NODE_OPTIONS="--max-old-space-size=256"
ENV GOMEMLIMIT=160MiB
ENV GOGC=50

EXPOSE 8080

# Run with dynamic PORT support for Render/Koyeb/Fly/Railway
CMD ["sh", "-c", "/app/crevo-builder --host 0.0.0.0 --port ${PORT:-8080} --key ${CREVO_KEY:-${WEBBY_KEY:-$SERVER_KEY}}"]
