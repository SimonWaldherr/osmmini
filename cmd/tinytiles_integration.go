package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tinytiles "github.com/Karte-Bayern/tinyTiles/v2"
	tinytilesserver "github.com/Karte-Bayern/tinyTiles/v2/server"
	tiles "github.com/SimonWaldherr/tinySQL/tiles"
	osmmini "simonwaldherr.de/go/osmmini"
)

const (
	tinyTilesDefaultMinZoom = 5
	tinyTilesDefaultMaxZoom = 14
	// Route-prefetching warms a small map corridor after a route response. The
	// tinyTiles server has a separate bounded queue; this is only the maximum
	// submission per route, never an unbounded download of the whole path.
	tinyTilesRoutePrefetchRadius   = 1
	tinyTilesRoutePrefetchMaxTiles = 512
)

// tinyTilesBuildStatus is deliberately small and public-facing: the UI can
// poll it without discovering local paths or implementation details.
type tinyTilesBuildStatus struct {
	State              string    `json:"state"`
	Phase              string    `json:"phase"`
	Progress           int       `json:"progress"`
	Message            string    `json:"message,omitempty"`
	Error              string    `json:"error,omitempty"`
	StartedAt          time.Time `json:"started_at,omitempty"`
	FinishedAt         time.Time `json:"finished_at,omitempty"`
	Artifact           string    `json:"artifact,omitempty"`
	MinZoom            int       `json:"min_zoom,omitempty"`
	MaxZoom            int       `json:"max_zoom,omitempty"`
	PostalCodes        bool      `json:"postal_codes,omitempty"`
	PostalPrefixLength int       `json:"postal_prefix_length,omitempty"`
	TerritoryLayer     string    `json:"territory_layer,omitempty"`
	Territories        int       `json:"territories,omitempty"`
	SourceBytes        int64     `json:"source_bytes,omitempty"`
	EstimatedDiskBytes int64     `json:"estimated_disk_bytes,omitempty"`
	EstimatedTileCount int64     `json:"estimated_tile_count,omitempty"`
	GeneratedTiles     int       `json:"generated_tiles,omitempty"`
	RoadFeatures       int       `json:"road_features,omitempty"`
	WaterwayFeatures   int       `json:"waterway_features,omitempty"`
	BuildingFeatures   int       `json:"building_features,omitempty"`
}

type tinyTilesBuildRequest struct {
	MinZoom            int  `json:"min_zoom"`
	MaxZoom            int  `json:"max_zoom"`
	PostalCodes        bool `json:"postal_codes"`
	PostalPrefixLength int  `json:"postal_prefix_length"`
}

