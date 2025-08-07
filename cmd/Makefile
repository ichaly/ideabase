# Makefile for CLI tools

# Automatically detect all directories with main.go files
CLI_TOOLS := $(notdir $(patsubst %/main.go,%,$(wildcard */main.go)))

# Mark the following targets as phony (not associated with files)
.PHONY: all build clean help install $(addprefix build-,$(CLI_TOOLS)) $(addprefix install-,$(CLI_TOOLS)) $(addprefix debug-,$(CLI_TOOLS))

# Default target - build all CLI tools
all: build

# Build all CLI tools
build: $(addprefix build-,$(CLI_TOOLS))

# Build individual tools
$(addprefix build-,$(CLI_TOOLS)):
	@tool_name=$$(echo $@ | sed 's/build-//'); \
	cd $$tool_name && go build -o $$tool_name main.go $$(ls *.go | grep -v main.go | xargs echo)

# Install all tools to GOPATH/bin
install: build
	@for tool in $(CLI_TOOLS); do \
		echo "Installing $$tool..."; \
		cp $$tool/$$tool $${GOPATH}/bin/; \
	done
	echo "All tools installed to $${GOPATH}/bin/"

# Install individual tool to GOPATH/bin
$(addprefix install-,$(CLI_TOOLS)):
	@tool_name=$$(echo $@ | sed 's/install-//'); \
	cp $$tool_name/$$tool_name $${GOPATH}/bin/; \
	echo "$$tool_name installed to $${GOPATH}/bin/"

# Debug run individual tool
$(addprefix debug-,$(CLI_TOOLS)):
	@tool_name=$$(echo $@ | sed 's/debug-//'); \
	echo "Running $$tool_name in debug mode..."; \
	cd $$tool_name && CGO_ENABLED=0 go run main.go $$(ls *.go | grep -v main.go | xargs echo)

# Clean build artifacts
clean:
	@for tool in $(CLI_TOOLS); do \
		echo "Cleaning $$tool..."; \
		rm -f $$tool/$$tool; \
	done
	echo "All tools cleaned"


# Display help information
help:
	@echo "Makefile for CLI tools"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  all              Build all CLI tools (default target)"
	@echo "  build            Build all CLI tools"
	@echo "  build-<tool>     Build specific tool (e.g., build-install)"
	@echo "  clean            Clean build artifacts"
	@echo "  help             Display this help message"
	@echo "  install          Install all tools to GOPATH/bin"
	@echo "  install-<tool>   Install specific tool to GOPATH/bin (e.g., install-install)"
	@echo "  debug-<tool>     Run specific tool in debug mode (e.g., debug-install)"
	@echo ""
	@echo "Detected tools: $(CLI_TOOLS)"
	@echo ""
	@echo "For end users, it's recommended to use 'go install' instead:"
	@for tool in $(CLI_TOOLS); do \
		echo "  go install github.com/ichaly/ideabase/cli/$$tool@latest"; \
	done