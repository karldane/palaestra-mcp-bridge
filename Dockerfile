# Stage 1: Builder
FROM golang:1.25 AS builder

WORKDIR /build

# Install CGO dependencies for go-sqlite3
RUN apt-get update && apt-get install -y gcc libc-dev && rm -rf /var/lib/apt/lists/*

# Download dependencies first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Build palaestra-mcp-bridge
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -trimpath -o /usr/local/bin/mcp-bridge .

# Install all Go backends
# GOPROXY=direct + GONOSUMDB bypasses proxy cache and sum DB for our private repos
# (qdrant-mcp tag was re-pointed after initial registration - sum DB has stale hash)
ENV GONOSUMDB=github.com/karldane/*
ENV GONOSUMCHECK=github.com/karldane/*
RUN GOPROXY=direct GONOSUMDB=github.com/karldane/* GOBIN=/usr/local/bin go install github.com/karldane/qdrant-mcp@v0.2.9
RUN GOPROXY=direct GONOSUMDB=github.com/karldane/* GOBIN=/usr/local/bin go install github.com/karldane/newrelic-mcp@latest
RUN GOPROXY=direct GONOSUMDB=github.com/karldane/* GOBIN=/usr/local/bin go install github.com/karldane/oracle-mcp@latest
RUN GOPROXY=direct GONOSUMDB=github.com/karldane/* GOBIN=/usr/local/bin go install github.com/karldane/slack-mcp@latest
RUN GOPROXY=direct GONOSUMDB=github.com/karldane/* GOBIN=/usr/local/bin go install github.com/karldane/appscan-asoc-mcp@latest
RUN GOPROXY=direct GONOSUMDB=github.com/karldane/* GOBIN=/usr/local/bin go install github.com/karldane/git-lsp-mcp@latest
RUN GOPROXY=direct GOBIN=/usr/local/bin go install github.com/github/github-mcp-server/cmd/github-mcp-server@v1.0.5

# Stage 2: Final image
FROM ubuntu:24.04

# Avoid interactive prompts during apt installs
ENV DEBIAN_FRONTEND=noninteractive

# Install runtimes and tools
RUN apt-get update && apt-get install -y \
    ca-certificates \
    nodejs \
    npm \
    python3 \
    python3-pip \
    sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# Install uv (for uvx / Python MCP servers)
RUN pip3 install --no-cache-dir uv --break-system-packages

# Copy all binaries from builder
COPY --from=builder /usr/local/bin/mcp-bridge /usr/local/bin/mcp-bridge
COPY --from=builder /usr/local/bin/qdrant-mcp /usr/local/bin/qdrant-mcp
COPY --from=builder /usr/local/bin/newrelic-mcp /usr/local/bin/newrelic-mcp
COPY --from=builder /usr/local/bin/oracle-mcp /usr/local/bin/oracle-mcp
COPY --from=builder /usr/local/bin/slack-mcp /usr/local/bin/slack-mcp
COPY --from=builder /usr/local/bin/appscan-asoc-mcp /usr/local/bin/appscan-asoc-mcp
COPY --from=builder /usr/local/bin/git-lsp-mcp /usr/local/bin/git-lsp-mcp
COPY --from=builder /usr/local/bin/github-mcp-server /usr/local/bin/github-mcp-server

# Bake in default config for first-run DB seeding
RUN mkdir -p /etc/mcp-bridge
COPY config.yaml.docker /etc/mcp-bridge/config.yaml

# Bake in web UI templates
RUN mkdir -p /etc/mcp-bridge/templates
COPY --from=builder /build/templates /etc/mcp-bridge/templates

ENV CONFIG_FILE=/etc/mcp-bridge/config.yaml
ENV DB_PATH=/data/mcp-bridge.db
ENV PORT=8080
ENV TEMPLATE_DIR=/etc/mcp-bridge/templates

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mcp-bridge"]
