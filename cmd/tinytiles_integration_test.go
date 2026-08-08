package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tinytiles "github.com/Karte-Bayern/tinyTiles"
	tiles "github.com/SimonWaldherr/tinySQL/tiles"
)

func TestTinyTilesBuildStatusIncludesPublicProgress(t *testing.T) {
	s := &server{}
	recorder := httptest.NewRecorder()
	s.handleTinyTilesBuild(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/tinytiles/build", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if got, want := payload["state"], "idle"; got != want {
		t.Fatalf("state = %#v, want %q", got, want)
	}
	if got, want := payload["phase"], "idle"; got != want {
		t.Fatalf("phase = %#v, want %q", got, want)
	}
	if got, want := payload["progress"], float64(0); got != want {
		t.Fatalf("progress = %#v, want %#v", got, want)
	}
}

func TestTinyTilesPublicBuildProgress(t *testing.T) {
	tests := []struct {
		name         string
		input        tinytiles.PBFBuildProgress
		wantPhase    string
		wantProgress int
	}{
		{
			name:         "generation starts",
			input:        tinytiles.PBFBuildProgress{Phase: "generate"},
			wantPhase:    "generating",
			wantProgress: 5,
		},
		{
			name:         "generation complete",
			input:        tinytiles.PBFBuildProgress{Phase: "generated"},
			wantPhase:    "importing",
			wantProgress: 55,
		},
		{
			name: "import follows completed rows",
			input: tinytiles.PBFBuildProgress{
				Phase:  "import",
				Import: &tiles.Progress{RowsRead: 50, RowsWritten: 50, TotalRows: 100},
			},
			wantPhase:    "importing",
			wantProgress: 78,
		},
		{
			name: "import uses the furthest read or write count",
			input: tinytiles.PBFBuildProgress{
				Phase:  "import",
				Import: &tiles.Progress{RowsRead: 100, RowsWritten: 90, TotalRows: 100},
			},
			wantPhase:    "importing",
			wantProgress: 95,
		},
		{
			name:         "published is finalizing",
			input:        tinytiles.PBFBuildProgress{Phase: "published"},
			wantPhase:    "finalizing",
			wantProgress: 97,
		},
		{
			name:         "unknown phase is public generic processing",
			input:        tinytiles.PBFBuildProgress{Phase: "future-internal-phase"},
			wantPhase:    "processing",
			wantProgress: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			phase, progress, message := tinyTilesPublicBuildProgress(test.input)
			if phase != test.wantPhase || progress != test.wantProgress {
				t.Fatalf("progress = (%q, %d), want (%q, %d)", phase, progress, test.wantPhase, test.wantProgress)
			}
			if message == "" {
				t.Fatal("public progress message is empty")
			}
		})
	}
}

func TestTinyTilesBuildProgressDoesNotMoveBackward(t *testing.T) {
	s := &server{tinyTilesBuild: tinyTilesBuildStatus{State: "building", Phase: "importing", Progress: 80}}
	s.updateTinyTilesBuildProgress("processing", 20, "Offline-Karte wird verarbeitet…")
	if got := s.tinyTilesBuild.Progress; got != 80 {
		t.Fatalf("progress moved backwards to %d, want 80", got)
	}
}
