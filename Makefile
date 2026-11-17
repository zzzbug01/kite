# kite builds three static binaries. CGO_ENABLED=0 is required for the
# release binaries. The race detector used by `make test` needs cgo, so the
# test recipe does not export CGO_ENABLED=0.

GO ?= go
BIN_DIR := bin
MODULE := github.com/zzzbug01/kite
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)

CMDS := kite kite-agent kite-relay
PLATFORMS := linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64

.PHONY: all test vet cross clean

all:
	@mkdir -p $(BIN_DIR)
	@set -e; for cmd in $(CMDS); do \
		echo "building $$cmd"; \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$$cmd ./cmd/$$cmd; \
	done

vet:
	$(GO) vet ./...

test: vet
	$(GO) test -race ./...

cross:
	@mkdir -p $(BIN_DIR)
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; arch=$${platform##*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		for cmd in $(CMDS); do \
			out=$(BIN_DIR)/$$cmd-$$os-$$arch$$ext; \
			echo "building $$out"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/$$cmd; \
		done; \
	done

clean:
	rm -rf $(BIN_DIR) dist
