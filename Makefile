GO ?= go

APP ?= osmmini
BIN_DIR ?= bin
PBF ?= region.osm.pbf
SETTINGS ?= settings.json
LISTEN ?= :8080
ADMIN_TOKEN ?=
BAYERN_PBF ?= bayern.osm.pbf
GEOFABRIK_URL ?= https://download.geofabrik.de/europe/germany/bayern-latest.osm.pbf
# sqliteimport enables tinySQL's pure-Go (modernc.org/sqlite, no cgo) MBTiles/
# GeoPackage import (see cmd/geodata_tiles.go). Does not affect cmd/wasm,
# which is built separately with its own GOOS=js GOARCH=wasm invocation.
GOTAGS ?= sqliteimport

.DEFAULT_GOAL := help

.PHONY: help build run bayern offline micromap-assets ensure-micromap-assets maplibre-assets ensure-maplibre-assets pbf-download offline-prep test test-js test-race vet fmt check check-js clean

help: ## Show available commands.
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ensure-micromap-assets ensure-maplibre-assets ## Build the server into bin/osmmini.
	@mkdir -p "$(BIN_DIR)"
	$(GO) build -tags=$(GOTAGS) -o "$(BIN_DIR)/$(APP)" ./cmd

run: ## Run with PBF, SETTINGS, LISTEN and optional ADMIN_TOKEN overrides.
	OSMMINI_ADMIN_TOKEN="$(ADMIN_TOKEN)" $(GO) run -tags=$(GOTAGS) ./cmd -pbf "$(PBF)" -settings "$(SETTINGS)" -listen "$(LISTEN)"

bayern: ## Run the Bavaria profile; set ADMIN_TOKEN and optionally BAYERN_PBF.
	@if [ -z "$(ADMIN_TOKEN)" ]; then \
		echo "Set ADMIN_TOKEN before starting the Bavaria profile."; \
		exit 2; \
	fi
	OSMMINI_ADMIN_TOKEN="$(ADMIN_TOKEN)" $(GO) run -tags=$(GOTAGS) ./cmd -pbf "$(BAYERN_PBF)" -settings settings.bayern.json -listen "$(LISTEN)"

offline: ## Run the fully local tinyTiles profile; set ADMIN_TOKEN and PBF.
	@if [ -z "$(ADMIN_TOKEN)" ]; then \
		echo "Set ADMIN_TOKEN before starting the offline profile."; \
		exit 2; \
	fi
	OSMMINI_ADMIN_TOKEN="$(ADMIN_TOKEN)" $(GO) run -tags=$(GOTAGS) ./cmd -pbf "$(PBF)" -settings settings.tinytiles.json -listen "$(LISTEN)"

micromap-assets: ## Rebuild microMap 0.3.0 from the pinned upstream commit (network required).
	@set -e; stage=$$(mktemp -d); trap 'rm -rf "$$stage"' EXIT; \
		git clone https://github.com/Karte-Bayern/microMap.git "$$stage/source"; \
		git -C "$$stage/source" checkout --detach 57e41ae403c916415ff779d9c17a77ea68523bb2; \
		(cd "$$stage/source" && npm ci && npm run build); \
		mkdir -p "$$stage/cmd/web/static/micromap"; \
		cp "$$stage/source/dist/micromap.mjs" "$$stage/source/LICENSE" "$$stage/cmd/web/static/micromap/"; \
		cp cmd/web/static/micromap/SHA256SUMS "$$stage/checksums"; \
		(cd "$$stage" && shasum -a 256 -c checksums); \
		cp "$$stage/cmd/web/static/micromap/micromap.mjs" "$$stage/cmd/web/static/micromap/LICENSE" cmd/web/static/micromap/

ensure-micromap-assets: ## Verify the committed microMap bundle and license (no network required).
	shasum -a 256 -c cmd/web/static/micromap/SHA256SUMS

maplibre-assets: ## Refresh the pinned MapLibre 6.7.0 ESM assets (network required).
	@mkdir -p cmd/web/static/maplibre
	@set -e; for asset in maplibre-gl.mjs maplibre-gl-shared.mjs maplibre-gl-worker.mjs maplibre-gl.css; do \
		curl -fsSL --retry 3 "https://unpkg.com/maplibre-gl@6.7.0/dist/$$asset" -o "cmd/web/static/maplibre/$$asset"; \
	done
	shasum -a 256 -c cmd/web/static/maplibre/SHA256SUMS

ensure-maplibre-assets: ## Verify local MapLibre assets, downloading them when missing or changed.
	@if [ -s cmd/web/static/maplibre/maplibre-gl.mjs ] && shasum -a 256 -c cmd/web/static/maplibre/SHA256SUMS >/dev/null 2>&1; then \
		echo "MapLibre assets are present and checksum-verified."; \
	else \
		$(MAKE) maplibre-assets; \
	fi

pbf-download: ## Download a Geofabrik PBF extract into PBF (set GEOFABRIK_URL to pick a region; skips if PBF already exists, use FORCE=1 to refetch).
	@if [ -e "$(PBF)" ] && [ "$(FORCE)" != "1" ]; then \
		echo "$(PBF) already exists, skipping (use FORCE=1 to refetch)."; \
	else \
		echo "Downloading $(GEOFABRIK_URL) -> $(PBF) ..."; \
		curl -fsSL --retry 3 "$(GEOFABRIK_URL)" -o "$(PBF)"; \
	fi

offline-prep: pbf-download ensure-micromap-assets maplibre-assets ## Prepare everything needed for a fully offline deployment: PBF extract + local microMap and MapLibre assets (no CDN needed at runtime).
	@echo ""
	@echo "Offline assets ready:"
	@echo "  PBF:      $(PBF)"
	@echo "  microMap: cmd/web/static/micromap/micromap.mjs"
	@echo "  MapLibre: cmd/web/static/maplibre/maplibre-gl.mjs + shared/worker modules + CSS"
	@echo ""
	@echo "Next: build the local tinyTiles map + start the offline profile:"
	@echo "  make offline ADMIN_TOKEN=<token>"
	@echo "Then use the 'Offline-Karte (tinyTiles)' source in Einstellungen to build the vector basemap from the loaded PBF."

test: ensure-micromap-assets ensure-maplibre-assets ## Run all unit tests.
	$(GO) test -tags=$(GOTAGS) ./...

test-race: ## Run the test suite with Go's race detector.
	$(GO) test -tags=$(GOTAGS) -race ./...

vet: ## Run Go static analysis.
	$(GO) vet -tags=$(GOTAGS) ./...

fmt: ## Format all Go packages.
	$(GO) fmt ./...

check: test vet build check-js test-js ## Run Go checks, build, and browser JavaScript checks (requires Node.js).

test-js: ## Run browser request and interaction regression tests (requires Node.js).
	node --test cmd/web_test/*.test.cjs

check-js: ## Validate the browser JavaScript syntax (requires Node.js).
	node --check cmd/web/app.js
	node --check cmd/web/map-renderer.js
	node --check cmd/web/offline-style.js

clean: ## Remove locally built binaries.
	rm -rf "$(BIN_DIR)"
