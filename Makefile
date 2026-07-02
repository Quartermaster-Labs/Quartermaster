# Define variables for the application
APP_NAME = llama-quartermaster
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
	# Refresh the bundle IN PLACE — overwrite only the shipped artifacts and never
	# touch user data: playground-data/ (chats/prefs/logins), runtime config.yaml,
	# the user-edited quartermaster-generate.yaml, and the UI-owned overrides
	# sidecar (quartermaster-overrides.yaml, holds API keys) all survive untouched.
	# Only the regenerated packaging/ subtree is removed first, to drop files that
	# were renamed/removed across versions. (No `rm -rf` of the bundle dir itself:
	# on Windows a file-watcher often holds a handle and it fails "resource busy".)
	mkdir -p $(PKG_WIN_DIR)/config
	rm -rf $(PKG_WIN_DIR)/packaging
	cp $(BUILD_DIR)/$(APP_NAME)-windows-amd64.exe $(PKG_WIN_DIR)/
	cp quartermaster-generate.example.yaml $(PKG_WIN_DIR)/config/quartermaster-generate.example.yaml
	# Seed the runtime generate file from the example only when none exists yet,
	# so a re-package never clobbers the user's edited quartermaster-generate.yaml.
	@if [ -f $(PKG_WIN_DIR)/config/quartermaster-generate.yaml ]; then \
		echo "  kept existing config/quartermaster-generate.yaml"; \
	else \
		cp quartermaster-generate.example.yaml $(PKG_WIN_DIR)/config/quartermaster-generate.yaml; \
		echo "  seeded config/quartermaster-generate.yaml from example"; fi
	cp config.example.yaml $(PKG_WIN_DIR)/config/config.example.yaml
	cp packaging/windows/start.cmd $(PKG_WIN_DIR)/start.cmd
	cp -r packaging $(PKG_WIN_DIR)/packaging
	@echo "$(APP_NAME) $(GIT_HASH) built $(BUILD_DATE)" > $(PKG_WIN_DIR)/VERSION.txt
	# Zip a CLEAN distributable: exclude the user-data that now lives in the bundle
	# (private chats + the regenerated-on-launch config.yaml) so it never ships.
	cd $(BUILD_DIR) && rm -f llama-quartermaster-windows.zip && \
		( zip -qr llama-quartermaster-windows.zip llama-quartermaster-windows \
			-x 'llama-quartermaster-windows/playground-data/*' \
			-x 'llama-quartermaster-windows/logs/*' \
			-x 'llama-quartermaster-windows/config/config.yaml' \
		|| tar -a -c -f llama-quartermaster-windows.zip \
			--exclude='llama-quartermaster-windows/playground-data' \
			--exclude='llama-quartermaster-windows/logs' \
			--exclude='llama-quartermaster-windows/config/config.yaml' \
			llama-quartermaster-windows \
		|| echo "WARN: no zip/tar found — folder left unarchived at $(PKG_WIN_DIR)" )
	@echo "Done: $(PKG_WIN_DIR)  (+ $(BUILD_DIR)/llama-quartermaster-windows.zip)"

# Assemble a runnable Linux/Mac folder + tar.gz. Same in-place, user-data-preserving
# refresh as package-windows; no launcher .cmd (the binary runs directly), but the
# systemd unit ships under packaging/. ponytail: amd64 linux / arm64 mac only — add
# more arches here if a release ever needs them.
PKG_NIX_BIN_linux = $(APP_NAME)-linux-amd64
PKG_NIX_BIN_mac   = $(APP_NAME)-darwin-arm64
package-linux: linux-amd64
	@$(MAKE) --no-print-directory _package-nix NIX_OS=linux NIX_BIN=$(PKG_NIX_BIN_linux)
package-mac: mac
	@$(MAKE) --no-print-directory _package-nix NIX_OS=mac NIX_BIN=$(PKG_NIX_BIN_mac)

