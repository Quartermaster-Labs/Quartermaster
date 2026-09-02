# Define variables for the application
APP_NAME = quartermaster
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

ui-svelte/node_modules:
	cd ui-svelte && npm install

# build the Svelte UI (embedded into every binary)
ui: ui-svelte/node_modules
	cd ui-svelte && npm run build
	touch internal/server/ui_dist/placeholder.txt

# Build OSX binary
mac: ui
	@echo "Building Mac binary..."
	GOOS=darwin GOARCH=arm64 go build -ldflags="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-darwin-arm64 ./cmd/quartermaster

# Build Linux binary
linux: linux-arm64 linux-amd64

linux-amd64: ui
	@echo "Building Linux AMD64 binary..."
	GOOS=linux GOARCH=amd64 go build -ldflags="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-linux-amd64 ./cmd/quartermaster

linux-arm64: ui
	@echo "Building Linux ARM64 binary..."
	GOOS=linux GOARCH=arm64 go build -ldflags="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-linux-arm64 ./cmd/quartermaster

# Windows VERSIONINFO resource. cmd/quartermaster/resource_windows_amd64.syso is
# committed (Go links any .syso in the main package automatically, which is why it
# and favicon.ico live beside the main package rather than at the repo root), and packaging/windows/build-release.ps1
# calls `go build` directly -- so the committed file, not this rule, is what a
# release actually ships. Commit it after it regenerates.
#
# These are FILE targets, not phony ones, so make rebuilds them exactly when
# favicon.ico or versioninfo.json is newer and skips the tool otherwise. The
# windows/setup-windows targets depend on them, because an icon change that only
# touches favicon.ico used to leave the exe carrying the previous icon with no
# warning. FileDescription is what the
# Windows Startup apps list / Task Manager shows for the autostart entry, so it
# must read "Quartermaster" — keep it in sync with the Run value name in
# internal/server/autostart.go. NOTE: a 0.0.0.0 FixedFileInfo makes Windows drop
# the whole string table; keep the version non-zero.
#
# -icon embeds favicon.ico as the exe's application icon (resource ID 1). Without
# it the exe has no icon at all, so Explorer, the taskbar, and the Startup apps
# list all fall back to the blank generic-executable glyph.
QM_SYSO = cmd/quartermaster/resource_windows_amd64.syso
SETUP_SYSO = cmd/quartermaster-setup/resource_windows_amd64.syso

$(QM_SYSO): cmd/quartermaster/favicon.ico cmd/quartermaster/versioninfo.json
	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -icon cmd/quartermaster/favicon.ico -o $@ cmd/quartermaster/versioninfo.json

versioninfo: $(QM_SYSO)

# Same, for the setup program. Its own .syso, in its own package directory: a
# .syso is linked by the main package that sits beside it, so the cmd/quartermaster
# one reaches the server binary and nothing else. Without this the wizard's exe has
# the blank generic-executable glyph in Explorer AND in the taskbar, which is
# the first thing a user sees of the app.
$(SETUP_SYSO): cmd/quartermaster/favicon.ico cmd/quartermaster-setup/versioninfo.json
	go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -icon cmd/quartermaster/favicon.ico -o $@ cmd/quartermaster-setup/versioninfo.json

versioninfo-setup: $(SETUP_SYSO)

# Build the first-run wizard's UI bundle (embedded into cmd/quartermaster-setup,
# NOT into the server binary — see internal/setup/api.go).
ui-setup: ui-svelte/node_modules
	cd ui-svelte && npm run build:setup

# Build the setup program: a native window (WebView2) that asks the first-run
# questions and drives the Inno installer silently.
#
# A dev build embeds only the zero-byte placeholder at cmd/quartermaster-setup/
# inno/setup.exe, so it falls back to copying whatever quartermaster binaries sit
# next to it — which makes this a runnable end-to-end test of the wizard (probe,
# scan, backend download, config write, launch) with no Inno toolchain in the
# loop. Copy the exe into build/quartermaster-windows/ first if you want it to
# pick up the config examples too. The release build
# (packaging/windows/build-release.ps1) is what embeds the real installer.
setup-windows: ui-setup $(SETUP_SYSO)
	@echo "Building Windows setup program..."
	GOOS=windows GOARCH=amd64 go build -ldflags="-H=windowsgui -X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-setup.exe ./cmd/quartermaster-setup

# Build Windows binary
windows: ui $(QM_SYSO)
	@echo "Building Windows binary..."
	GOOS=windows GOARCH=amd64 go build -ldflags="-H=windowsgui -X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE}" -o $(BUILD_DIR)/$(APP_NAME)-windows-amd64.exe ./cmd/quartermaster

