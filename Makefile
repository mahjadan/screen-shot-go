SHELL := /usr/bin/env bash
FRONTEND_DIR := frontend

.PHONY: help check tools frontend install dev build run clean

help:
	@printf '%s\n' \
		'make check    Verify go, npm, and wails3 are available' \
		'make install  Install frontend dependencies' \
		'make dev      Run wails3 dev after environment checks' \
		'make build    Build the frontend and Go app' \
		'make run      Run the built binary' \
		'make clean    Remove generated frontend and binary artifacts'

check: tools
	@printf 'Environment check passed.\n'

tools:
	@command -v go >/dev/null || { echo 'Missing dependency: go'; exit 1; }
	@command -v npm >/dev/null || { echo 'Missing dependency: npm'; exit 1; }
	@command -v wails3 >/dev/null || { echo 'Missing dependency: wails3'; exit 1; }
	@printf 'go: %s\n' "$$(go version)"
	@printf 'npm: %s\n' "$$(npm --version)"
	@printf 'wails3: %s\n' "$$(wails3 version)"

install: tools
	@npm --prefix $(FRONTEND_DIR) install

frontend: install
	@npm --prefix $(FRONTEND_DIR) run build

dev: install
	@wails3 dev

build: frontend
	@mkdir -p bin
	@go build -o ./bin/screenshot-go .

run: build
	@./bin/screenshot-go

clean:
	@rm -rf ./bin
	@find ./$(FRONTEND_DIR)/dist -mindepth 1 ! -name 'index.html' -delete 2>/dev/null || true
