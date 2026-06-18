# Define variables for the application
APP_NAME = llama-swap
BUILD_DIR = build

# Get the current Git hash
GIT_HASH := $(shell git rev-parse --short HEAD)
ifneq ($(shell git status --porcelain),)
    # There are untracked changes
    GIT_HASH := $(GIT_HASH)+
endif

# Capture the current build date in RFC3339 format
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Default target: Builds binaries for both OSX and Linux
all: mac linux simple-responder

# Clean build directory
clean:
	rm -rf $(BUILD_DIR)

# use cached test results while developing
test-dev:
	go test -short ./...
	staticcheck ./... || true

test:
	go test -short -count=1 ./internal/...

# for CI - full test (takes longer)
test-all:
	go test -race -count=1 ./internal/...

ui/node_modules:
	cd ui-svelte && npm install

# build react UI
ui: ui/node_modules
	cd ui-svelte && npm run build
	touch internal/server/ui_dist/placeholder.txt

# Build OSX binary
mac: ui
	@echo "Building Mac binary..."
	GOOS=darwin GOARCH=arm64 go build -ldflags="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-darwin-arm64

# Build Linux binary
linux: linux-arm64 linux-amd64

linux-amd64: ui
	@echo "Building Linux AMD64 binary..."
	GOOS=linux GOARCH=amd64 go build -ldflags="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-linux-amd64

linux-arm64: ui
	@echo "Building Linux ARM64 binary..."
	GOOS=linux GOARCH=arm64 go build -ldflags="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-linux-arm64

# Build Windows binary
windows: ui
	@echo "Building Windows binary..."
	GOOS=windows GOARCH=amd64 go build -ldflags="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-windows-amd64.exe

# Assemble a runnable Windows folder + zip (binary, configs, launcher, service files).
# NOTE: llama-server.exe (llama.cpp) and GGUF models are NOT bundled — separate
# projects/licensing. Copy quartermaster-generate.example.yaml to
# quartermaster-generate.yaml and edit modelsRoot / serverExe before use.
PKG_WIN_DIR = $(BUILD_DIR)/llama-quartermaster-windows
package-windows: windows
	@echo "Packaging Windows bundle..."
	# Preserve an existing personal generate file across the bundle rebuild.
	@if [ -f $(PKG_WIN_DIR)/quartermaster-generate.yaml ]; then \
		cp $(PKG_WIN_DIR)/quartermaster-generate.yaml $(BUILD_DIR)/.qm-generate.keep; fi
	rm -rf $(PKG_WIN_DIR)
	mkdir -p $(PKG_WIN_DIR)
	cp $(BUILD_DIR)/$(APP_NAME)-windows-amd64.exe $(PKG_WIN_DIR)/
	cp quartermaster-generate.example.yaml $(PKG_WIN_DIR)/quartermaster-generate.example.yaml
	# Seed the runtime generate file from the example only when none exists yet,
	# so a re-package never clobbers the user's edited quartermaster-generate.yaml.
	@if [ -f $(BUILD_DIR)/.qm-generate.keep ]; then \
		mv $(BUILD_DIR)/.qm-generate.keep $(PKG_WIN_DIR)/quartermaster-generate.yaml; \
		echo "  preserved existing quartermaster-generate.yaml"; \
	else \
		cp quartermaster-generate.example.yaml $(PKG_WIN_DIR)/quartermaster-generate.yaml; \
		echo "  seeded quartermaster-generate.yaml from example"; fi
	cp config.example.yaml $(PKG_WIN_DIR)/config.example.yaml
	cp packaging/windows/start.cmd $(PKG_WIN_DIR)/start.cmd
	cp -r packaging $(PKG_WIN_DIR)/packaging
	@echo "$(APP_NAME) $(GIT_HASH) built $(BUILD_DATE)" > $(PKG_WIN_DIR)/VERSION.txt
	cd $(BUILD_DIR) && ( zip -qr llama-quartermaster-windows.zip llama-quartermaster-windows \
		|| tar -a -c -f llama-quartermaster-windows.zip llama-quartermaster-windows \
		|| echo "WARN: no zip/tar found — folder left unarchived at $(PKG_WIN_DIR)" )
	@echo "Done: $(PKG_WIN_DIR)  (+ $(BUILD_DIR)/llama-quartermaster-windows.zip)"

# for testing with real external processes
simple-responder:
	@echo "Building simple responder"
	GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/simple-responder_darwin_arm64 cmd/simple-responder/simple-responder.go
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/simple-responder_linux_amd64 cmd/simple-responder/simple-responder.go

simple-responder-windows:
	@echo "Building simple responder for windows"
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/simple-responder.exe cmd/simple-responder/simple-responder.go

# Ensure build directory exists
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# Create a new release tag
release:
	@echo "Checking for unstaged changes..."
	@if [ -n "$(shell git status --porcelain)" ]; then \
		echo "Error: There are unstaged changes. Please commit or stash your changes before creating a release tag." >&2; \
		exit 1; \
	fi

# Get the highest tag in v{number} format, increment it, and create a new tag
	@highest_tag=$$(git tag --sort=-v:refname | grep -E '^v[0-9]+$$' | head -n 1 || echo "v0"); \
	new_tag="v$$(( $${highest_tag#v} + 1 ))"; \
	echo "tagging new version: $$new_tag"; \
	git tag "$$new_tag";

GOOS ?= $(shell go env GOOS 2>/dev/null || echo linux)
GOARCH ?= $(shell go env GOARCH 2>/dev/null || echo amd64)
wol-proxy: $(BUILD_DIR)
	@echo "Building wol-proxy"
	go build -o $(BUILD_DIR)/wol-proxy-$(GOOS)-$(GOARCH)-$(shell date +%Y-%m-%d) cmd/wol-proxy/wol-proxy.go

test-ui:
	cd ui-svelte && npm ci && npm run check && npm test

# Phony targets
.PHONY: all clean ui mac windows package-windows simple-responder simple-responder-windows test test-all test-dev test-ui wol-proxy
.PHONE: linux linux-arm64 linux-amd64
