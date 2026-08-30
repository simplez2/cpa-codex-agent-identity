VERSION ?= $(strip $(file <VERSION))
PLUGIN_NAME ?= codex-agent-identity
PLUGIN_DIR ?= plugin/codex-agent-identity
BUILD_DIR ?= dist
ASSETS_DIR ?= $(BUILD_DIR)/release-assets
GO ?= go
GOOS ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)

EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
PLUGIN_EXT = $(or $(EXT_$(GOOS)),so)
PLUGIN_OUTPUT ?= $(BUILD_DIR)/$(PLUGIN_NAME).$(PLUGIN_EXT)
PLUGIN_HEADER = $(basename $(PLUGIN_OUTPUT)).h
PLUGIN_ARCHIVE ?= $(BUILD_DIR)/$(PLUGIN_NAME)_$(VERSION)_$(GOOS)_$(GOARCH).zip
PLUGIN_CHECKSUM ?= $(PLUGIN_ARCHIVE).sha256
SIDECAR_OUTPUT ?= $(BUILD_DIR)/cpa-codex-agent-identity-sidecar

.PHONY: test race vet verify-release-state verify-published-release publish-registry build build-sidecar build-plugin build-plugin-portable verify-plugin-compatibility package-plugin package-plugin-portable checksums clean

test:
	$(GO) test ./... -count=1
	cd $(PLUGIN_DIR) && $(GO) test ./... -count=1

race:
	$(GO) test -race ./... -count=1
	cd $(PLUGIN_DIR) && $(GO) test -race ./... -count=1

vet:
	$(GO) vet ./...
	cd $(PLUGIN_DIR) && $(GO) vet ./...

verify-release-state:
	$(GO) run ./.github/scripts/verify-release-state.go -root .

verify-published-release:
	$(GO) run ./.github/scripts/verify-release-state.go -root . -require-registry-match

publish-registry:
	$(GO) run ./.github/scripts/publish-registry.go -root . -assets-dir "$(ASSETS_DIR)"

ifeq ($(GOOS),linux)
build: build-sidecar verify-plugin-compatibility
else
build: build-sidecar build-plugin
endif

build-sidecar:
	mkdir -p $(dir $(SIDECAR_OUTPUT))
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -buildvcs=false -ldflags "-s -w" -o $(SIDECAR_OUTPUT) ./cmd/sidecar

build-plugin:
ifeq ($(GOOS),linux)
	bash .github/scripts/build-linux-plugin-portable.sh "$(VERSION)" "$(GOARCH)" "$(PLUGIN_OUTPUT)"
else
	mkdir -p $(dir $(PLUGIN_OUTPUT))
	cd $(PLUGIN_DIR) && CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -buildvcs=false -buildmode=c-shared -ldflags "-s -w -X main.pluginVersion=$(VERSION)" -o "$(abspath $(PLUGIN_OUTPUT))" .
	rm -f $(PLUGIN_HEADER)
endif

# Explicit alias used by release jobs and documentation. Linux build-plugin is
# portable by default so local packages cannot silently regress to a modern
# host GLIBC dependency.
build-plugin-portable: build-plugin

verify-plugin-compatibility: build-plugin
	bash .github/scripts/verify-linux-plugin.sh "$(PLUGIN_OUTPUT)" "$(GOARCH)"

ifeq ($(GOOS),linux)
package-plugin: verify-plugin-compatibility
else
package-plugin: build-plugin
endif
	GOOS= GOARCH= CGO_ENABLED=0 $(GO) run ./.github/scripts/package-release.go -library "$(PLUGIN_OUTPUT)" -archive "$(PLUGIN_ARCHIVE)" -checksum "$(PLUGIN_CHECKSUM)"
	mkdir -p "$(ASSETS_DIR)"
	cp "$(PLUGIN_ARCHIVE)" "$(PLUGIN_CHECKSUM)" "$(ASSETS_DIR)/"

package-plugin-portable: verify-plugin-compatibility
	GOOS= GOARCH= CGO_ENABLED=0 $(GO) run ./.github/scripts/package-release.go -library "$(PLUGIN_OUTPUT)" -archive "$(PLUGIN_ARCHIVE)" -checksum "$(PLUGIN_CHECKSUM)"
	mkdir -p "$(ASSETS_DIR)"
	cp "$(PLUGIN_ARCHIVE)" "$(PLUGIN_CHECKSUM)" "$(ASSETS_DIR)/"

checksums: package-plugin
	cat $(BUILD_DIR)/*.sha256 | sort -k 2 > $(BUILD_DIR)/checksums.txt

clean:
	rm -rf $(BUILD_DIR)
