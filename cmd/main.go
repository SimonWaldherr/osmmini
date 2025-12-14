package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	osmmini "simonwaldherr.de/go/osmmini"
)

// Offline routing between two addresses using the local PBF and the osmmini package.
func main() {
	pbf := flag.String("pbf", "region.osm.pbf", "Pfad zur OSM-PBF-Datei")
	from := flag.String("from", "Karl-Schmid-Straße 2, 94522 Wallersdorf", "Startadresse")
	to := flag.String("to", "Marktpl. 19, 94522 Wallersdorf", "Zieladresse")
	serve := flag.Bool("serve", false, "Weboberfläche mit HTTP-API starten")
	listen := flag.String("listen", ":8080", "HTTP Listen-Address")
	flag.Parse()

	log.Printf("Lade PBF %s und baue Graph...", *pbf)
	r, addrs, err := osmmini.BuildRouterWithAddresses(*pbf)
	if err != nil {
		log.Fatalf("Graph-Aufbau fehlgeschlagen: %v", err)
	}
	log.Printf("Graph fertig: %d Knoten, %d Adressen", r.NodeCount(), len(addrs))

	if *serve {
		startServer(r, addrs, *listen, *from, *to)
		return
	}

	res, err := planRoute(r, addrs, *from, *to)
	if err != nil {
		log.Fatalf("Routing fehlgeschlagen: %v", err)
	}

	fmt.Printf("From: %s [%s] (%.6f, %.6f) -> graph node %d (snap %.1fm)\n",
		res.FromInput, res.FromLabel, res.FromCoord.Lat, res.FromCoord.Lon, res.FromNode, res.FromSnapM)
	fmt.Printf("To:   %s [%s] (%.6f, %.6f) -> graph node %d (snap %.1fm)\n",
		res.ToInput, res.ToLabel, res.ToCoord.Lat, res.ToCoord.Lon, res.ToNode, res.ToSnapM)
	fmt.Printf("Route: %.1f km (%d Knoten im Pfad)\n", res.DistanceM/1000, len(res.Path))
}

type routeResult struct {
	FromInput string
	ToInput   string

	FromLabel string
	ToLabel   string

	FromCoord osmmini.Coord
	ToCoord   osmmini.Coord

	FromNode int64
	ToNode   int64

	FromSnapM float64
	ToSnapM   float64

	Path      []int64
	PathCoord []osmmini.Coord
	DistanceM float64
}

func planRoute(r *osmmini.Router, addrs []osmmini.AddressEntry, fromStr, toStr string) (routeResult, error) {
	fromQuery := osmmini.ParseAddressGuess(fromStr)
	toQuery := osmmini.ParseAddressGuess(toStr)

	fromCoord, fromLabel, err := resolveCoord(r, addrs, fromQuery, fromStr)
	if err != nil {
		return routeResult{}, err
	}
	toCoord, toLabel, err := resolveCoord(r, addrs, toQuery, toStr)
	if err != nil {
		return routeResult{}, err
	}

	startID, startDist, ok := r.NearestNode(fromCoord.Lat, fromCoord.Lon)
	if !ok {
		return routeResult{}, fmt.Errorf("kein Startknoten im Graph gefunden")
	}
	endID, endDist, ok := r.NearestNode(toCoord.Lat, toCoord.Lon)
	if !ok {
		return routeResult{}, fmt.Errorf("kein Zielknoten im Graph gefunden")
	}

	path, meters, err := r.Route(startID, endID)
	if err != nil {
		return routeResult{}, err
	}

	return routeResult{
		FromInput: fromStr,
		ToInput:   toStr,
		FromLabel: fromLabel,
		ToLabel:   toLabel,
		FromCoord: fromCoord,
		ToCoord:   toCoord,
		FromNode:  startID,
		ToNode:    endID,
		FromSnapM: startDist,
		ToSnapM:   endDist,
		Path:      path,
		PathCoord: r.CoordsForPath(path),
		DistanceM: meters,
	}, nil
}

func resolveCoord(r *osmmini.Router, addrs []osmmini.AddressEntry, q osmmini.AddressQuery, raw string) (osmmini.Coord, string, error) {
	addr, ok := osmmini.FindBestAddress(addrs, q)
	if ok {
		return addr.Coord, fmt.Sprintf("Adresse %s", addr.Tags["addr:street"]), nil
	}
	if nodeID, okStreet := r.StreetNode(q.Street); okStreet {
		c, _ := r.Coord(nodeID)
		return c, fmt.Sprintf("Straßensnapshot %s", q.Street), nil
	}
	return osmmini.Coord{}, "", fmt.Errorf("Adresse nicht gefunden: %s", raw)
}

