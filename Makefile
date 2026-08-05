APP := jieshan
BIN_DIR := bin
WEB_DIR := web
GO ?= go
PNPM ?= pnpm
DOCKER ?= docker

.PHONY: help install build build-api build-web dev-api dev-web test lint fmt vet docker-build docker-up docker-down clean

help:
	@echo "JieShan development commands"
	@echo "  make install       Install frontend dependencies"
	@echo "  make build         Build frontend and backend"
	@echo "  make test          Run frontend and backend tests"
	@echo "  make docker-up     Pull and start the production container"

install:
	cd $(WEB_DIR) && $(PNPM) install --frozen-lockfile

build: build-web build-api

build-web:
	cd $(WEB_DIR) && $(PNPM) run build

build-api:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN_DIR)/$(APP) ./cmd/jieshan

dev-api:
	$(GO) run ./cmd/jieshan

dev-web:
	cd $(WEB_DIR) && $(PNPM) run dev

test:
	$(GO) test ./...
	cd $(WEB_DIR) && $(PNPM) run --if-present test

lint: vet
	cd $(WEB_DIR) && $(PNPM) run --if-present lint

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

docker-build:
	$(DOCKER) build -t $(APP):dev .

docker-up:
	$(DOCKER) compose pull
	$(DOCKER) compose up -d --no-build

docker-down:
	$(DOCKER) compose down

clean:
	rm -rf $(BIN_DIR) $(WEB_DIR)/dist
