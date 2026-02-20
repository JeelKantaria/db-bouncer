BINARY_NAME=dbbouncer
BUILD_DIR=bin
GO=go
GOFLAGS=-v
LDFLAGS=-s -w

.PHONY: all build clean test lint run docker-build docker-push helm-install

all: build

build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/dbbouncer/

clean:
	rm -rf $(BUILD_DIR)

test:
	$(GO) test $(GOFLAGS) ./...

test-cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run ./...

run: build
	./$(BUILD_DIR)/$(BINARY_NAME) -config configs/dbbouncer.yaml

docker-build:
	docker build -t dbbouncer:latest .

docker-push: docker-build
	docker push dbbouncer:latest

helm-install:
	helm install dbbouncer deploy/helm/dbbouncer

helm-upgrade:
	helm upgrade dbbouncer deploy/helm/dbbouncer

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

mod-tidy:
	$(GO) mod tidy
