# MCP Bridge Makefile

BINARY_NAME=mcp-bridge
BUILD_DIR=.
LDFLAGS=-ldflags="-s -w" -trimpath

# Default target - downloads dependencies and builds
.PHONY: all
all: deps build

# Download and verify dependencies
.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	@GOPROXY=direct GOSUMDB=off go mod tidy
	@echo "Dependencies ready"

# Build the binary
.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) .
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"
	@du -h $(BUILD_DIR)/$(BINARY_NAME) | cut -f1

# Build for multiple platforms
.PHONY: build-all
build-all: deps build-linux build-darwin build-windows

.PHONY: build-linux
build-linux:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 .

.PHONY: build-darwin
build-darwin:
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 .

.PHONY: build-windows
build-windows:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe .

# Run tests
.PHONY: test
test:
	go test ./... -v

# Clean build artifacts
.PHONY: clean
clean:
	rm -f $(BUILD_DIR)/$(BINARY_NAME)
	rm -f $(BUILD_DIR)/$(BINARY_NAME)-*

# Install locally
.PHONY: install
install: build
	go install $(LDFLAGS) .

# Show help
.PHONY: help
help:
	@echo "MCP Bridge"
	@echo ""
	@echo "Usage:"
	@echo "  make              - Download dependencies and build binary"
	@echo "  make deps         - Download and verify dependencies"
	@echo "  make build        - Build the binary"
	@echo "  make build-all    - Build for all platforms (Linux, macOS, Windows)"
	@echo "  make test         - Run tests"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make install      - Install binary to GOPATH/bin"
	@echo "  make help         - Show this help message"