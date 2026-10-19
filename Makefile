GO ?= go
VERSION ?= dev
LDFLAGS ?= -s -w
BIN_DIR ?= bin

.PHONY: build host cloud test vet dashboard images
build: host cloud

host:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS) -X github.com/thekiharani/monitor/internal/host.version=$(VERSION)" -o $(BIN_DIR)/monitor-host ./cmd/host

cloud:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS) -X main.version=$(VERSION)" -o $(BIN_DIR)/monitor-cloud ./cmd/cloud

dashboard:
	cd cloud/dashboard && pnpm install --frozen-lockfile && pnpm build

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

images:
	docker build -t monitor-host:local .
	./cloud/scripts/build-images.sh
