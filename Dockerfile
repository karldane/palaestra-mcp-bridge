ARG BASE_IMAGE=ghcr.io/karldane/palaestra-mcp-bridge:base-latest

# Stage 1: Builder — builds mcp-bridge only
FROM golang:1.25 AS builder

WORKDIR /build

RUN apt-get update && apt-get install -y gcc libc-dev && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -trimpath -o /usr/local/bin/mcp-bridge .

# Stage 2: Final — thin layer on top of base image
FROM ${BASE_IMAGE} AS final

COPY --from=builder /usr/local/bin/mcp-bridge /usr/local/bin/mcp-bridge

RUN mkdir -p /etc/mcp-bridge
COPY config.yaml.docker /etc/mcp-bridge/config.yaml

RUN mkdir -p /etc/mcp-bridge/templates
COPY --from=builder /build/templates /etc/mcp-bridge/templates

ENV CONFIG_FILE=/etc/mcp-bridge/config.yaml
ENV DB_PATH=/data/mcp-bridge.db
ENV PORT=8080
ENV TEMPLATE_DIR=/etc/mcp-bridge/templates

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mcp-bridge"]
