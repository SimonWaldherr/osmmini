package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	osmmini "simonwaldherr.de/go/osmmini"
)

func TestWorkPlanCLI(t *testing.T) {
	command := exec.Command(territoryCLIBin, "dispatch", "plan", "--input", "../testdata/dispatch/maintenance.json")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI: %v %s", err, out)
	}
	var plan osmmini.WorkPlan
	if err := json.Unmarshal(out, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Manifests) != 2 || len(plan.Unassigned) != 1 || plan.Unassigned[0].JobID != "roof-inspection" || plan.Manifests[0].Load != 90 || plan.Manifests[1].Load != 45 {
		t.Fatal(plan)
	}
	badFile := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badFile, []byte(`{"jobs":[{"id":"duplicate"},{"id":"duplicate"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(territoryCLIBin, "dispatch", "plan", "--input", badFile).Run(); err == nil {
		t.Fatal("CLI accepted duplicate jobs")
	}
}

func TestWorkPlanHTTP(t *testing.T) {
	s := &server{}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "delivery.geojson"), []byte(territoryGeoJSONFixture), 0600); err != nil {
		t.Fatal(err)
	}
	s.loadTerritories(dir)
	for _, tc := range []struct {
		method, body string
		status       int
	}{
		{"POST", `{"resources":[],"jobs":[]}`, 200},
		{"POST", `{"resources":[],"jobs":[{"id":"job","location":{"lat":48,"lon":12},"demand":1}]}`, 200},
		{"POST", `{"layer":"delivery","resources":[{"id":"van","capacity":3,"properties":{"territory_id":"north"}}],"jobs":[{"id":"job","location":{"lat":48.5,"lon":12.5},"demand":2}]}`, 200},
		{"POST", `{"layer":"missing"}`, 400},
		{"POST", `{"vehicle_key":"zone"}`, 400},
		{"POST", `{"jobs":[{"id":"job","location":{"lat":91}}]}`, 400},
		{"POST", `{"unknown":true}`, 400},
		{"POST", `{} {}`, 400},
		{"GET", `{}`, 405},
	} {
		w := httptest.NewRecorder()
		s.handleWorkPlan(w, httptest.NewRequest(tc.method, "/api/v1/dispatch/plan", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
		if tc.status == http.StatusOK {
			var plan osmmini.WorkPlan
			if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.Algorithm != "priority-greedy" || plan.Unassigned == nil || plan.Manifests == nil {
				t.Fatal(plan)
			}
			if strings.Contains(tc.body, `"north"`) && (len(plan.Manifests[0].JobIDs) != 1 || plan.Manifests[0].Load != 2) {
				t.Fatal(plan)
			}
		}
	}
}
