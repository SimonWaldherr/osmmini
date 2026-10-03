package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestOperationsStorePersistsPODAndMaintenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store := NewOperationsStore(path)
	latitude, longitude := 48.5667, 13.4319
	pod, err := store.Create(OperationRecord{
		Type: operationTypePOD, AssetCode: "PKG-4711", Status: "delivered", Recipient: "Max Mustermann",
		Latitude: &latitude, Longitude: &longitude, OccurredAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("create pod: %v", err)
	}
	if pod.ID == "" || pod.CreatedAt.IsZero() || pod.OccurredAt.IsZero() {
		t.Fatalf("created pod = %#v", pod)
	}
	if _, err := store.Create(OperationRecord{Type: operationTypeMaintenance, AssetCode: "PUMP-4", Status: "completed", WorkType: "inspection"}); err != nil {
		t.Fatalf("create maintenance: %v", err)
	}
	if _, err := store.Create(OperationRecord{Type: operationTypeCheck, AssetCode: "SHELTER-NORTH", Status: "available", Reference: "Abschnitt Nord"}); err != nil {
		t.Fatalf("create check: %v", err)
	}

	reloaded := NewOperationsStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("load operations: %v", err)
	}
	all := reloaded.List("", 10)
	if len(all) != 3 || all[0].Type != operationTypeCheck || all[2].AssetCode != "PKG-4711" {
		t.Fatalf("reloaded records = %#v", all)
	}
}