# Assemble a runnable Windows folder + zip (binary, configs, launcher, service files).
# NOTE: llama-server.exe (llama.cpp) and GGUF models are NOT bundled — separate
# projects/licensing. Copy quartermaster-generate.example.yaml to
# quartermaster-generate.yaml and edit modelsRoot / serverExe before use.
PKG_WIN_DIR = $(BUILD_DIR)/quartermaster-windows
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
	# templates/ is no longer shipped; the rm stays so an existing install that
	# still has the folder from an older package loses it on the next re-package.
	rm -rf $(PKG_WIN_DIR)/packaging $(PKG_WIN_DIR)/templates
	# Installed as Quartermaster.exe; the old lowercase build-artifact name is
	# removed so a re-packaged bundle does not keep two copies, one of which
	# would never be updated again.
	cp $(BUILD_DIR)/$(APP_NAME)-windows-amd64.exe $(PKG_WIN_DIR)/Quartermaster.exe
	rm -f $(PKG_WIN_DIR)/$(APP_NAME)-windows-amd64.exe
	cp quartermaster-generate.example.yaml $(PKG_WIN_DIR)/config/quartermaster-generate.example.yaml
	# Seed the runtime generate file from the example only when none exists yet,
	# so a re-package never clobbers the user's edited quartermaster-generate.yaml.
	@if [ -f $(PKG_WIN_DIR)/config/quartermaster-generate.yaml ]; then \
		echo "  kept existing config/quartermaster-generate.yaml"; \
	else \
		cp quartermaster-generate.example.yaml $(PKG_WIN_DIR)/config/quartermaster-generate.yaml; \
		echo "  seeded config/quartermaster-generate.yaml from example"; fi
	cp config.example.yaml $(PKG_WIN_DIR)/config/config.example.yaml
	cp -r packaging $(PKG_WIN_DIR)/packaging
	@echo "$(APP_NAME) $(GIT_HASH) built $(BUILD_DATE)" > $(PKG_WIN_DIR)/VERSION.txt
	# Zip a CLEAN distributable, OPT-IN via ZIP=1. The bundle doubles as a live
	# install, so it accumulates ~1.3 GB of FETCHED artifacts the archive must not
	# carry: bin/ (backend exes the installer wizard downloads on first run) and
	# config/titlegen/ (the 79 MB title model, fetched on demand). Deflating those
	# turned a seconds-long re-package into a minutes-long one — and nothing
	# consumes this zip today (installer.iss and build-release.ps1 both build from
	# the folder), so it is no longer made by default.
	# Excluded beyond that: user data living in the bundle — private chats, the
	# regenerated-on-launch config.yaml, and the overrides sidecar, which holds
	# the user's API keys and must never ship.
	@if [ "$(ZIP)" != "1" ]; then \
		echo "  skipped quartermaster-windows.zip (use ZIP=1 to build it)"; \
	else \
		cd $(BUILD_DIR) && rm -f quartermaster-windows.zip && \
		if command -v zip >/dev/null 2>&1; then \
			zip -qr quartermaster-windows.zip quartermaster-windows \
				-x 'quartermaster-windows/playground-data/*' \
				   'quartermaster-windows/.cache/*' \
				   'quartermaster-windows/logs/*' \
				   'quartermaster-windows/bin/*' \
				   'quartermaster-windows/config/titlegen/*' \
				   'quartermaster-windows/config/config.yaml' \
				   'quartermaster-windows/config/quartermaster-overrides.yaml'; \
		elif command -v tar >/dev/null 2>&1; then \
			tar -a -c -f quartermaster-windows.zip \
				--exclude='quartermaster-windows/playground-data' \
				--exclude='quartermaster-windows/.cache' \
				--exclude='quartermaster-windows/logs' \
				--exclude='quartermaster-windows/bin' \
				--exclude='quartermaster-windows/config/titlegen' \
				--exclude='quartermaster-windows/config/config.yaml' \
				--exclude='quartermaster-windows/config/quartermaster-overrides.yaml' \
				quartermaster-windows; \
		else \
			echo "WARN: no zip/tar found — folder left unarchived at $(PKG_WIN_DIR)"; \
		fi; \
	fi
	@echo "Done: $(PKG_WIN_DIR)"

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
	rm -rf $(PKG_NIX_DIR)/packaging $(PKG_NIX_DIR)/templates
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
	# Same exclusions as the Windows bundle: user data (chats, the regenerated
	# config.yaml, and the API-key-bearing overrides sidecar) plus the fetched
	# artifacts a live install accumulates (bin/, the slot-KV cache, the title
	# model). The tarball IS the linux/mac deliverable, so it is not opt-in.
	cd $(BUILD_DIR) && rm -f $(APP_NAME)-$(NIX_OS).tar.gz && \
		tar --exclude='$(APP_NAME)-$(NIX_OS)/playground-data' \
			--exclude='$(APP_NAME)-$(NIX_OS)/logs' \
			--exclude='$(APP_NAME)-$(NIX_OS)/.cache' \
			--exclude='$(APP_NAME)-$(NIX_OS)/bin' \
			--exclude='$(APP_NAME)-$(NIX_OS)/config/titlegen' \
			--exclude='$(APP_NAME)-$(NIX_OS)/config/config.yaml' \
			--exclude='$(APP_NAME)-$(NIX_OS)/config/quartermaster-overrides.yaml' \
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

