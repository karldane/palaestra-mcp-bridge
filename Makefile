# MCP Bridge Makefile

BINARY_NAME=mcp-bridge
BUILD_DIR=.
LDFLAGS=-ldflags="-s -w" -trimpath
DOCKER_IMAGE=ghcr.io/karldane/palaestra-mcp-bridge
DOCKER_TAG=latest

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

# Build Docker image
.PHONY: docker-build
docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Build and push Docker image to ghcr.io
.PHONY: docker-push
docker-push: docker-build
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

# Production ECR details (cross-account pull from staging)
ECR_REGISTRY=100732956864.dkr.ecr.eu-west-2.amazonaws.com
ECR_IMAGE=tusker-direct/mcp-bridge
ECR_TAG=latest

# Push Docker image to production ECR
.PHONY: docker-push-ecr
docker-push-ecr:
	aws ecr get-login-password --region eu-west-2 | docker login --username AWS --password-stdin $(ECR_REGISTRY)
	docker tag $(DOCKER_IMAGE):$(DOCKER_TAG) $(ECR_REGISTRY)/$(ECR_IMAGE):$(ECR_TAG)
	docker push $(ECR_REGISTRY)/$(ECR_IMAGE):$(ECR_TAG)

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
	@echo "  make docker-build - Build Docker image ($(DOCKER_IMAGE):$(DOCKER_TAG))"
	@echo "  make docker-push  - Build and push Docker image to ghcr.io"
	@echo "  make docker-push-ecr - Build and push to staging ECR"
	@echo "  make help         - Show this help message"