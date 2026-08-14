package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"unicode"

	osmmini "simonwaldherr.de/go/osmmini"
)

// FireVehicle is one vehicle stationed at a fire station, keyed by its
// German BOS radio call sign (Funkrufname, e.g. "FL Musterstadt 40/1").
// Meta carries free-form extra fields from a CSV import (equipment counts,
// tank capacity, etc.) that don't warrant a dedicated column.
type FireVehicle struct {
	Callsign string            `json:"callsign"`
	Type     string            `json:"type,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

// fireStationRecord is the persisted (locally stored, never committed) part
// of a fire station: either an enrichment of an OSM-derived station (ID
// prefixed "osm:", Lat/Lon ignored in favor of the live OSM geometry, but
// Name may be set to override/supply a display name — some real stations
// have an amenity=fire_station way in OSM with no "name" tag, which would
// otherwise never be shown or matched by CSV import) or a fully standalone
// manual entry (ID prefixed "manual:", own Name/Lat/Lon).
type fireStationRecord struct {
	ID       string        `json:"id"`
	Name     string        `json:"name,omitempty"`
	Lat      float64       `json:"lat,omitempty"`
	Lon      float64       `json:"lon,omitempty"`
	Vehicles []FireVehicle `json:"vehicles,omitempty"`
}

// apiFireStation is what the frontend receives: an OSM- or manually-sourced
// station merged with any stored vehicle roster.
type apiFireStation struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Lat      float64       `json:"lat"`
	Lon      float64       `json:"lon"`
	Source   string        `json:"source"` // "osm" | "manual"
	Vehicles []FireVehicle `json:"vehicles,omitempty"`
}

// FireStationStore persists manual entries and OSM-station vehicle
// enrichment to a local JSON file (gitignored — this is the user's own,
// often internal, roster data, never meant for the repository).
type FireStationStore struct {
	mu   sync.RWMutex
	path string
	v    map[string]fireStationRecord
	seq  int
}

func NewFireStationStore(path string) *FireStationStore {
	return &FireStationStore{path: path, v: map[string]fireStationRecord{}}
}

func (s *FireStationStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var v map[string]fireStationRecord
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	s.v = v
	return nil
}

func (s *FireStationStore) saveLocked() error {
	tmp := s.path + ".tmp"
	b, err := json.MarshalIndent(s.v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *FireStationStore) get(id string) (fireStationRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.v[id]
	return r, ok
}

func (s *FireStationStore) all() map[string]fireStationRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]fireStationRecord, len(s.v))
	for k, v := range s.v {
		out[k] = v
	}
	return out
}

func (s *FireStationStore) put(rec fireStationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v[rec.ID] = rec
	return s.saveLocked()
}

func (s *FireStationStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.v[id]; !ok {
		return nil
	}
	delete(s.v, id)
	return s.saveLocked()
}

func (s *FireStationStore) nextManualID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("manual:%d", s.seq)
}

// osmFireStation is one amenity=fire_station node/way from the loaded PBF,
// resolved to a point (way centroid, mirroring how other POI code in this
// file computes one from constituent node coordinates).
type osmFireStation struct {
	id   string
	name string
	lat  float64
	lon  float64
}

// osmFireStations scans the in-memory POI index for amenity=fire_station
// nodes and ways. window is optional (nil scans the whole loaded region,
// used for CSV name-matching); when set, only stations inside it are kept
// (used by the map overlay's bbox query).
func (s *server) osmFireStations(window *osmmini.CoordWindow) []osmFireStation {
	var out []osmFireStation
	s.poiMu.RLock()
	defer s.poiMu.RUnlock()
	for _, node := range s.poiTaggedNodes {
		if node.Tags["amenity"] != "fire_station" {
			continue
		}
		if window != nil && !window.Contains(osmmini.Coord{Lat: node.Lat, Lon: node.Lon}) {
			continue
		}
		out = append(out, osmFireStation{
			id:   fmt.Sprintf("osm:node/%d", node.ID),
			name: strings.TrimSpace(node.Tags["name"]),
			lat:  node.Lat,
			lon:  node.Lon,
		})
	}
	for _, way := range s.poiWays {
		if way.Tags["amenity"] != "fire_station" {
			continue
		}
		var cx, cy float64
		var cnt int
		for _, nid := range way.NodeIDs {
			if c, ok := s.poiNodes[nid]; ok {
				cx += c.Lat
				cy += c.Lon
				cnt++
			}
		}
		if cnt == 0 {
			continue
		}
		coord := osmmini.Coord{Lat: cx / float64(cnt), Lon: cy / float64(cnt)}
		if window != nil && !window.Contains(coord) {
			continue
		}
		out = append(out, osmFireStation{
			id:   fmt.Sprintf("osm:way/%d", way.ID),
			name: strings.TrimSpace(way.Tags["name"]),
			lat:  coord.Lat,
			lon:  coord.Lon,
		})
	}
	return out
}

func (s *server) handleFireStations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleFireStationsList(w, r)
	case http.MethodPost:
		if !s.requireSettingsAdmin(w, r) {
			return
		}
		s.handleFireStationsCreate(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) handleFireStationsList(w http.ResponseWriter, r *http.Request) {
	window, ok := offlineLabelsWindow(r.URL.Query().Get("bbox"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "bbox must be minLon,minLat,maxLon,maxLat")
		return
	}
	stored := s.fireStations.all()
	out := make([]apiFireStation, 0, 16)
	for _, osmStation := range s.osmFireStations(&window) {
		st := apiFireStation{ID: osmStation.id, Name: osmStation.name, Lat: osmStation.lat, Lon: osmStation.lon, Source: "osm"}
		if rec, ok := stored[osmStation.id]; ok {
			st.Vehicles = rec.Vehicles
			if rec.Name != "" {
				st.Name = rec.Name
			}
		}
		out = append(out, st)
	}
	for id, rec := range stored {
		if !strings.HasPrefix(id, "manual:") {
			continue
		}
		if !window.Contains(osmmini.Coord{Lat: rec.Lat, Lon: rec.Lon}) {
			continue
		}
		out = append(out, apiFireStation{ID: rec.ID, Name: rec.Name, Lat: rec.Lat, Lon: rec.Lon, Source: "manual", Vehicles: rec.Vehicles})
	}
	writeJSON(w, http.StatusOK, map[string]any{"stations": out})
}

type fireStationCreateRequest struct {
	Name     string        `json:"name"`
	Lat      float64       `json:"lat"`
	Lon      float64       `json:"lon"`
	Vehicles []FireVehicle `json:"vehicles,omitempty"`
}

func (s *server) handleFireStationsCreate(w http.ResponseWriter, r *http.Request) {
	var req fireStationCreateRequest
	if err := readJSON(w, r, &req, 1<<20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Lat < -90 || req.Lat > 90 || req.Lon < -180 || req.Lon > 180 {
		writeJSONError(w, http.StatusBadRequest, "lat/lon out of range")
		return
	}
	id := s.fireStations.nextManualID()
	rec := fireStationRecord{ID: id, Name: req.Name, Lat: req.Lat, Lon: req.Lon, Vehicles: req.Vehicles}
	if err := s.fireStations.put(rec); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apiFireStation{ID: id, Name: rec.Name, Lat: rec.Lat, Lon: rec.Lon, Source: "manual", Vehicles: rec.Vehicles})
}

// handleFireStationByID handles PUT (upsert vehicles/name/coords for a
// specific station, "osm:..." or "manual:...") and DELETE.
func (s *server) handleFireStationByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettingsAdmin(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/fire-stations/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing station id")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req fireStationCreateRequest
		if err := readJSON(w, r, &req, 1<<20); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		// Start from the existing stored record and only overlay fields the
		// caller actually sent — e.g. the "add vehicle" popup action only
		// sends Vehicles, and previously blew away the station's own Name/
		// Lat/Lon (both zeroed to their Go zero value) every time it fired.
		rec, _ := s.fireStations.get(id)
		rec.ID = id
		rec.Vehicles = req.Vehicles
		if name := strings.TrimSpace(req.Name); name != "" {
			rec.Name = name
		}
		// For "osm:" ids, Name is only a display-name override (e.g. the OSM
		// way has no "name" tag) — geometry always stays the live OSM coord.
		if strings.HasPrefix(id, "manual:") && (req.Lat != 0 || req.Lon != 0) {
			rec.Lat, rec.Lon = req.Lat, req.Lon
		}
		if err := s.fireStations.put(rec); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "save failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.fireStations.delete(id); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// stationNameTokens splits a raw "Dienststelle" string into fields and drops
// administrative-code-like tokens (e.g. "2.1.3"), shared by
// normalizeStationName (which also drops org-type words, for equality
// matching) and stationPlaceQuery (which keeps them, for a search query).
func stationNameTokens(name string) []string {
	fields := strings.Fields(strings.TrimSpace(name))
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		isCodeLike := true
		for _, r := range f {
			if unicode.IsLetter(r) && r != '.' {
				isCodeLike = false
				break
			}
		}
		if isCodeLike && strings.ContainsAny(f, "0123456789.") {
			continue // administrative prefix like "2.1.3"
		}
		kept = append(kept, f)
	}
	return kept
}

// isStationOrgWord reports whether a (lowercased) token is a fire/rescue
// organization-type word rather than part of a place name — "FF"
// (Freiwillige Feuerwehr), "WF"/"BF" (Werk-/Berufsfeuerwehr), etc.
func isStationOrgWord(lower string) bool {
	switch lower {
	case "ff", "wf", "bf", "feuerwehr", "freiwillige":
		return true
	}
	return false
}

// normalizeStationName strips common German fire-department station-name
// noise (administrative codes, "FF"/"Feuerwehr" prefixes, diacritics case)
// so a CSV's "2.1.3 DGF FF Musterstadt" can match an OSM name like
// "Feuerwehr Musterstadt" or plain "Musterstadt".
func normalizeStationName(name string) string {
	tokens := stationNameTokens(strings.ToLower(name))
	kept := make([]string, 0, len(tokens))
	for _, f := range tokens {
		if isStationOrgWord(f) {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// stationPlaceQuery extracts just the place-name part of a "Dienststelle"
// string (codes and org-type words removed, original casing kept) to use as
// a search query — e.g. "2.1.3 DGF FF Adldorf" -> "Adldorf". Searching the
// raw string directly performs badly: noise tokens like "DGF" derail the
// fuzzy address search toward unrelated results (observed with real data).
func stationPlaceQuery(name string) string {
	tokens := stationNameTokens(name)
	kept := make([]string, 0, len(tokens))
	for _, f := range tokens {
		if isStationOrgWord(strings.ToLower(f)) {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

type fireStationImportResult struct {
	Matched   []string                 `json:"matched"`           // station names matched to an OSM station by name
	Guessed   []string                 `json:"guessed,omitempty"` // matched to a nearby *unnamed* OSM fire_station by geocoding the place name — worth spot-checking
	Unmatched []unmatchedImportStation `json:"unmatched"`         // no OSM fire_station found nearby; a geocode hint is included when available so it can be placed with one click instead of a manual map search
}

type unmatchedImportStation struct {
	Name      string  `json:"name"`
	HintLat   float64 `json:"hintLat,omitempty"`
	HintLon   float64 `json:"hintLon,omitempty"`
	HintLabel string  `json:"hintLabel,omitempty"`
}

// fireStationGeocodeMatchRadiusMeters bounds how far a geocoded place-name
// hit may be from an *unnamed* OSM fire_station feature for the import to
// treat them as the same station. German village fire stations sit inside
// the village itself, so this stays tight on purpose — the cost of a false
// match (attaching a real vehicle roster to the wrong building) is worse
// than leaving a row unmatched for a human to place.
const fireStationGeocodeMatchRadiusMeters = 1500.0

// geocodeStationHint turns a raw "Dienststelle" string into a best-effort
// coordinate guess via the existing address/POI search, used only as a
// fallback when direct name matching fails. It is never authoritative: it
// either narrows down which *unnamed* OSM fire_station is the real match
// (see handleFireStationsImport), or is surfaced as a one-click placement
// suggestion for a station with no OSM equivalent at all.
func (s *server) geocodeStationHint(rawName string) (lat, lon float64, label string, ok bool) {
	query := stationPlaceQuery(rawName)
	if query == "" {
		return 0, 0, "", false
	}
	results := s.searchLocationResults(query, 5)
	for _, r := range results {
		if r.Tags["amenity"] == "fire_station" || strings.Contains(strings.ToLower(r.Label), "feuerwehr") {
			return r.Lat, r.Lon, r.Label, true
		}
	}
	// No direct fire-station-looking hit — settle for the place (village/
	// town/hamlet) centroid if there is one. Deliberately does NOT fall
	// back further to "just the top search result": an unrelated shop or
	// address a search away from the real place is worse than no hint at
	// all, since a human may act on it without checking closely.
	for _, r := range results {
		if r.Tags["place"] != "" {
			return r.Lat, r.Lon, r.Label, true
		}
	}
	return 0, 0, "", false
}

// handleFireStationsImport parses a CSV with columns Dienststelle,Funkrufname
// plus optional extra columns (kept as vehicle Meta), matching each row's
// station name against the whole loaded region's OSM fire stations by a
// normalized-name comparison. Rows for stations with no OSM match are
// reported back, not guessed at — the operator places those manually.
func (s *server) handleFireStationsImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireSettingsAdmin(w, r) {
		return
	}
	body := http.MaxBytesReader(w, r.Body, 4<<20)
	reader := csv.NewReader(body)
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid csv: "+err.Error())
		return
	}
	if len(records) < 2 {
		writeJSONError(w, http.StatusBadRequest, "csv needs a header row and at least one data row")
		return
	}
	header := records[0]
	colIdx := map[string]int{}
	for i, h := range header {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	stationCol, ok := colIdx["dienststelle"]
	if !ok {
		stationCol, ok = colIdx["station"]
	}
	callsignCol, hasCallsign := colIdx["funkrufname"]
	if !hasCallsign {
		callsignCol, hasCallsign = colIdx["callsign"]
	}
	typeCol, hasType := colIdx["fahrzeugtyp"]
	if !hasType {
		typeCol, hasType = colIdx["type"]
	}
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "csv needs a 'Dienststelle' (station) column")
		return
	}

	osmStations := s.osmFireStations(nil)
	storedForMatch := s.fireStations.all()
	byNormName := make(map[string]osmFireStation, len(osmStations))
	var unnamed []osmFireStation // amenity=fire_station features with no name (OSM or override) — geocode-fallback candidates
	for _, st := range osmStations {
		name := st.name
		// A stored display-name override (set via the popup's "Umbenennen"
		// action) takes precedence — this is what makes an OSM way with no
		// "name" tag matchable at all.
		if rec, ok := storedForMatch[st.id]; ok && rec.Name != "" {
			name = rec.Name
		}
		if name == "" {
			unnamed = append(unnamed, st)
			continue
		}
		byNormName[normalizeStationName(name)] = st
	}

	// station id -> vehicles being built up across this import's rows
	pending := map[string][]FireVehicle{}
	result := fireStationImportResult{}
	matchedNames := map[string]bool{}
	guessedNames := map[string]bool{}
	unmatchedNames := map[string]bool{}

	// A geocode lookup costs a full fuzzy address/POI search (seconds, not
	// milliseconds) and the same station name repeats across several vehicle
	// rows in a typical roster CSV, so memoize per rawName for this import —
	// without it, a CSV with many rows per station made this handler take
	// well over a minute in testing.
	type geocodeHit struct {
		lat, lon float64
		label    string
		ok       bool
	}
	geocodeCache := map[string]geocodeHit{}
	geocode := func(name string) (float64, float64, string, bool) {
		if hit, cached := geocodeCache[name]; cached {
			return hit.lat, hit.lon, hit.label, hit.ok
		}
		lat, lon, label, ok := s.geocodeStationHint(name)
		geocodeCache[name] = geocodeHit{lat, lon, label, ok}
		return lat, lon, label, ok
	}

	for _, row := range records[1:] {
		if stationCol >= len(row) {
			continue
		}
		rawName := strings.TrimSpace(row[stationCol])
		if rawName == "" {
			continue
		}
		norm := normalizeStationName(rawName)
		match, found := byNormName[norm]
		if !found {
			// tolerate the OSM name being a substring of (or containing) the CSV name
			for k, st := range byNormName {
				if k != "" && (strings.Contains(norm, k) || strings.Contains(k, norm)) {
					match, found = st, true
					break
				}
			}
		}
		guessed := false
		if !found && len(unnamed) > 0 {
			// Last resort: geocode the place name and, if an *unnamed*
			// fire_station feature sits close by, adopt it as a real match —
			// this is what actually resolves stations like a real-world
			// "Wallersdorf" whose OSM way carries no name tag at all.
			if hintLat, hintLon, _, ok := geocode(rawName); ok {
				bestIdx, bestDist := -1, fireStationGeocodeMatchRadiusMeters
				for i, st := range unnamed {
					if d := haversineMeters(hintLat, hintLon, st.lat, st.lon); d <= bestDist {
						bestDist, bestIdx = d, i
					}
				}
				if bestIdx >= 0 {
					st := unnamed[bestIdx]
					display := stationPlaceQuery(rawName)
					if display == "" {
						display = rawName
					}
					rec, _ := s.fireStations.get(st.id)
					rec.ID = st.id
					rec.Name = display
					if err := s.fireStations.put(rec); err == nil {
						match, found, guessed = st, true, true
						byNormName[normalizeStationName(display)] = st
						unnamed = append(unnamed[:bestIdx], unnamed[bestIdx+1:]...)
					}
				}
			}
		}
		if !found {
			if !unmatchedNames[rawName] {
				unmatchedNames[rawName] = true
				entry := unmatchedImportStation{Name: rawName}
				if hintLat, hintLon, hintLabel, ok := geocode(rawName); ok {
					entry.HintLat, entry.HintLon, entry.HintLabel = hintLat, hintLon, hintLabel
				}
				result.Unmatched = append(result.Unmatched, entry)
			}
			continue
		}
		if guessed {
			if !guessedNames[rawName] {
				guessedNames[rawName] = true
				result.Guessed = append(result.Guessed, rawName)
			}
		} else if !matchedNames[rawName] {
			matchedNames[rawName] = true
			result.Matched = append(result.Matched, rawName)
		}
		vehicle := FireVehicle{}
		if hasCallsign && callsignCol < len(row) {
			vehicle.Callsign = strings.TrimSpace(row[callsignCol])
		}
		if hasType && typeCol < len(row) {
			vehicle.Type = strings.TrimSpace(row[typeCol])
		}
		for name, idx := range colIdx {
			if idx == stationCol || (hasCallsign && idx == callsignCol) || (hasType && idx == typeCol) || idx >= len(row) {
				continue
			}
			val := strings.TrimSpace(row[idx])
			if val == "" {
				continue
			}
			if vehicle.Meta == nil {
				vehicle.Meta = map[string]string{}
			}
			vehicle.Meta[name] = val
		}
		if vehicle.Callsign == "" {
			continue
		}
		pending[match.id] = append(pending[match.id], vehicle)
	}

	for id, vehicles := range pending {
		existing, _ := s.fireStations.get(id)
		existing.ID = id
		existing.Vehicles = mergeFireVehicles(existing.Vehicles, vehicles)
		if err := s.fireStations.put(existing); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "save failed: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// mergeFireVehicles replaces vehicles with the same callsign and appends new
// ones, so re-importing an updated CSV refreshes existing entries instead of
// duplicating them.
func mergeFireVehicles(existing []FireVehicle, incoming []FireVehicle) []FireVehicle {
	byCallsign := make(map[string]int, len(existing))
	out := make([]FireVehicle, len(existing))
	copy(out, existing)
	for i, v := range out {
		byCallsign[v.Callsign] = i
	}
	for _, v := range incoming {
		if idx, ok := byCallsign[v.Callsign]; ok {
			out[idx] = v
		} else {
			byCallsign[v.Callsign] = len(out)
			out = append(out, v)
		}
	}
	return out
}
