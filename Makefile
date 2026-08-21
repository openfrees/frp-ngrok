.DEFAULT_GOAL := help
.PHONY: help build dev install uninstall status package mac icons fmt vet clean

BIN := frp-ngrok

help:
	@echo "frp-ngrok · dev commands"
	@echo ""
	@echo "  make dev        build and run the service in the foreground"
	@echo "  make build      build ./$(BIN) for this machine"
	@echo "  make install    build, install, enable login item, open the console"
	@echo "  make uninstall  uninstall the local service and binary"
	@echo "  make status     print running status"
	@echo "  make package    build all-platform artifacts into ./dist"
	@echo "  make mac        build the macOS .app only"
	@echo "  make icons      regenerate app and menu bar icons"
	@echo "  make fmt vet    format and vet"

# 菜单栏需要原生界面接口，本机构建一律开启 CGO
build:
	CGO_ENABLED=1 go build -trimpath -o $(BIN) .

dev: build
	./$(BIN) serve

install: build
	./$(BIN) install
	./$(BIN) open

uninstall:
	@./$(BIN) uninstall 2>/dev/null || go run . uninstall

status:
	@go run . status

package:
	@bash scripts/package.sh

mac:
	@bash scripts/package.sh mac

icons:
	go run ./cmd/genicon

fmt:
	gofmt -w .

vet:
	go vet ./...

clean:
	rm -rf dist $(BIN)
