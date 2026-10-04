package main

import (
	"net/http"

	osmmini "simonwaldherr.de/go/osmmini"
)

func (s *server) handleWorkPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		osmmini.WorkPlanRequest
		Layer      string `json:"layer,omitempty"`
		VehicleKey string `json:"vehicle_key,omitempty"`
	}
	if err := readJSON(w, r, &req, 4<<20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid work plan: "+err.Error())
		return
	}
	var constraints []osmmini.AssignmentConstraint
	if req.Layer != "" {
		s.territoriesMu.RLock()
		store := s.territories
		s.territoriesMu.RUnlock()
		found := false
		if store != nil {
			for _, layer := range store.Layers() {
				if layer == req.Layer {
					found = true
					break
				}
			}
		}
		if !found {
			writeJSONError(w, http.StatusBadRequest, "unknown territory layer")
			return
		}
		if req.VehicleKey == "" {
			req.VehicleKey = "territory_id"
		}
		constraints = append(constraints, osmmini.TerritoryConstraint{Store: store, Layer: req.Layer, VehicleKey: req.VehicleKey})
	} else if req.VehicleKey != "" {
		writeJSONError(w, http.StatusBadRequest, "vehicle_key requires layer")
		return
	}
	plan, err := osmmini.PlanWork(r.Context(), req.WorkPlanRequest, constraints...)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