func startServer(r *osmmini.Router, addrs []osmmini.AddressEntry, listen, defaultFrom, defaultTo string) {
	// search endpoint (registered once)
	http.HandleFunc("/search", func(w http.ResponseWriter, req *http.Request) {
		q := strings.TrimSpace(req.URL.Query().Get("q"))
		if q == "" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]interface{}{})
			return
		}
		limit := 10
		if l := req.URL.Query().Get("limit"); l != "" {
			// ignore parse errors and keep default
		}
		aq := osmmini.ParseAddressGuess(q)
		matches := osmmini.SearchAddresses(addrs, aq, limit)
		out := make([]map[string]interface{}, 0, len(matches))
		for _, m := range matches {
			out = append(out, map[string]interface{}{
				"id": m.ID,
				"label": func() string {
					if s := m.Tags["addr:street"]; s != "" {
						return s
					}
					return m.Tags["name"]
				}(),
				"lat":  m.Coord.Lat,
				"lon":  m.Coord.Lon,
				"tags": m.Tags,
			})
		}
		// If no address matches found, also try matching street names from the graph
		if len(out) == 0 {
			streetMatches := r.SearchStreets(q, limit)
			for _, s := range streetMatches {
				out = append(out, map[string]interface{}{
					"id": s.NodeID,
					"label": func() string {
						// display a human-friendly name by un-normalizing if possible
						return s.Name
					}(),
					"lat":  s.Coord.Lat,
					"lon":  s.Coord.Lon,
					"tags": map[string]string{"street": s.Name},
				})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	http.HandleFunc("/route", func(w http.ResponseWriter, req *http.Request) {
		from := strings.TrimSpace(req.URL.Query().Get("from"))
		to := strings.TrimSpace(req.URL.Query().Get("to"))
		if from == "" {
			from = defaultFrom
		}
		if to == "" {
			to = defaultTo
		}

		res, err := planRoute(r, addrs, from, to)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Build path with lowercase keys for JS convenience
		if len(res.PathCoord) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "empty path"})
			return
		}
		path := make([]map[string]float64, 0, len(res.PathCoord))
		for _, c := range res.PathCoord {
			path = append(path, map[string]float64{"lat": c.Lat, "lon": c.Lon})
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"from": map[string]interface{}{
				"input":  res.FromInput,
				"label":  res.FromLabel,
				"lat":    res.FromCoord.Lat,
				"lon":    res.FromCoord.Lon,
				"node":   res.FromNode,
				"snap_m": res.FromSnapM,
			},
			"to": map[string]interface{}{
				"input":  res.ToInput,
				"label":  res.ToLabel,
				"lat":    res.ToCoord.Lat,
				"lon":    res.ToCoord.Lon,
				"node":   res.ToNode,
				"snap_m": res.ToSnapM,
			},
			"distance_m": res.DistanceM,
			"path":       path,
		})
	})

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(indexHTML))
	})

	log.Printf("Webserver läuft auf %s (GET / für UI, GET /route?from=...&to=... für API)", listen)
	log.Fatal(http.ListenAndServe(listen, nil))
}