func TestOperationsHandlerValidatesAndCreates(t *testing.T) {
	s := &server{operations: NewOperationsStore(filepath.Join(t.TempDir(), "operations.json"))}
	payload, err := json.Marshal(OperationRecord{Type: operationTypePOD, AssetCode: "QR-42", Status: "delivered", Recipient: "Empfang", OccurredAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	created := httptest.NewRecorder()
	s.handleOperations(created, httptest.NewRequest(http.MethodPost, "/api/v1/operations", bytes.NewReader(payload)))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	var record OperationRecord
	if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.ID == "" || record.AssetCode != "QR-42" {
		t.Fatalf("created operation = %#v", record)
	}

	list := httptest.NewRecorder()
	s.handleOperations(list, httptest.NewRequest(http.MethodGet, "/api/v1/operations?type=pod", nil))
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte("QR-42")) {
		t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
	}

	invalid := httptest.NewRecorder()
	s.handleOperations(invalid, httptest.NewRequest(http.MethodPost, "/api/v1/operations", bytes.NewBufferString(`{"type":"pod","asset_code":"QR-42","status":"delivered"}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("missing recipient status = %d: %s", invalid.Code, invalid.Body.String())
	}
}

func TestOperationsUnorderedHistoryLimitsAfterOrdering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	now := time.Now().UTC()
	records := []OperationRecord{
		{ID: "newest", Type: operationTypeCheck, CreatedAt: now},
		{ID: "other", Type: operationTypeMaintenance, CreatedAt: now.Add(-time.Minute)},
		{ID: "older", Type: operationTypeCheck, CreatedAt: now.Add(-time.Hour)},
	}
	data, _ := json.Marshal(records)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewOperationsStore(path)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"", operationTypeCheck} {
		got := store.List(kind, 1)
		if len(got) != 1 || got[0].ID != "newest" {
			t.Fatalf("kind %q: got %#v", kind, got)
		}
	}
	if !reflect.DeepEqual(store.records, records) {
		t.Fatal("load changed persisted ordering")
	}
	// A typed decode error can partially populate a destination slice.
	if err := os.WriteFile(path, []byte(`[{"id":"damaged","created_at":42}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err == nil {
		t.Fatal("expected decode failure")
	}
	if got := store.List("", 1); len(got) != 1 || got[0].ID != "newest" {
		t.Fatalf("failed reload damaged history: %#v", got)
	}
}

func TestOperationsCoordinatesDoNotAliasStore(t *testing.T) {
	store := NewOperationsStore(filepath.Join(t.TempDir(), "operations.json"))
	lat, lon := 48.0, 12.0
	created, err := store.Create(OperationRecord{Type: operationTypeCheck, AssetCode: "test", Status: "available", Latitude: &lat, Longitude: &lon})
	if err != nil {
		t.Fatal(err)
	}
	lat = 0
	*created.Longitude = 0
	got := store.List("", 1)
	if *got[0].Latitude != 48 || *got[0].Longitude != 12 {
		t.Fatal("caller mutated stored coordinates")
	}
	*got[0].Latitude = 1
	if *store.List("", 1)[0].Latitude != 48 {
		t.Fatal("list exposes stored coordinates")
	}
}

func TestOperationsFailedWriteDoesNotPublishIndex(t *testing.T) {
	store := NewOperationsStore(filepath.Join(t.TempDir(), "operations.json"))
	record := OperationRecord{Type: operationTypeCheck, AssetCode: "test", Status: "available"}
	if _, err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	// Force rename failure without relying on filesystem permission semantics.
	store.path = t.TempDir()
	if _, err := store.Create(record); err == nil {
		t.Fatal("expected write failure")
	}
	if len(store.records) != 1 || len(store.newest) != 1 || len(store.List("", 10)) != 1 {
		t.Fatal("failed write leaked into history")
	}
}

func TestOperationsClientEventIDRetryAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	store := NewOperationsStore(path)
	input := OperationRecord{
		ClientEventID: "offline-event-1", Type: operationTypeCheck,
		AssetCode: "WATER-1", Status: "available", Actor: "alice",
		OccurredAt: time.Now().Add(-time.Minute).UTC(),
	}
	first, err := store.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.Create(input)
	if err != nil || retry.ID != first.ID || len(store.List("", 10)) != 1 {
		t.Fatalf("same-session retry: record=%#v err=%v", retry, err)
	}
	reloaded := NewOperationsStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	retry, err = reloaded.Create(input)
	if err != nil || retry.ID != first.ID || len(reloaded.List("", 10)) != 1 {
		t.Fatalf("reloaded retry: record=%#v err=%v", retry, err)
	}
	for _, changed := range []OperationRecord{
		func() OperationRecord { item := input; item.Status = "unavailable"; return item }(),
		func() OperationRecord { item := input; item.Actor = "bob"; return item }(),
	} {
		if _, err := reloaded.Create(changed); !errors.Is(err, ErrOperationIdempotencyConflict) {
			t.Fatalf("changed retry error = %v", err)
		}
	}
	if len(reloaded.List("", 10)) != 1 {
		t.Fatal("conflicting retries changed the store")
	}
}

func TestOperationsRejectNonFiniteCoordinatesAndInvalidEventID(t *testing.T) {
	store := NewOperationsStore(filepath.Join(t.TempDir(), "operations.json"))
	lat, lon := math.NaN(), 12.0
	base := OperationRecord{Type: operationTypeCheck, AssetCode: "TEST", Status: "available", Latitude: &lat, Longitude: &lon}
	if _, err := store.Create(base); err == nil {
		t.Fatal("NaN latitude accepted")
	}
	lat, lon = 48, math.Inf(1)
	if _, err := store.Create(base); err == nil {
		t.Fatal("infinite longitude accepted")
	}
	base.Latitude, base.Longitude = nil, nil
	base.ClientEventID = "unsafe/id"
	if _, err := store.Create(base); err == nil {
		t.Fatal("invalid event ID accepted")
	}
}

func TestOperationsHandlerIdempotentRetryAndConflict(t *testing.T) {
	s := &server{operations: NewOperationsStore(filepath.Join(t.TempDir(), "operations.json"))}
	record := OperationRecord{ClientEventID: "retry-1", Type: operationTypeCheck, AssetCode: "WATER-1", Status: "available"}
	post := func(input OperationRecord) *httptest.ResponseRecorder {
		payload, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		s.handleOperations(response, httptest.NewRequest(http.MethodPost, "/api/v1/operations", bytes.NewReader(payload)))
		return response
	}
	first := post(record)
	retry := post(record)
	if first.Code != http.StatusCreated || retry.Code != http.StatusCreated || first.Body.String() != retry.Body.String() {
		t.Fatalf("retry status/body: first=%d %s retry=%d %s", first.Code, first.Body.String(), retry.Code, retry.Body.String())
	}
	record.Status = "unavailable"
	if conflict := post(record); conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d: %s", conflict.Code, conflict.Body.String())
	}
}
