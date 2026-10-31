package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	osmmini "simonwaldherr.de/go/osmmini/core"
	"strings"
	"testing"
)

func TestAIUIProtocol(t *testing.T) {
	text, ui, err := extractAIUI("Hier.\n```agent-ui\n{\"search\":[\"Dingolfing\"],\"elements\":[{\"type\":\"card\",\"text\":\"Details\"}]}\n```")
	if err != nil || text != "Hier." || len(ui.Search) != 1 || len(ui.Elements) != 1 {
		t.Fatalf("%q %#v %v", text, ui, err)
	}
	text, _, err = extractAIUI("```agent-ui\n{broken}\n```")
	if err == nil || strings.Contains(text, "broken") {
		t.Fatal("malformed output not rejected")
	}
	_, ui, err = extractAIUI("```agent-ui\n{\"search\":[\"1\",\"2\",\"3\",\"4\",\"5\"]}\n```")
	if err != nil || len(ui.Search) != 4 {
		t.Fatal("search limit missing")
	}
	for _, p := range []string{"Zeichne einen Kreis um Dingolfing", "Markiere das Kino", "Zeige ein Diagramm", "Zeige eine Ortskarte für das Museum"} {
		if !wantsAIVisuals(p) {
			t.Fatal(p)
		}
	}
	if wantsAIVisuals("Route nach Dingolfing") {
		t.Fatal("navigation intercepted")
	}
}

func TestAIUISearchRoundTrip(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		answer := "```agent-ui\n{\"search\":[\"Dingolfing\"]}\n```"
		if calls > 1 {
			if !strings.Contains(body.Messages[0].Content, "Lokale Werkzeugergebnisse") || !strings.Contains(body.Messages[0].Content, "48.63") {
				t.Error("missing grounded search results")
			}
			answer = "Hier ist der Ort.\n```agent-ui\n{\"elements\":[{\"type\":\"marker\",\"coordinates\":[[12.5,48.63]]}]}\n```"
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": answer}}}})
	}))
	defer provider.Close()
	s := navigationFixture()
	s.router = osmmini.NewRouterFromGraph(map[int64]osmmini.Coord{}, map[int64][]osmmini.Edge{})
	s.settings = NewSettingsStore("", DefaultSettings("", ""))
	s.aiProbe.set([]aiProvider{{Name: "lmstudio", URL: provider.URL, Available: true, Models: []string{"test"}}})
	rec := httptest.NewRecorder()
	s.handleAIQuery(rec, httptest.NewRequest("POST", "/api/v1/ai/query", strings.NewReader(`{"prompt":"Markiere Dingolfing","model":"test"}`)))
	var result aiQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || rec.Code != 200 || len(result.Elements) != 1 || strings.Contains(result.Response, "agent-ui") {
		t.Fatalf("calls=%d status=%d body=%s", calls, rec.Code, rec.Body.String())
	}
}
