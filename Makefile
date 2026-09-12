.PHONY: all build build-all test clean run

BINARY_NAME=tgdown

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo "v0.0.3")

all: test build

build:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.AppVersion=$(VERSION)" -o $(BINARY_NAME) .

build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.AppVersion=$(VERSION)" -o $(BINARY_NAME)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X main.AppVersion=$(VERSION)" -o $(BINARY_NAME)-linux-arm64 .

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-*

run: build
	./$(BINARY_NAME)