# Build the four published binaries + SHA256SUMS into $(BUILD_DIR)/dist.
#
# This is the UPDATE deliverable: the app is one self-contained binary, so the
# in-app updater (internal/update) downloads exactly one of these files and
# renames it over the running exe. The names MUST match
# internal/update.assetName() — a name that drifts means that platform silently
# stops seeing updates.
#
# VERSION=vX.Y.Z stamps the release version; without it the binaries are marked
# local_<hash> and, by design, never offer themselves an update.
DIST_DIR = $(BUILD_DIR)/dist
DIST_VERSION = $(if $(VERSION),$(VERSION),local_$(GIT_HASH))
DIST_LDFLAGS = -X main.commit=$(GIT_HASH) -X main.version=$(DIST_VERSION) -X main.date=$(BUILD_DATE)
dist: ui
	@echo "Building release binaries ($(DIST_VERSION))..."
	@mkdir -p $(DIST_DIR)
	# CGO is off across the project, so all four targets cross-compile from any
	# one host with nothing but the Go toolchain.
	# -H=windowsgui: no console window on launch, including the relaunch that
	# follows a self-update.
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-H=windowsgui $(DIST_LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME)-windows-amd64.exe ./cmd/quartermaster
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="$(DIST_LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME)-linux-amd64 ./cmd/quartermaster
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="$(DIST_LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME)-linux-arm64 ./cmd/quartermaster
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="$(DIST_LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME)-darwin-arm64 ./cmd/quartermaster
	# sha256sum's own format, which is what the updater's SHA256SUMS fallback
	# parses. Generated from inside the dir so the entries are bare names.
	@cd $(DIST_DIR) && rm -f SHA256SUMS && \
		if command -v sha256sum >/dev/null 2>&1; then \
			sha256sum $(APP_NAME)-* > SHA256SUMS; \
		else \
			shasum -a 256 $(APP_NAME)-* > SHA256SUMS; \
		fi
	@cat $(DIST_DIR)/SHA256SUMS
	@echo "Done: $(DIST_DIR)"

# GitHub repo for `gh` (avoids "no default remote" when multiple remotes exist).
RELEASE_REPO ?= Quartermaster-Labs/Quartermaster

# Build every release artifact LOCALLY and upload it to a GitHub release
# (private-repo Actions minutes are metered; local build is free). What ships by
# default is the setup programs (one per platform) + SHA256SUMS, and nothing
# else: a release page is a download page. The four bare server binaries are
# still BUILT -- the Windows one is the installer's payload -- but publishing
# them is opt-in via -PublishBinaries, which the updater and the unix wizards
# need. See the .PARAMETER block in build-release.ps1 for what that costs.
#   make release VERSION=v0.5.1          -> DRAFT release of that tag
#   make release-public VERSION=v0.5.1   -> same, but PUBLIC
#   make release-binaries VERSION=v0.5.1 -> binaries + sums only, no installer
#
# VERSION is MANDATORY. It used to default to the highest existing tag, which
# built whatever HEAD was and published it under a version that tag need not
# point at. build-release.ps1 also refuses a dirty tree, and refuses a tag that
# already exists anywhere but HEAD.
# Needs go, npm, gh (authed), and — unless -SkipInstaller — Inno Setup 6 (ISCC).
release:
	@$(MAKE) --no-print-directory _build-release DRAFT=true

release-public:
	@$(MAKE) --no-print-directory _build-release DRAFT=false

# Skips ISCC entirely: a patch release that every existing install will pick up
# through the updater does not need a new first-install wizard, and this is also
# the fast way to test the update path itself.
release-binaries:
	@$(MAKE) --no-print-directory _build-release DRAFT=true RELEASE_EXTRA="-SkipInstaller -PublishBinaries"

_build-release:
	@if [ -z "$(VERSION)" ]; then \
		echo "Error: VERSION=vMAJOR.MINOR.PATCH is required (e.g. make release VERSION=v0.5.1)" >&2; exit 1; fi
	@echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { \
		echo "Error: VERSION must be vMAJOR.MINOR.PATCH (e.g. v0.5.1)" >&2; exit 1; }
	# Created on HEAD when it does not exist yet. build-release.ps1 then verifies
	# the tag IS HEAD, so a tag that already lives on another commit stops the
	# release instead of quietly shipping that commit under this version.
	@git rev-parse "$(VERSION)" >/dev/null 2>&1 || git tag "$(VERSION)"
	powershell -NoProfile -ExecutionPolicy Bypass \
		-File packaging/windows/build-release.ps1 -Tag $(VERSION) -Draft $(DRAFT) -Repo $(RELEASE_REPO) $(RELEASE_EXTRA)

GOOS ?= $(shell go env GOOS 2>/dev/null || echo linux)
GOARCH ?= $(shell go env GOARCH 2>/dev/null || echo amd64)

test-ui:
	cd ui-svelte && npm ci && npm run check && npm test

# Phony targets
.PHONY: all clean ui dist mac windows versioninfo versioninfo-setup ui-setup setup-windows package-windows package-linux package-mac _package-nix simple-responder simple-responder-windows test test-all test-dev test-ui release release-public release-binaries _build-release
.PHONY: linux linux-arm64 linux-amd64
