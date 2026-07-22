VERSION ?= 0.3.1
PLUGIN_NAME ?= codex-agent-identity
PLUGIN_DIR ?= plugin/codex-agent-identity
BUILD_DIR ?= dist
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

.PHONY: test race vet build build-sidecar build-plugin package-plugin checksums clean

test:
	$(GO) test ./... -count=1
	cd $(PLUGIN_DIR) && $(GO) test ./... -count=1

race:
	$(GO) test -race ./... -count=1
	cd $(PLUGIN_DIR) && $(GO) test -race ./... -count=1

vet:
	$(GO) vet ./...
	cd $(PLUGIN_DIR) && $(GO) vet ./...

build: build-sidecar build-plugin

build-sidecar:
	mkdir -p $(dir $(SIDECAR_OUTPUT))
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -buildvcs=false -ldflags "-s -w" -o $(SIDECAR_OUTPUT) ./cmd/sidecar

build-plugin:
	mkdir -p $(dir $(PLUGIN_OUTPUT))
	cd $(PLUGIN_DIR) && CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -buildvcs=false -buildmode=c-shared -ldflags "-s -w -X main.pluginVersion=$(VERSION)" -o "$(abspath $(PLUGIN_OUTPUT))" .
	rm -f $(PLUGIN_HEADER)

package-plugin: build-plugin
	GOOS= GOARCH= CGO_ENABLED=0 $(GO) run ./.github/scripts/package-release.go -library "$(PLUGIN_OUTPUT)" -archive "$(PLUGIN_ARCHIVE)" -checksum "$(PLUGIN_CHECKSUM)"

checksums: package-plugin
	cat $(BUILD_DIR)/*.sha256 | sort -k 2 > $(BUILD_DIR)/checksums.txt

clean:
	rm -rf $(BUILD_DIR)
