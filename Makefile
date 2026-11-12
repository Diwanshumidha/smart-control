VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BUN ?= bun
# Wails v2 on Linux: webkit2gtk-4.1 needs the webkit2_41 tag.
DESKTOP_TAGS ?= desktop,production$(if $(filter Linux,$(shell uname -s)),$(shell pkg-config --exists webkit2gtk-4.1 && echo ,webkit2_41),)

.PHONY: all web cli desktop test vet run run-desktop dev-web clean

all: web cli desktop

web:
	cd web && $(BUN) install --frozen-lockfile && $(BUN) run build

cli: web
	go build -ldflags "$(LDFLAGS)" -o bin/smart-control ./cmd/smart-control

desktop: web
	go build -tags $(DESKTOP_TAGS) -ldflags "$(LDFLAGS)" -o bin/smart-control-desktop ./cmd/smart-control-desktop

test:
	go test ./...

vet:
	go vet ./...

run: cli
	./bin/smart-control serve

run-desktop: desktop
	./bin/smart-control-desktop

# UI hot reload against a running `smart-control serve` (proxies /api to :8788).
dev-web:
	cd web && $(BUN) run dev

clean:
	rm -rf bin web/dist/assets web/dist/index.html
