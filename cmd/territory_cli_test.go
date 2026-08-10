package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The territories/territory/dispatch/route subcommands call log.Fatalf/
// os.Exit on bad input, which would abort the whole `go test` process if
// invoked in-process. TestMain builds the real osmmini binary once and the
// tests below drive it as a subprocess -- genuine black-box coverage of the
// shipped CLI, and immune to that hazard.
var territoryCLIBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "osmmini-cli-test-")
	if err != nil {
		panic(err)
	}
	territoryCLIBin = filepath.Join(dir, "osmmini")
	build := exec.Command("go", "build", "-o", territoryCLIBin, ".")
	build.Env = append(os.Environ(), "GO111MODULE=on")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic("build osmmini CLI for tests: " + err.Error() + "\n" + string(out))
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func repoTestdata(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "testdata", "territory", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("missing test fixture %s: %v", abs, err)
	}
	return abs
}

func runCLI(t *testing.T, dir string, args ...string) (stdout, stderr string, exitErr error) {
	t.Helper()
	cmd := exec.Command(territoryCLIBin, args...)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

func TestCLITerritoriesLoadAndLookup(t *testing.T) {
	dir := t.TempDir()
	fixture := repoTestdata(t, "delivery-zones.geojson")

	out, stderr, err := runCLI(t, dir, "territories", "load", "--layer", "delivery", "--input", fixture)
	if err != nil {
		t.Fatalf("territories load failed: %v\nstdout=%s\nstderr=%s", err, out, stderr)
	}

	out, stderr, err = runCLI(t, dir, "territory", "lookup", "--layer", "delivery", "--lat", "48.65", "--lon", "12.65")
	if err != nil {
		t.Fatalf("territory lookup failed: %v\nstderr=%s", err, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON output %q: %v", out, err)
	}
	if result["territory_id"] != "Zone-1" {
		t.Fatalf("territory_id = %v, want Zone-1", result["territory_id"])
	}
	if result["vehicle"] != "VAN-01" {
		t.Fatalf("vehicle = %v, want VAN-01", result["vehicle"])
	}

	// A point outside every territory: matched=false, not an arbitrary guess.
	out, _, err = runCLI(t, dir, "territory", "lookup", "--layer", "delivery", "--lat", "0", "--lon", "0")
	if err != nil {
		t.Fatalf("territory lookup (unmatched) failed: %v", err)
	}
	var unmatched map[string]any
	if err := json.Unmarshal([]byte(out), &unmatched); err != nil {
		t.Fatalf("invalid JSON output %q: %v", out, err)
	}
	if matched, _ := unmatched["matched"].(bool); matched {
		t.Fatalf("expected matched=false for an out-of-territory point, got %v", unmatched)
	}
}

func TestCLIDispatchAssign(t *testing.T) {
	dir := t.TempDir()
	fixture := repoTestdata(t, "delivery-zones.geojson")
	parcelsCSV := filepath.Join(dir, "parcels.csv")
	if err := os.WriteFile(parcelsCSV, []byte(
		"parcel_id,lat,lon,weight_kg\n"+
			"P001,48.65,12.65,4.3\n"+
			"P002,48.62,12.85,1.1\n"+
			"P003,40.0,10.0,3.0\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	outputCSV := filepath.Join(dir, "assignments.csv")

	_, stderr, err := runCLI(t, dir, "dispatch", "assign",
		"--territories", fixture,
		"--layer", "delivery",
		"--input", parcelsCSV,
		"--output", outputCSV,
		"--include", "territory_id,depot,vehicle",
	)
	if err != nil {
		t.Fatalf("dispatch assign failed: %v\nstderr=%s", err, stderr)
	}

	f, err := os.Open(outputCSV)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 { // header + 3 rows
		t.Fatalf("got %d CSV rows, want 4: %v", len(records), records)
	}
	wantHeader := []string{"parcel_id", "territory_id", "depot", "vehicle"}
	for i, h := range wantHeader {
		if records[0][i] != h {
			t.Fatalf("header[%d] = %q, want %q (full header: %v)", i, records[0][i], h, records[0])
		}
	}
	byParcel := map[string][]string{}
	for _, row := range records[1:] {
		byParcel[row[0]] = row
	}
	if got := byParcel["P001"]; got[1] != "Zone-1" || got[2] != "Depot-Passau" || got[3] != "VAN-01" {
		t.Fatalf("P001 row = %v, want Zone-1/Depot-Passau/VAN-01", got)
	}
	// P002 lands in Zone-2, whose "depot" property is a merged list
	// (Depot-Freyung and Depot-Passau disagree) -- CSV output must
	// pipe-join it rather than crash or print a Go-formatted slice.
	if got := byParcel["P002"]; got[1] != "Zone-2" || got[2] != "Depot-Freyung|Depot-Passau" {
		t.Fatalf("P002 row = %v, want Zone-2/Depot-Freyung|Depot-Passau", got)
	}
	// P003 is outside every territory: default policy is "unassigned".
	if got := byParcel["P003"]; got[1] != "" {
		t.Fatalf("P003 row = %v, want an empty territory_id (unassigned)", got)
	}
}

// 10. Vehicle manifest grouping: one CSV per group value, each carrying the
// full original row plus the resolved territory_id.
func TestCLIDispatchManifests(t *testing.T) {
	dir := t.TempDir()
	fixture := repoTestdata(t, "delivery-zones.geojson")
	parcelsCSV := filepath.Join(dir, "parcels.csv")
	if err := os.WriteFile(parcelsCSV, []byte(
		"parcel_id,lat,lon,weight_kg\n"+
			"P001,48.65,12.65,4.3\n"+
			"P002,48.62,12.85,1.1\n"+
			"P003,48.98,13.0,12.8\n"+
			"P004,40.0,10.0,3.0\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestsDir := filepath.Join(dir, "manifests")

	_, stderr, err := runCLI(t, dir, "dispatch", "manifests",
		"--territories", fixture,
		"--layer", "delivery",
		"--input", parcelsCSV,
		"--group-by", "vehicle",
		"--output", manifestsDir,
	)
	if err != nil {
		t.Fatalf("dispatch manifests failed: %v\nstderr=%s", err, stderr)
	}

	for _, want := range []string{"VAN-01.csv", "VAN-02.csv", "VAN-03.csv", "UNASSIGNED.csv"} {
		if _, err := os.Stat(filepath.Join(manifestsDir, want)); err != nil {
			t.Fatalf("expected manifest %s to exist: %v", want, err)
		}
	}

	f, err := os.Open(filepath.Join(manifestsDir, "VAN-01.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 { // header + P001
		t.Fatalf("VAN-01.csv has %d rows, want 2: %v", len(records), records)
	}
	if records[1][0] != "P001" {
		t.Fatalf("VAN-01.csv row = %v, want parcel_id P001", records[1])
	}
}
