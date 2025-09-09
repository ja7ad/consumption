build:
	@echo "Building the project..."
	go build -o ./build/consumption ./cmd/consumption/main.go

fmt:
	@echo "Formatting the code..."
	gofumpt -l -w .
