GO ?= go

APP ?= osmmini
BIN_DIR ?= bin
PBF ?= region.osm.pbf
SETTINGS ?= settings.json
LISTEN ?= :8080
ADMIN_TOKEN ?=
BAYERN_PBF ?= bayern.osm.pbf
GEOFABRIK_URL ?= https://download.geofabrik.de/europe/germany/bayern-latest.osm.pbf

.DEFAULT_GOAL := help

.PHONY: help build run bayern offline maplibre-assets pbf-download offline-prep test test-race vet fmt check check-js clean

help: ## Show available commands.
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the server into bin/osmmini.
	@mkdir -p "$(BIN_DIR)"
	$(GO) build -o "$(BIN_DIR)/$(APP)" ./cmd

run: ## Run with PBF, SETTINGS, LISTEN and optional ADMIN_TOKEN overrides.
	OSMMINI_ADMIN_TOKEN="$(ADMIN_TOKEN)" $(GO) run ./cmd -pbf "$(PBF)" -settings "$(SETTINGS)" -listen "$(LISTEN)"

bayern: ## Run the Bavaria profile; set ADMIN_TOKEN and optionally BAYERN_PBF.
	@if [ -z "$(ADMIN_TOKEN)" ]; then \
		echo "Set ADMIN_TOKEN before starting the Bavaria profile."; \
		exit 2; \
	fi
	OSMMINI_ADMIN_TOKEN="$(ADMIN_TOKEN)" $(GO) run ./cmd -pbf "$(BAYERN_PBF)" -settings settings.bayern.json -listen "$(LISTEN)"

offline: ## Run the fully local tinyTiles profile; set ADMIN_TOKEN and PBF.
	@if [ -z "$(ADMIN_TOKEN)" ]; then \
		echo "Set ADMIN_TOKEN before starting the offline profile."; \
		exit 2; \
	fi
	OSMMINI_ADMIN_TOKEN="$(ADMIN_TOKEN)" $(GO) run ./cmd -pbf "$(PBF)" -settings settings.tinytiles.json -listen "$(LISTEN)"

maplibre-assets: ## Refresh the pinned local MapLibre GL assets (network required).
	@mkdir -p cmd/web/static/maplibre
	curl -fsSL --retry 3 https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.js -o cmd/web/static/maplibre/maplibre-gl.js
	curl -fsSL --retry 3 https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.css -o cmd/web/static/maplibre/maplibre-gl.css
	@echo "be9633c4d870e26fb37f1cfe5c5a77181667114003ea16207ac7850d8da8add1  cmd/web/static/maplibre/maplibre-gl.js" | shasum -a 256 -c -
	@echo "576b085fdd9487a65a19215328c1e086c07ce5bf6da09b666b3806d3d008dae9  cmd/web/static/maplibre/maplibre-gl.css" | shasum -a 256 -c -

pbf-download: ## Download a Geofabrik PBF extract into PBF (set GEOFABRIK_URL to pick a region; skips if PBF already exists, use FORCE=1 to refetch).
	@if [ -e "$(PBF)" ] && [ "$(FORCE)" != "1" ]; then \
		echo "$(PBF) already exists, skipping (use FORCE=1 to refetch)."; \
	else \
		echo "Downloading $(GEOFABRIK_URL) -> $(PBF) ..."; \
		curl -fsSL --retry 3 "$(GEOFABRIK_URL)" -o "$(PBF)"; \
	fi

offline-prep: pbf-download maplibre-assets ## Prepare everything needed for a fully offline deployment: PBF extract + vendored MapLibre GL assets (no CDN needed at runtime).
	@echo ""
	@echo "Offline assets ready:"
	@echo "  PBF:      $(PBF)"
	@echo "  MapLibre: cmd/web/static/maplibre/maplibre-gl.{js,css}"
	@echo ""
	@echo "Next: build the local tinyTiles map + start the offline profile:"
	@echo "  make offline ADMIN_TOKEN=<token>"
	@echo "Then use the 'Offline-Karte (tinyTiles)' source in Einstellungen to build the vector basemap from the loaded PBF."

test: ## Run all unit tests.
	$(GO) test ./...

test-race: ## Run the test suite with Go's race detector.
	$(GO) test -race ./...

vet: ## Run Go static analysis.
	$(GO) vet ./...

fmt: ## Format all Go packages.
	$(GO) fmt ./...

check: test vet build ## Run the standard Go verification suite.

check-js: ## Validate the browser JavaScript syntax (requires Node.js).
	node --check cmd/web/app.js

clean: ## Remove locally built binaries.
	rm -rf "$(BIN_DIR)"