# NIX_OS = linux|mac, NIX_BIN = built binary filename in $(BUILD_DIR)
_package-nix:
	@echo "Packaging $(NIX_OS) bundle..."
	$(eval PKG_NIX_DIR := $(BUILD_DIR)/$(APP_NAME)-$(NIX_OS))
	mkdir -p $(PKG_NIX_DIR)/config
	rm -rf $(PKG_NIX_DIR)/packaging
	cp $(BUILD_DIR)/$(NIX_BIN) $(PKG_NIX_DIR)/
	cp quartermaster-generate.example.yaml $(PKG_NIX_DIR)/config/quartermaster-generate.example.yaml
	@if [ -f $(PKG_NIX_DIR)/config/quartermaster-generate.yaml ]; then \
		echo "  kept existing config/quartermaster-generate.yaml"; \
	else \
		cp quartermaster-generate.example.yaml $(PKG_NIX_DIR)/config/quartermaster-generate.yaml; \
		echo "  seeded config/quartermaster-generate.yaml from example"; fi
	cp config.example.yaml $(PKG_NIX_DIR)/config/config.example.yaml
	cp -r packaging $(PKG_NIX_DIR)/packaging
	@echo "$(APP_NAME) $(GIT_HASH) built $(BUILD_DATE)" > $(PKG_NIX_DIR)/VERSION.txt
	cd $(BUILD_DIR) && rm -f $(APP_NAME)-$(NIX_OS).tar.gz && \
		tar --exclude='$(APP_NAME)-$(NIX_OS)/playground-data' \
			--exclude='$(APP_NAME)-$(NIX_OS)/logs' \
			--exclude='$(APP_NAME)-$(NIX_OS)/config/config.yaml' \
			-czf $(APP_NAME)-$(NIX_OS).tar.gz $(APP_NAME)-$(NIX_OS)
	@echo "Done: $(PKG_NIX_DIR)  (+ $(BUILD_DIR)/$(APP_NAME)-$(NIX_OS).tar.gz)"

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

# GitHub repo for `gh` (avoids "no default remote" when multiple remotes exist).
RELEASE_REPO ?= Quartermaster-Labs/llama-quartermaster

# Build the Windows binary + installer LOCALLY and upload the .exe to a GitHub
# release (private-repo Actions minutes are metered; local build is free).
#   make release                  -> latest existing vX.Y.Z tag, DRAFT
#   make release-public           -> same, but PUBLIC
#   make release VERSION=v0.5.1   -> creates that tag first, then releases it
# Needs go, npm, gh (authed), and Inno Setup 6 (ISCC) installed locally.
release:
	@$(MAKE) --no-print-directory _build-release DRAFT=true

release-public:
	@$(MAKE) --no-print-directory _build-release DRAFT=false

_build-release:
	@targ=""; \
	if [ -n "$(VERSION)" ]; then \
		echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { \
			echo "Error: VERSION must be vMAJOR.MINOR.PATCH (e.g. v0.5.1)" >&2; exit 1; }; \
		git rev-parse "$(VERSION)" >/dev/null 2>&1 || git tag "$(VERSION)"; \
		targ="-Tag $(VERSION)"; \
	fi; \
	powershell -NoProfile -ExecutionPolicy Bypass \
		-File packaging/windows/build-release.ps1 $$targ -Draft $(DRAFT) -Repo $(RELEASE_REPO)

GOOS ?= $(shell go env GOOS 2>/dev/null || echo linux)
GOARCH ?= $(shell go env GOARCH 2>/dev/null || echo amd64)
wol-proxy: $(BUILD_DIR)
	@echo "Building wol-proxy"
	go build -o $(BUILD_DIR)/wol-proxy-$(GOOS)-$(GOARCH)-$(shell date +%Y-%m-%d) cmd/wol-proxy/wol-proxy.go

test-ui:
	cd ui-svelte && npm ci && npm run check && npm test

# Phony targets
.PHONY: all clean ui mac windows package-windows package-linux package-mac _package-nix simple-responder simple-responder-windows test test-all test-dev test-ui wol-proxy release release-public _build-release
.PHONE: linux linux-arm64 linux-amd64
