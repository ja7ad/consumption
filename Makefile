.PHONY: build fmt

build:
	@echo "Building the project..."
	@VERSION=$$(./scripts/version.sh); \
	go build -ldflags "-X 'main.Version=$$VERSION'" -o ./build/consumption ./cmd/consumption/main.go

fmt:
	@echo "Formatting the code..."
	gofumpt -l -w .
