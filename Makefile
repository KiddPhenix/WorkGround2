VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOEXE := $(shell go env GOEXE)
ADDONS_ROOT ?= ../wg2addons

.PHONY: build build-addons vet fmt test hooks cross clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/workground2$(GOEXE) ./cmd/workground2
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/workground2-plugin-example$(GOEXE) ./cmd/workground2-plugin-example

build-addons:
	powershell -NoProfile -ExecutionPolicy Bypass -File "$(ADDONS_ROOT)/scripts/build-addons.ps1" -WorkGround2Root "$(CURDIR)"

vet:
	go vet ./...

fmt:
	gofmt -w .

test:
	go test -count=1 ./...

hooks:
	@git config core.hooksPath .githooks
	@echo "installed: core.hooksPath -> .githooks (pre-push runs go vet)"

cross:
	@mkdir -p dist
	@for p in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o dist/workground2-$$os-$$arch$$ext ./cmd/workground2; \
	done

clean:
	rm -rf bin dist
