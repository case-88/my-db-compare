.PHONY: build test test-coverage lint clean deps fmt vet

# Build variables
BINARY_NAME=my-db-compare
BUILD_DIR=./build
MAIN_PATH=./cmd/my-db-compare

# Go variables
GOFLAGS=-ldflags="-s -w"

# Default target
all: deps fmt vet test build

# Install dependencies
deps:
	go mod tidy
	go mod download

# Format code
fmt:
	go fmt ./...

# Vet code
vet:
	go vet ./...

# Run tests
test:
	go test -v -race ./...

# Run tests with coverage
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Run linter (requires golangci-lint)
lint:
	golangci-lint run ./...

# Build binary
build:
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME)"

# Build for multiple platforms
build-all:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(MAIN_PATH)
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Run the application (for development)
run:
	go run $(MAIN_PATH) $(ARGS)

# Show help
help:
	@echo "Available targets:"
	@echo "  deps          - Install dependencies"
	@echo "  fmt           - Format code"
	@echo "  vet           - Vet code"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage report"
	@echo "  lint          - Run linter"
	@echo "  build         - Build binary"
	@echo "  build-all     - Build for multiple platforms"
	@echo "  clean         - Clean build artifacts"
	@echo "  run           - Run application (use ARGS=... for arguments)"
