# Makefile for CLI tools

BINARY_NAME=release
BINARY_DIR=release
BINARY_PATH=$(BINARY_DIR)/$(BINARY_NAME)

# Go modules that contain CLI tools
CLI_MODULES=release

.PHONY: build clean install release all

# Build all CLI tools
build: $(CLI_MODULES)

# Build release tool
release:
	cd $(BINARY_DIR) && go build -o $(BINARY_NAME) main.go version.go

# Build all CLI tools
all:
	@echo "Building all CLI tools..."
	go build -o $(BINARY_PATH) ./release

# Clean build artifacts
clean:
	rm -f $(BINARY_PATH)

# Install release tool
install: build
	cp $(BINARY_PATH) ${GOPATH}/bin/$(BINARY_NAME)

# Run go mod tidy to ensure dependencies are up-to-date
tidy:
	go mod tidy

# List available targets
help:
	@echo "Available targets:"
	@echo "  build     - Build all CLI tools"
	@echo "  clean     - Clean build artifacts"
	@echo "  install   - Install release tool to GOPATH/bin"
	@echo "  release   - Build release tool"
	@echo "  all       - Build all CLI tools"
	@echo "  tidy      - Run go mod tidy"
	@echo "  help      - Show this help message"