# OVA ESXi Uploader Makefile
# Build cross-platform binaries for release

# Application info
APP_NAME := ova-esxi-uploader
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Go build flags
LDFLAGS := -s -w
LDFLAGS += -X 'main.Version=$(VERSION)'
LDFLAGS += -X 'main.BuildTime=$(BUILD_TIME)'
LDFLAGS += -X 'main.GitCommit=$(GIT_COMMIT)'

# Build directory
BUILD_DIR := build
DIST_DIR := dist

# Supported platforms
PLATFORMS := \
	windows/amd64 \
	windows/arm64 \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64

.PHONY: all build clean test deps check release help

# Default target
all: clean deps test build

# Install dependencies
deps:
	@echo "Installing dependencies..."
	go mod tidy
	go mod download
	go mod verify

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Check code quality
check:
	@echo "Running code checks..."
	go vet ./...
	go fmt ./...
	@if command -v golint >/dev/null 2>&1; then \
		golint ./...; \
	else \
		echo "golint not installed, skipping lint check"; \
	fi
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not installed, skipping static analysis"; \
	fi

# Build for current platform
build:
	@echo "Building for current platform..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) .

# Build for all platforms
build-all: clean
	@echo "Building for all platforms..."
	@mkdir -p $(BUILD_DIR)
	$(foreach PLATFORM,$(PLATFORMS),$(call build_platform,$(PLATFORM)))

# Build for specific platform (usage: make build-linux)
build-linux:
	@$(call build_platform,linux/amd64)

build-windows:
	@$(call build_platform,windows/amd64)

build-darwin:
	@$(call build_platform,darwin/amd64)

build-linux-arm:
	@$(call build_platform,linux/arm64)

# Create release packages
release: clean deps test check build-all
	@echo "Creating release packages..."
	@mkdir -p $(DIST_DIR)
	$(foreach PLATFORM,$(PLATFORMS),$(call package_platform,$(PLATFORM)))
	@echo "Release packages created in $(DIST_DIR)/"
	@ls -la $(DIST_DIR)/

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR) $(DIST_DIR)
	@rm -f .upload-session-*.json

# Install to local system
install: build
	@echo "Installing $(APP_NAME) to /usr/local/bin..."
	@sudo cp $(BUILD_DIR)/$(APP_NAME) /usr/local/bin/
	@echo "Installation complete. Run '$(APP_NAME) --help' to get started."

# Uninstall from local system
uninstall:
	@echo "Uninstalling $(APP_NAME) from /usr/local/bin..."
	@sudo rm -f /usr/local/bin/$(APP_NAME)
	@echo "Uninstallation complete."

# Development build with race detection
dev:
	@echo "Building development version with race detection..."
	@mkdir -p $(BUILD_DIR)
	go build -race -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)-dev .

# Run the application
run:
	go run . $(ARGS)

# Run with sample arguments
demo:
	@echo "Running demo (dry run)..."
	go run . upload --help

# Check for security vulnerabilities
security:
	@echo "Checking for security vulnerabilities..."
	@if command -v gosec >/dev/null 2>&1; then \
		gosec ./...; \
	else \
		echo "gosec not installed, skipping security scan"; \
		echo "Install with: go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest"; \
	fi

# Generate documentation
docs:
	@echo "Generating documentation..."
	@mkdir -p docs
	go run . --help > docs/CLI_USAGE.md 2>&1 || true
	@echo "Documentation generated in docs/"

# Create Docker image
docker:
	@echo "Building Docker image..."
	docker build -t $(APP_NAME):$(VERSION) .
	docker tag $(APP_NAME):$(VERSION) $(APP_NAME):latest

# Show version information
version:
	@echo "Application: $(APP_NAME)"
	@echo "Version: $(VERSION)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo "Git Commit: $(GIT_COMMIT)"

# Help target
help:
	@echo "OVA ESXi Uploader Build System"
	@echo ""
	@echo "Available targets:"
	@echo "  all           - Clean, install deps, test, and build for current platform"
	@echo "  build         - Build for current platform"
	@echo "  build-all     - Build for all supported platforms"
	@echo "  build-linux   - Build for Linux AMD64"
	@echo "  build-windows - Build for Windows AMD64"
	@echo "  build-darwin  - Build for macOS AMD64"
	@echo "  build-linux-arm - Build for Linux ARM64"
	@echo "  release       - Create release packages for all platforms"
	@echo "  clean         - Clean build artifacts"
	@echo "  deps          - Install dependencies"
	@echo "  test          - Run tests"
	@echo "  check         - Run code quality checks"
	@echo "  install       - Install to local system (/usr/local/bin)"
	@echo "  uninstall     - Remove from local system"
	@echo "  dev           - Build development version with race detection"
	@echo "  run           - Run the application (use ARGS='...' for arguments)"
	@echo "  demo          - Show command help"
	@echo "  security      - Run security vulnerability scan"
	@echo "  docs          - Generate documentation"
	@echo "  docker        - Build Docker image"
	@echo "  version       - Show version information"
	@echo "  help          - Show this help message"
	@echo ""
	@echo "Examples:"
	@echo "  make build"
	@echo "  make release"
	@echo "  make run ARGS='upload vm.ova esxi.example.com --help'"
	@echo "  make build-linux"

# Helper function to build for a specific platform
define build_platform
	$(eval GOOS := $(word 1,$(subst /, ,$(1))))
	$(eval GOARCH := $(word 2,$(subst /, ,$(1))))
	$(eval EXT := $(if $(filter windows,$(GOOS)),.exe,))
	$(eval BINARY := $(BUILD_DIR)/$(APP_NAME)-$(GOOS)-$(GOARCH)$(EXT))
	@echo "Building $(BINARY)..."
	@GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .
endef

# Helper function to package a platform build
define package_platform
	$(eval GOOS := $(word 1,$(subst /, ,$(1))))
	$(eval GOARCH := $(word 2,$(subst /, ,$(1))))
	$(eval EXT := $(if $(filter windows,$(GOOS)),.exe,))
	$(eval BINARY := $(APP_NAME)-$(GOOS)-$(GOARCH)$(EXT))
	$(eval ARCHIVE := $(APP_NAME)-$(VERSION)-$(GOOS)-$(GOARCH))
	@echo "Packaging $(ARCHIVE)..."
	@if [ "$(GOOS)" = "windows" ]; then \
		cd $(BUILD_DIR) && zip -q ../$(DIST_DIR)/$(ARCHIVE).zip $(BINARY) && cd ..; \
		cp README.md $(DIST_DIR)/$(ARCHIVE)-README.md; \
	else \
		cd $(BUILD_DIR) && tar -czf ../$(DIST_DIR)/$(ARCHIVE).tar.gz $(BINARY) && cd ..; \
		cp README.md $(DIST_DIR)/$(ARCHIVE)-README.md; \
	fi
endef