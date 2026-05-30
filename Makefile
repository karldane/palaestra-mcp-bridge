# MCP Bridge Makefile

BINARY_NAME=mcp-bridge
BUILD_DIR=.
LDFLAGS=-ldflags="-s -w" -trimpath
DOCKER_TAG=latest

# Discover all commands under cmd/
CMDS := $(patsubst cmd/%/,%,$(wildcard cmd/*/))

DOCKER_IMAGE_FILE := .git/info/docker-image
ECR_REGION_FILE := .git/info/ecr-region
K8S_DEPLOYMENT_FILE := .git/info/k8s-deployment
K8S_NAMESPACE_FILE := .git/info/k8s-namespace

ifneq ($(wildcard $(DOCKER_IMAGE_FILE)),)
  DOCKER_IMAGE := $(shell cat $(DOCKER_IMAGE_FILE))
endif

ifneq ($(wildcard $(ECR_REGION_FILE)),)
  ECR_REGION := $(shell cat $(ECR_REGION_FILE))
endif

ifneq ($(wildcard $(K8S_DEPLOYMENT_FILE)),)
  K8S_DEPLOYMENT := $(shell cat $(K8S_DEPLOYMENT_FILE))
endif

ifneq ($(wildcard $(K8S_NAMESPACE_FILE)),)
  K8S_NAMESPACE := $(shell cat $(K8S_NAMESPACE_FILE))
endif

# ECR registry is derived from DOCKER_IMAGE (the host part)
ECR_REGISTRY := $(firstword $(subst /, ,$(DOCKER_IMAGE)))

# Default target - downloads dependencies and builds
.PHONY: all
all: deps build

# Download and verify dependencies
.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	@GOPROXY=direct GOSUMDB=off go mod tidy
	@echo "Dependencies ready"

# Build the binary and all cmd/* tools
.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) .
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"
	@du -h $(BUILD_DIR)/$(BINARY_NAME) | cut -f1
	@for cmd in $(CMDS); do \
		echo "Building $$cmd..."; \
		go build $(LDFLAGS) -o $(BUILD_DIR)/$$cmd ./cmd/$$cmd; \
		du -h $(BUILD_DIR)/$$cmd | cut -f1; \
	done

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

# Check for legacy encryption code in non-test files
.PHONY: check-no-legacy
check-no-legacy:
	@grep -rn "UpdateMasterKeyEncrypted\|SetUserTokenEncrypted\|encryption_type.*=.*'legacy'" \
	    --include='*.go' --exclude='*_test.go' \
	    store/ web/ cmd/ internal/ \
	    && (echo "ERROR: legacy encryption code detected" && exit 1) || true

# Clean build artifacts
.PHONY: clean
clean:
	rm -f $(BUILD_DIR)/$(BINARY_NAME)
	rm -f $(BUILD_DIR)/$(BINARY_NAME)-*
	for cmd in $(CMDS); do rm -f $(BUILD_DIR)/$$cmd; done

# Install locally
.PHONY: install
install: build
	go install $(LDFLAGS) .

# Validate that DOCKER_IMAGE and ECR_REGION are configured
.PHONY: ensure-docker-config
ensure-docker-config:
	@if [ -z "$(DOCKER_IMAGE)" ]; then \
		echo "ERROR: DOCKER_IMAGE is not configured."; \
		echo "Create $(DOCKER_IMAGE_FILE) with your container registry path."; \
		echo "Example: echo 'registry.example.com/my-image' > $(DOCKER_IMAGE_FILE)"; \
		exit 1; \
	fi
	@if [ -z "$(ECR_REGION)" ]; then \
		echo "ERROR: ECR region is not configured."; \
		echo "Create $(ECR_REGION_FILE) with your AWS region."; \
		echo "Example: echo 'eu-west-2' > $(ECR_REGION_FILE)"; \
		exit 1; \
	fi

# Build base image (runtimes + backends — rarely needed)
.PHONY: docker-build-base
docker-build-base: ensure-docker-config
	docker build -f Dockerfile.base -t $(DOCKER_IMAGE):base-latest .

# Build and push base image
.PHONY: docker-push-base
docker-push-base: docker-build-base
	aws ecr get-login-password --region $(ECR_REGION) | docker login --username AWS --password-stdin $(ECR_REGISTRY)
	docker push $(DOCKER_IMAGE):base-latest

# Build Docker image (fast: layers mcp-bridge on existing base)
# Set REBUILD_BACKENDS=1 to rebuild base image first (backends + runtimes)
.PHONY: docker-build
docker-build: ensure-docker-config
	@if [ -n "$(REBUILD_BACKENDS)" ]; then \
		$(MAKE) docker-build-base; \
	fi
	docker build \
		--build-arg BASE_IMAGE=$(DOCKER_IMAGE):base-latest \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Build and push Docker image (authenticates to ECR, then pushes)
.PHONY: docker-push
docker-push: docker-build
	aws ecr get-login-password --region $(ECR_REGION) | docker login --username AWS --password-stdin $(ECR_REGISTRY)
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)
	@if [ -n "$(REBUILD_BACKENDS)" ]; then \
		docker push $(DOCKER_IMAGE):base-latest; \
	fi

# Validate that K8S_DEPLOYMENT and K8S_NAMESPACE are configured
.PHONY: ensure-k8s-config
ensure-k8s-config:
	@if [ -z "$(K8S_DEPLOYMENT)" ]; then \
		echo "ERROR: K8S_DEPLOYMENT is not configured."; \
		echo "Create $(K8S_DEPLOYMENT_FILE) with your deployment name."; \
		echo "Example: echo 'mcp-bridge' > $(K8S_DEPLOYMENT_FILE)"; \
		exit 1; \
	fi
	@if [ -z "$(K8S_NAMESPACE)" ]; then \
		echo "ERROR: K8S_NAMESPACE is not configured."; \
		echo "Create $(K8S_NAMESPACE_FILE) with your Kubernetes namespace."; \
		echo "Example: echo 'mcp-bridge-infra' > $(K8S_NAMESPACE_FILE)"; \
		exit 1; \
	fi

# Trigger a rolling restart of the deployment in Kubernetes
.PHONY: k8s-reload
k8s-reload: ensure-k8s-config
	kubectl rollout restart deployment/$(K8S_DEPLOYMENT) -n $(K8S_NAMESPACE)

# Full pipeline: build, push, and deploy by deleting old pod (new pod pulls fresh image)
.PHONY: deploy
deploy: docker-push
	kubectl delete pod -n $(K8S_NAMESPACE) -l app=$(K8S_DEPLOYMENT) --wait=false

# Show help
.PHONY: help
help:
	@echo "MCP Bridge"
	@echo ""
	@echo "Usage:"
	@echo "  make              - Download dependencies and build binary"
	@echo "  make deps         - Download and verify dependencies"
	@echo "  make build        - Build binary and cmd/* tools"
	@echo "  make build-all    - Build for all platforms (Linux, macOS, Windows)"
	@echo "  make test         - Run tests"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make install      - Install binary to GOPATH/bin"
	@echo "  make docker-build-base - Build base image (runtimes + backends, rarely needed)"
	@echo "  make docker-push-base  - Build and push base image to ECR"
	@echo "  make docker-build      - Build Docker image (fast: mcp-bridge on base)"
	@echo "  make docker-build      REBUILD_BACKENDS=1  # rebuild base first"
	@echo "  make docker-push       - Authenticate to ECR, build and push"
	@echo "  make k8s-reload        - Trigger rolling restart of deployment"
	@echo "  make deploy            - Build, push, and redeploy (deletes old pod, new pod pulls fresh image)"
	@echo "  make help         - Show this help message"