func (s *server) handleTinyTilesBuild(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.tinyTilesMu.RLock()
		status := s.tinyTilesBuild
		s.tinyTilesMu.RUnlock()
		if status.State == "" {
			status = tinyTilesBuildStatus{
				State:    "idle",
				Phase:    "idle",
				Progress: 0,
				Message:  "Noch keine Offline-Karte erzeugt.",
			}
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodPost:
		if !s.requireSettingsAdmin(w, r) {
			return
		}
		var request tinyTilesBuildRequest
		if err := readJSON(w, r, &request, 4<<10); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ungültige Build-Anfrage: "+err.Error())
			return
		}
		if request.MinZoom == 0 {
			request.MinZoom = tinyTilesDefaultMinZoom
		}
		if request.MaxZoom == 0 {
			request.MaxZoom = tinyTilesDefaultMaxZoom
		}
		if request.PostalCodes && request.PostalPrefixLength == 0 {
			request.PostalPrefixLength = 3
		}
		if request.PostalCodes && (request.PostalPrefixLength < 1 || request.PostalPrefixLength > 5) {
			writeJSONError(w, http.StatusBadRequest, "PLZ-Präfixlänge muss zwischen 1 und 5 liegen")
			return
		}
		if request.MinZoom < tinyTilesDefaultMinZoom || request.MaxZoom > tinyTilesDefaultMaxZoom || request.MaxZoom < request.MinZoom {
			writeJSONError(w, http.StatusBadRequest, "ungültiger Zoom-Bereich; unterstützt werden 5 bis 14")
			return
		}
		pbfInfo, err := os.Stat(s.pbfPath)
		if err != nil || pbfInfo.IsDir() {
			writeJSONError(w, http.StatusBadRequest, "geladene PBF-Datei ist nicht verfügbar")
			return
		}
		// A previous postal build leaves an authoritative OSM boundary sidecar
		// beside the local artifact. Creating PLZ1 after PLZ3 must not rebuild
		// every vector tile from the PBF again.
		postalSidecar := filepath.Join(s.tinyTilesDir, "basemap.postcodes.geojson")
		reusePostalBoundaries := request.PostalCodes && regularFileExists(postalSidecar)

		s.tinyTilesMu.Lock()
		if s.tinyTilesBuild.State == "building" {
			s.tinyTilesMu.Unlock()
			writeJSONError(w, http.StatusConflict, "Offline-Karte wird bereits erzeugt")
			return
		}
		message := "Offline-Karte wird vorbereitet…"
		phase := "preparing"
		if reusePostalBoundaries {
			message = "Vorhandene OSM-PLZ-Grenzen werden für die neue Ebene aufbereitet…"
			phase = "territories"
		}
		status := tinyTilesBuildStatus{
			State:              "building",
			Phase:              phase,
			Progress:           0,
			Message:            message,
			StartedAt:          time.Now().UTC(),
			MinZoom:            request.MinZoom,
			MaxZoom:            request.MaxZoom,
			PostalCodes:        request.PostalCodes,
			PostalPrefixLength: request.PostalPrefixLength,
			SourceBytes:        pbfInfo.Size(),
		}
		status.WaterwayFeatures = s.tinyTilesWaterways.len()
		s.tinyTilesBuild = status
		s.tinyTilesMu.Unlock()
		if reusePostalBoundaries {
			go s.buildPostalTerritory(status, postalSidecar)
		} else {
			go s.buildTinyTiles(status)
		}
		writeJSON(w, http.StatusAccepted, status)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (s *server) buildTinyTiles(status tinyTilesBuildStatus) {
	// A startup backfill may be creating a missing companion layer. Keep its
	// publication serialized with a full build so an explicit rebuild always
	// wins and never gets paired with a concurrently written sidecar.
	s.tinyTilesWaterwayBuildMu.Lock()
	defer s.tinyTilesWaterwayBuildMu.Unlock()

	artifact := filepath.Join(s.tinyTilesDir, "basemap.ttiles")
	waterways := tinyTilesWaterwaySidecarPath(s.tinyTilesDir)
	if err := os.MkdirAll(s.tinyTilesDir, 0o755); err != nil {
		s.finishTinyTilesBuild("", "", 0, 0, 0, 0, fmt.Errorf("Wasserwege vorbereiten: %w", err))
		return
	}
	stagedWaterways, err := os.CreateTemp(s.tinyTilesDir, ".basemap.waterways-*")
	if err != nil {
		s.finishTinyTilesBuild("", "", 0, 0, 0, 0, fmt.Errorf("Wasserwege vorbereiten: %w", err))
		return
	}
	stagedWaterwaysPath := stagedWaterways.Name()
	if err := stagedWaterways.Close(); err != nil {
		_ = os.Remove(stagedWaterwaysPath)
		s.finishTinyTilesBuild("", "", 0, 0, 0, 0, fmt.Errorf("Wasserwege vorbereiten: %w", err))
		return
	}
	defer os.Remove(stagedWaterwaysPath)

	s.updateTinyTilesBuildProgress("waterways", 4, "Gewässer und 2,5D-Gebäude werden vorbereitet…")
	waterwayFeatures, err := buildTinyTilesWaterwaySidecar(s.pbfPath, stagedWaterwaysPath)
	if err != nil {
		s.finishTinyTilesBuild("", "", 0, 0, 0, 0, err)
		return
	}
	s.updateTinyTilesBuildProgress("generating", 5, "Kartendaten werden aus der PBF erzeugt…")
	result, err := tinytiles.BuildPBF(context.Background(), tinytiles.PBFBuildOptions{
		PBFInputs:       []string{s.pbfPath},
		ArtifactPath:    artifact,
		MinZoom:         status.MinZoom,
		MaxZoom:         status.MaxZoom,
		MaxMemoryBytes:  s.tinyTilesMaxMemory,
		MinFreeBytes:    0,
		PostalCodes:     status.PostalCodes,
		ReplaceExisting: true,
		Progress: func(progress tinytiles.PBFBuildProgress) {
			if progress.Import != nil && progress.Import.Estimate != nil {
				s.updateTinyTilesBuildEstimate(progress.Import.Estimate)
			}
			phase, percent, message := tinyTilesPublicBuildProgress(progress)
			s.updateTinyTilesBuildProgress(phase, percent, message)
		},
	})
	if err == nil {
		if err = stampTinyTilesWaterwaySidecar(stagedWaterwaysPath, artifact); err != nil {
			err = fmt.Errorf("Wasserwege mit Offline-Karte abgleichen: %w", err)
		}
	}
	if err == nil {
		if err = os.Rename(stagedWaterwaysPath, waterways); err != nil {
			err = fmt.Errorf("Wasserwege veröffentlichen: %w", err)
		}
	}
	if err == nil {
		s.updateTinyTilesBuildProgress("activating", 98, "Offline-Karte wird aktiviert…")
		err = s.installTinyTiles(artifact)
	}
	var territoryLayer string
	var territoryCount int
	if err == nil && status.PostalCodes {
		s.updateTinyTilesBuildProgress("territories", 99, "PLZ-Gebiete werden aus OSM-Grenzen gruppiert…")
		territoryLayer, territoryCount, err = s.publishPostalTerritories(result.PostalCodesPath, status.PostalPrefixLength)
	}

	s.finishTinyTilesBuild(filepath.Base(artifact), territoryLayer, territoryCount, result.GeneratedTiles, result.RoadFeatures, waterwayFeatures, err)
}

// buildPostalTerritory creates an additional PLZ1–PLZ5 layer from the
// already generated postal-boundary sidecar. It intentionally never touches
// the .ttiles artifact, so users can add levels independently and quickly.
func (s *server) buildPostalTerritory(status tinyTilesBuildStatus, postalSidecar string) {
	s.updateTinyTilesBuildProgress("territories", 20, "OSM-PLZ-Grenzen werden geladen…")
	territoryLayer, territoryCount, err := s.publishPostalTerritories(postalSidecar, status.PostalPrefixLength)
	s.finishTinyTilesBuild("", territoryLayer, territoryCount, 0, 0, -1, err)
}

func (s *server) finishTinyTilesBuild(artifact, territoryLayer string, territoryCount, generatedTiles, roadFeatures, waterwayFeatures int, err error) {
	s.tinyTilesMu.Lock()
	defer s.tinyTilesMu.Unlock()
	s.tinyTilesBuild.FinishedAt = time.Now().UTC()
	if err != nil {
		log.Printf("tinyTiles build failed: %v", err)
		s.tinyTilesBuild.State = "failed"
		s.tinyTilesBuild.Phase = "failed"
		s.tinyTilesBuild.Error = "Erzeugung fehlgeschlagen; Details stehen im Server-Log."
		s.tinyTilesBuild.Message = "Offline-Karte konnte nicht erzeugt werden."
		return
	}
	s.tinyTilesBuild.State = "ready"
	s.tinyTilesBuild.Phase = "ready"
	s.tinyTilesBuild.Progress = 100
	s.tinyTilesBuild.TerritoryLayer = territoryLayer
	s.tinyTilesBuild.Territories = territoryCount
	if generatedTiles > 0 {
		s.tinyTilesBuild.GeneratedTiles = generatedTiles
	}
	if roadFeatures > 0 {
		s.tinyTilesBuild.RoadFeatures = roadFeatures
	}
	if waterwayFeatures >= 0 {
		s.tinyTilesBuild.WaterwayFeatures = waterwayFeatures
		if s.tinyTilesWaterways != nil && s.tinyTilesWaterways.buildings != nil {
			s.tinyTilesBuild.BuildingFeatures = len(s.tinyTilesWaterways.buildings.features)
		}
	}
	if territoryLayer != "" {
		s.tinyTilesBuild.Message = fmt.Sprintf("%d %s-Gebiete sind bereit.", territoryCount, territoryLayer)
	} else {
		s.tinyTilesBuild.Message = "Offline-Karte ist bereit."
	}
	if artifact != "" {
		s.tinyTilesBuild.Artifact = artifact
	}
}

// updateTinyTilesBuildEstimate records tinySQL's bounded preflight plan. It
// is safe to expose: the estimate describes aggregate resource use only, not
// local paths or the PBF's contents.
func (s *server) updateTinyTilesBuildEstimate(estimate *tiles.ResourceEstimate) {
	if estimate == nil {
		return
	}
	s.tinyTilesMu.Lock()
	defer s.tinyTilesMu.Unlock()
	if s.tinyTilesBuild.State != "building" {
		return
	}
	s.tinyTilesBuild.EstimatedDiskBytes = estimate.EstimatedDisk
	s.tinyTilesBuild.EstimatedTileCount = estimate.TileCount
}

// updateTinyTilesBuildProgress records only a small, path-free public status.
// Progress is intentionally monotonic for one build: asynchronous clients can
// render it directly without a bar moving backwards when tinyTiles enters a
// short finalization phase after the import has completed.
func (s *server) updateTinyTilesBuildProgress(phase string, progress int, message string) {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	s.tinyTilesMu.Lock()
	defer s.tinyTilesMu.Unlock()
	if s.tinyTilesBuild.State != "building" {
		return
	}
	if progress < s.tinyTilesBuild.Progress {
		progress = s.tinyTilesBuild.Progress
	}
	s.tinyTilesBuild.Phase = phase
	s.tinyTilesBuild.Progress = progress
	s.tinyTilesBuild.Message = message
}

// tinyTilesPublicBuildProgress translates tinyTiles' implementation-level
// callback into a stable, path-free API contract. PBF generation exposes only
// start/end callbacks, while the importer supplies bounded row totals. The
// import range therefore uses 60–95 percent and follows its real row progress.
func tinyTilesPublicBuildProgress(progress tinytiles.PBFBuildProgress) (phase string, percent int, message string) {
	switch strings.ToLower(strings.TrimSpace(progress.Phase)) {
	case "generate":
		return "generating", 5, "Kartendaten werden aus der PBF erzeugt…"
	case "generated":
		return "importing", 55, "Lokale Kacheln werden vorbereitet…"
	case "preflight":
		return "importing", 60, "Lokale Kacheln werden vorbereitet…"
	case "import":
		return "importing", tinyTilesImportPercent(progress), "Lokale Kacheln werden gespeichert…"
	case "published":
		return "finalizing", 97, "Offline-Karte wird überprüft…"
	default:
		// Keep unknown future tinyTiles phases useful without exposing the raw
		// implementation value. The monotonic update method preserves the last
		// known percentage if this callback cannot provide a better estimate.
		return "processing", 0, "Offline-Karte wird verarbeitet…"
	}
}

func tinyTilesImportPercent(progress tinytiles.PBFBuildProgress) int {
	const (
		importStart = 60
		importEnd   = 95
	)
	if progress.Import == nil || progress.Import.TotalRows <= 0 {
		return importStart
	}
	completed := progress.Import.RowsWritten
	if progress.Import.RowsRead > completed {
		completed = progress.Import.RowsRead
	}
	if completed < 0 {
		completed = 0
	}
	if completed > progress.Import.TotalRows {
		completed = progress.Import.TotalRows
	}
	return importStart + int((completed*int64(importEnd-importStart)+progress.Import.TotalRows/2)/progress.Import.TotalRows)
}

// loadTinyTilesIfPresent restores a previously validated offline artifact on
// restart. A corrupt or incomplete path is never served and does not prevent
// the routing service from starting.
func (s *server) loadTinyTilesIfPresent() {
	artifact := filepath.Join(s.tinyTilesDir, "basemap.ttiles")
	if _, err := os.Stat(artifact); errors.Is(err, os.ErrNotExist) {
		return
	} else if err != nil {
		log.Printf("tinyTiles artifact check %q: %v", artifact, err)
		return
	}
	if err := s.installTinyTiles(artifact); err != nil {
		log.Printf("tinyTiles artifact %q not activated: %v", artifact, err)
		return
	}
	minZoom, maxZoom := s.tinyTilesZoomRange()
	waterwayFeatures := s.tinyTilesWaterwayCount()
	waterwaySidecarLoaded := s.tinyTilesHasWaterwaySidecar() && s.tinyTilesHasBuildingSidecar()
	message := "Vorhandene Offline-Karte ist bereit."
	if !waterwaySidecarLoaded {
		message = "Vorhandene Offline-Karte ist bereit. Gewässer und 2,5D-Gebäude werden aus der PBF ergänzt…"
	}
	buildingFeatures := s.tinyTilesBuildingCount()
	s.tinyTilesMu.Lock()
	s.tinyTilesBuild = tinyTilesBuildStatus{
		State:       "ready",
		Phase:       "ready",
		Progress:    100,
		Message:     message,
		Artifact:    filepath.Base(artifact),
		MinZoom:     minZoom,
		MaxZoom:     maxZoom,
		PostalCodes: regularFileExists(filepath.Join(s.tinyTilesDir, "basemap.postcodes.geojson")),
		FinishedAt:  time.Now().UTC(),
		SourceBytes: func() int64 {
			info, err := os.Stat(s.pbfPath)
			if err != nil || info.IsDir() {
				return 0
			}
			return info.Size()
		}(),
		WaterwayFeatures: waterwayFeatures,
		BuildingFeatures: buildingFeatures,
	}
	s.tinyTilesMu.Unlock()
	if !waterwaySidecarLoaded {
		go s.backfillTinyTilesWaterways(artifact)
	}
}

func (s *server) tinyTilesZoomRange() (int, int) {
	minZoom, maxZoom := tinyTilesDefaultMinZoom, tinyTilesDefaultMaxZoom
	s.tinyTilesMu.RLock()
	dataset := s.tinyTilesDataset
	s.tinyTilesMu.RUnlock()
	if dataset == nil {
		return minZoom, maxZoom
	}
	metadata, err := dataset.Metadata()
	if err != nil {
		return minZoom, maxZoom
	}
	if parsed, err := strconv.Atoi(metadata["minzoom"]); err == nil && parsed >= 0 && parsed <= 22 {
		minZoom = parsed
	}
	if parsed, err := strconv.Atoi(metadata["maxzoom"]); err == nil && parsed >= minZoom && parsed <= 22 {
		maxZoom = parsed
	}
	return minZoom, maxZoom
}

func (s *server) installTinyTiles(artifact string) error {
	readers := s.tinyTilesReaders
	if readers == 0 {
		readers = 4
	}
	readerMemory := s.tinyTilesReaderMemory
	if readerMemory == 0 {
		readerMemory = 32 << 20
	}
	dataset, err := tinytiles.Open(context.Background(), artifact, tinytiles.OpenOptions{
		Readers:        readers,
		MaxMemoryBytes: readerMemory,
	})
	if err != nil {
		return fmt.Errorf("öffne erzeugte Offline-Karte: %w", err)
	}
	postalIndex := filepath.Join(s.tinyTilesDir, "basemap.postcodes.geojson")
	if !regularFileExists(postalIndex) {
		postalIndex = ""
	}
	next, err := tinytilesserver.New(tinytilesserver.Config{
		Dataset:           dataset,
		DatasetID:         "osmmini",
		TileCacheBytes:    s.tinyTilesTileCacheBytes,
		PostcodeIndexPath: postalIndex,
		// The handler is mounted below /tinytiles. Advertising that mount keeps
		// TileJSON and the offline sync manifest directly consumable by clients.
		MountPath: "/tinytiles",
	})
	if err != nil {
		_ = dataset.Close()
		return fmt.Errorf("starte Offline-Kartenserver: %w", err)
	}
	waterways, waterwaysErr := loadTinyTilesWaterwaySidecarForArtifact(tinyTilesWaterwaySidecarPath(s.tinyTilesDir), artifact)
	if waterwaysErr != nil && !errors.Is(waterwaysErr, os.ErrNotExist) {
		log.Printf("tinyTiles waterway sidecar is unavailable: %v", waterwaysErr)
		waterways = nil
	}

	s.tinyTilesMu.Lock()
	previousServer := s.tinyTilesServer
	previousDataset := s.tinyTilesDataset
	if previousServer != nil {
		if _, err := previousServer.SwapDataset(dataset); err != nil {
			s.tinyTilesMu.Unlock()
			next.Close()
			_ = dataset.Close()
			return fmt.Errorf("aktualisiere Offline-Karte: %w", err)
		}
		s.tinyTilesDataset = dataset
		s.tinyTilesWaterways = waterways
		// Keep the existing handler so requests in flight continue unchanged.
	} else {
		s.tinyTilesServer = next
		s.tinyTilesDataset = dataset
		s.tinyTilesWaterways = waterways
		s.tinyTilesHandler = next.Handler()
	}
	s.tinyTilesMu.Unlock()

	if previousServer != nil {
		next.Close()
	}
	if previousDataset != nil {
		_ = previousDataset.Close()
	}
	return nil
}

func (s *server) tinyTilesWaterwayCount() int {
	s.tinyTilesMu.RLock()
	defer s.tinyTilesMu.RUnlock()
	return s.tinyTilesWaterways.len()
}

func (s *server) tinyTilesHasWaterwaySidecar() bool {
	s.tinyTilesMu.RLock()
	defer s.tinyTilesMu.RUnlock()
	return s.tinyTilesWaterways != nil
}

// backfillTinyTilesWaterways migrates pre-waterway offline artifacts without
// forcing the user to regenerate every base tile. It runs only after the
// artifact has already been activated, never blocks startup, and gives up
// safely when the source PBF has changed since that artifact was built.
func (s *server) backfillTinyTilesWaterways(artifact string) {
	s.tinyTilesWaterwayBuildMu.Lock()
	defer s.tinyTilesWaterwayBuildMu.Unlock()

	sidecar := tinyTilesWaterwaySidecarPath(s.tinyTilesDir)
	if index, err := loadTinyTilesWaterwaySidecarForArtifact(sidecar, artifact); err == nil && index.buildings != nil {
		return
	}
	pbfInfo, err := os.Stat(s.pbfPath)
	if err != nil || pbfInfo.IsDir() {
		s.noteTinyTilesWaterwayBackfillFailure(fmt.Errorf("PBF ist nicht verfügbar"))
		return
	}
	artifactManifest, err := tinyTilesArtifactManifestPath(artifact)
	if err != nil {
		s.noteTinyTilesWaterwayBackfillFailure(fmt.Errorf("Offline-Karte ist nicht verfügbar"))
		return
	}
	artifactInfo, err := os.Stat(artifactManifest)
	if err != nil {
		s.noteTinyTilesWaterwayBackfillFailure(fmt.Errorf("Offline-Karte ist nicht verfügbar"))
		return
	}
	if pbfInfo.ModTime().After(artifactInfo.ModTime()) {
		s.noteTinyTilesWaterwayBackfillFailure(fmt.Errorf("PBF wurde nach der Offline-Karte geändert"))
		return
	}

	staged, err := os.CreateTemp(s.tinyTilesDir, ".basemap.waterways-backfill-*")
	if err != nil {
		s.noteTinyTilesWaterwayBackfillFailure(fmt.Errorf("Wasserweg-Zwischendatei: %w", err))
		return
	}
	stagedPath := staged.Name()
	if err := staged.Close(); err != nil {
		_ = os.Remove(stagedPath)
		s.noteTinyTilesWaterwayBackfillFailure(fmt.Errorf("Wasserweg-Zwischendatei schließen: %w", err))
		return
	}
	defer os.Remove(stagedPath)

	count, err := buildTinyTilesWaterwaySidecar(s.pbfPath, stagedPath)
	if err != nil {
		s.noteTinyTilesWaterwayBackfillFailure(err)
		return
	}
	// An explicit full build that started while this background migration was
	// scanning will publish its own matching pair. Do not race it with an
	// otherwise valid sidecar for the old artifact.
	s.tinyTilesMu.RLock()
	building := s.tinyTilesBuild.State == "building"
	s.tinyTilesMu.RUnlock()
	if building {
		return
	}
	if err := stampTinyTilesWaterwaySidecar(stagedPath, artifact); err != nil {
		s.noteTinyTilesWaterwayBackfillFailure(err)
		return
	}
	if err := os.Rename(stagedPath, sidecar); err != nil {
		s.noteTinyTilesWaterwayBackfillFailure(fmt.Errorf("Gewässerlayer veröffentlichen: %w", err))
		return
	}
	index, err := loadTinyTilesWaterwaySidecarForArtifact(sidecar, artifact)
	if err != nil {
		s.noteTinyTilesWaterwayBackfillFailure(err)
		return
	}

	s.tinyTilesMu.Lock()
	if s.tinyTilesBuild.State != "building" && s.tinyTilesDataset != nil {
		s.tinyTilesWaterways = index
		if s.tinyTilesBuild.State == "ready" {
			s.tinyTilesBuild.WaterwayFeatures = count
			s.tinyTilesBuild.BuildingFeatures = len(index.buildings.features)
			s.tinyTilesBuild.Message = "Vorhandene Offline-Karte ist bereit; Gewässer und 2,5D-Gebäude wurden ergänzt."
		}
	}
	s.tinyTilesMu.Unlock()
}

func (s *server) noteTinyTilesWaterwayBackfillFailure(err error) {
	log.Printf("tinyTiles waterway migration failed: %v", err)
	s.tinyTilesMu.Lock()
	defer s.tinyTilesMu.Unlock()
	if s.tinyTilesBuild.State == "ready" {
		s.tinyTilesBuild.Message = "Offline-Karte ist bereit; Gewässer und 2,5D-Gebäude konnten nicht ergänzt werden. Bitte Offline-Karte neu erzeugen."
	}
}

func (s *server) serveTinyTiles(w http.ResponseWriter, r *http.Request) {
	s.tinyTilesMu.RLock()
	handler := s.tinyTilesHandler
	s.tinyTilesMu.RUnlock()
	if handler == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "keine Offline-Karte erzeugt")
		return
	}
	// http.ServeMux redirects when a mounted handler receives the outer prefix
	// in RawPath. Rewrite a request copy explicitly so tinyTiles sees precisely
	// its documented routes (/tiles, /tilejson.json, /metadata, /sync).
	request := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = strings.TrimPrefix(r.URL.Path, "/tinytiles")
	if urlCopy.Path == "" {
		urlCopy.Path = "/"
	}
	urlCopy.RawPath = ""
	request.URL = &urlCopy
	handler.ServeHTTP(w, request)
}