const indexHTML = `<!doctype html>
<html lang="de">
<head>
  <meta charset="utf-8" />
  <title>OSM Offline Routing</title>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
	<link rel="stylesheet" href="https://unpkg.com/leaflet@1.9.4/dist/leaflet.css" />
  <style>
    body { margin: 0; font-family: system-ui, sans-serif; background: #0b1021; color: #e8ecf8; }
    header { padding: 12px 16px; background: linear-gradient(120deg, #122347, #0b1021); box-shadow: 0 2px 10px rgba(0,0,0,0.35); position: sticky; top: 0; z-index: 10; }
    h1 { margin: 0; font-size: 18px; letter-spacing: 0.3px; }
    form { display: grid; grid-template-columns: 1fr 1fr auto; gap: 8px; margin-top: 10px; }
    input { padding: 10px; border-radius: 8px; border: 1px solid #2c3a5d; background: #0f1934; color: #e8ecf8; }
    button { padding: 10px 16px; border-radius: 8px; border: none; background: #4da3ff; color: #0b1021; font-weight: 700; cursor: pointer; }
    button:hover { background: #76b8ff; }
    #map { height: calc(100vh - 130px); width: 100%; }
    #info { padding: 10px 16px; background: #0f1934; border-top: 1px solid #1f2c4b; display: flex; gap: 16px; align-items: center; flex-wrap: wrap; }
    .pill { padding: 6px 10px; border-radius: 999px; background: #152650; border: 1px solid #27407a; }
    a { color: #9cd0ff; }
		.suggest { position: absolute; left: 0; right: 0; top: 44px; background: #071029; border: 1px solid #1f2c4b; z-index: 20; max-height: 220px; overflow: auto; display: none; }
		.suggest .item { padding: 8px 10px; cursor: pointer; border-bottom: 1px solid rgba(255,255,255,0.02); }
		.suggest .item:hover { background: rgba(77,163,255,0.08); }
  </style>
</head>
<body>
  <header>
    <h1>Offline OSM Routing</h1>
		<form id="route-form">
			<div style="position:relative">
				<input id="from" name="from" placeholder="Von" value="Klöpfstraße 2 94522 Wallersdorf" autocomplete="off" />
				<div id="from-suggest" class="suggest"></div>
			</div>
			<div style="position:relative">
				<input id="to" name="to" placeholder="Nach" value="Kaplan-Strohmeier-Straße Leiblfing" autocomplete="off" />
				<div id="to-suggest" class="suggest"></div>
			</div>
			<button type="submit">Route berechnen</button>
		</form>
  </header>
  <div id="map"></div>
  <div id="info">
    <div class="pill" id="status">Bereit</div>
    <div class="pill" id="distance"></div>
  </div>

	<script src="https://unpkg.com/leaflet@1.9.4/dist/leaflet.js"></script>
  <script>
    const map = L.map('map').setView([48.7, 12.7], 10);
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 19,
      attribution: '&copy; OpenStreetMap contributors'
    }).addTo(map);

    let line, startM, endM;

    async function fetchRoute(from, to) {
      const params = new URLSearchParams({from, to});
      const res = await fetch('/route?' + params.toString());
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || res.statusText);
      }
      return res.json();
    }

    function render(data) {
      const coords = data.path.map(p => [p.lat, p.lon]);
      if (line) line.remove();
      if (startM) startM.remove();
      if (endM) endM.remove();
      line = L.polyline(coords, {color: '#4da3ff', weight: 5}).addTo(map);
      startM = L.circleMarker(coords[0], {radius: 6, color: '#7cf29c', fillColor: '#7cf29c', fillOpacity: 0.9}).addTo(map);
      endM = L.circleMarker(coords[coords.length-1], {radius: 6, color: '#ffb347', fillColor: '#ffb347', fillOpacity: 0.9}).addTo(map);
      map.fitBounds(line.getBounds(), {padding: [30, 30]});

	document.getElementById('status').textContent = 'OK: ' + data.from.label + ' \u2192 ' + data.to.label;
	document.getElementById('distance').textContent = (data.distance_m/1000).toFixed(1) + ' km | ' + data.path.length + ' Punkte';
    }

    document.getElementById('route-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const from = document.getElementById('from').value.trim();
      const to = document.getElementById('to').value.trim();
      document.getElementById('status').textContent = 'Berechne...';
      try {
        const data = await fetchRoute(from, to);
        render(data);
      } catch (err) {
        document.getElementById('status').textContent = 'Fehler: ' + err.message;
      }
    });

    // initial route
    fetchRoute(document.getElementById('from').value, document.getElementById('to').value)
      .then(render)
      .catch(err => document.getElementById('status').textContent = 'Fehler: ' + err.message);

		// autocomplete helpers
		function makeSuggest(containerId, inputId) {
			const container = document.getElementById(containerId);
			const input = document.getElementById(inputId);
			let timeout = null;
			input.addEventListener('input', () => {
				const q = input.value.trim();
				if (timeout) clearTimeout(timeout);
				if (q.length < 2) { container.style.display = 'none'; return; }
				timeout = setTimeout(async () => {
					try {
						const res = await fetch('/search?q=' + encodeURIComponent(q));
						if (!res.ok) return;
						const data = await res.json();
						container.innerHTML = '';
						if (!Array.isArray(data) || data.length === 0) { container.style.display = 'none'; return; }
						data.slice(0,10).forEach(item => {
							const el = document.createElement('div');
							el.className = 'item';
							const name = (item.label || (item.tags && item.tags['addr:street']) || (item.tags && item.tags.name) || '');
							const city = (item.tags && (item.tags['addr:city'] || item.tags['addr:place'])) || '';
							el.textContent = name + (city ? ' — ' + city : '');
							el.addEventListener('click', () => { input.value = name; container.style.display = 'none'; });
							container.appendChild(el);
						});
						container.style.display = 'block';
					} catch (err) {
						container.style.display = 'none';
					}
				}, 240);
			});
			document.addEventListener('click', (ev) => { if (!container.contains(ev.target) && ev.target !== input) container.style.display = 'none'; });
		}

		makeSuggest('from-suggest','from');
		makeSuggest('to-suggest','to');
  </script>
</body>
</html>`