func (s *server) closeTinyTiles() {
	s.tinyTilesMu.Lock()
	server := s.tinyTilesServer
	dataset := s.tinyTilesDataset
	s.tinyTilesServer = nil
	s.tinyTilesDataset = nil
	s.tinyTilesHandler = nil
	s.tinyTilesWaterways = nil
	s.tinyTilesMu.Unlock()
	if server != nil {
		server.Close()
	}
	if dataset != nil {
		_ = dataset.Close()
	}
}

// prefetchTinyTilesRoute uses tinyTiles v2.3's bounded predictive cache after
// osmmini has already computed a trusted route. It never becomes a public HTTP
// endpoint, so an unauthenticated caller cannot turn it into a disk/RAM work
// amplifier. A missing offline map or a deliberately disabled cache is simply
// a no-op for the routing API.
func (s *server) prefetchTinyTilesRoute(path []osmmini.Coord) {
	if len(path) == 0 {
		return
	}
	points := make([]tinytilesserver.RoutePoint, len(path))
	for i, point := range path {
		points[i] = tinytilesserver.RoutePoint{Latitude: point.Lat, Longitude: point.Lon}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		s.tinyTilesMu.RLock()
		server := s.tinyTilesServer
		dataset := s.tinyTilesDataset
		if server == nil || dataset == nil {
			s.tinyTilesMu.RUnlock()
			return
		}
		maxZoom := tinyTilesDefaultMaxZoom
		if metadata, err := dataset.Metadata(); err == nil {
			if parsed, err := strconv.Atoi(metadata["maxzoom"]); err == nil && parsed >= 0 && parsed < maxZoom {
				maxZoom = parsed
			}
		}
		_, err := server.PrefetchRoute(ctx, points, tinytilesserver.RoutePrefetchOptions{
			Zoom:     maxZoom,
			Radius:   tinyTilesRoutePrefetchRadius,
			MaxTiles: tinyTilesRoutePrefetchMaxTiles,
		})
		s.tinyTilesMu.RUnlock()
		if err != nil && !errors.Is(err, tinytilesserver.ErrPredictiveCachingDisabled) {
			log.Printf("tinyTiles route prefetch: %v", err)
		}
	}()
}
