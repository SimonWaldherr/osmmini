package osmmini

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// WorkResource represents a vehicle or team. Capacity and demand must use the
// same unit (e.g. kilograms or working minutes); capacity zero accepts no demand.
type WorkResource struct {
	ID         string         `json:"id"`
	Location   Coord          `json:"location"`
	Capacity   float64        `json:"capacity"`
	MaxJobs    int            `json:"max_jobs,omitempty"` // zero means unlimited
	Skills     []string       `json:"skills,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type WorkJob struct {
	ID             string         `json:"id"`
	Location       Coord          `json:"location"`
	Demand         float64        `json:"demand"`
	Priority       int            `json:"priority,omitempty"` // higher values are assigned first
	RequiredSkills []string       `json:"required_skills,omitempty"`
	Properties     map[string]any `json:"properties,omitempty"`
}

type WorkPlanRequest struct {
	Resources []WorkResource `json:"resources"`
	Jobs      []WorkJob      `json:"jobs"`
}

type WorkManifest struct {
	ResourceID        string   `json:"resource_id"`
	JobIDs            []string `json:"job_ids"`
	Load              float64  `json:"load"`
	RemainingCapacity float64  `json:"remaining_capacity"`
}

type UnassignedWork struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason"`
}

type WorkPlan struct {
	Algorithm     string           `json:"algorithm"`
	DistanceModel string           `json:"distance_model"`
	Manifests     []WorkManifest   `json:"manifests"`
	Unassigned    []UnassignedWork `json:"unassigned"`
}

// PlanWork produces a stateless, deterministic greedy allocation, not a VRP
// solution or stop sequence. Each accepted constraint adds a finite nonnegative
// cost in metres to depot-to-job great-circle distance. Constraints must be pure.
// Feed each manifest's destinations into the trip solver for road routing.
func PlanWork(ctx context.Context, req WorkPlanRequest, constraints ...AssignmentConstraint) (WorkPlan, error) {
	if len(req.Resources) > 256 || len(req.Jobs) > 5000 {
		return WorkPlan{}, fmt.Errorf("work plan: maximum 256 resources and 5000 jobs")
	}
	validLocation := func(c Coord) bool {
		return finiteNonnegative(math.Abs(c.Lat)) && finiteNonnegative(math.Abs(c.Lon)) && math.Abs(c.Lat) <= 90 && math.Abs(c.Lon) <= 180
	}
	seen := map[string]bool{}
	resources := append([]WorkResource(nil), req.Resources...)
	for _, r := range resources {
		if strings.TrimSpace(r.ID) == "" || seen[r.ID] || !validLocation(r.Location) || !finiteNonnegative(r.Capacity) || r.MaxJobs < 0 {
			return WorkPlan{}, fmt.Errorf("work plan: invalid or duplicate resource %q", r.ID)
		}
		seen[r.ID] = true
	}
	seen = map[string]bool{}
	jobs := append([]WorkJob(nil), req.Jobs...)
	for _, j := range jobs {
		if strings.TrimSpace(j.ID) == "" || seen[j.ID] || !validLocation(j.Location) || !finiteNonnegative(j.Demand) {
			return WorkPlan{}, fmt.Errorf("work plan: invalid or duplicate job %q", j.ID)
		}
		seen[j.ID] = true
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Priority != jobs[j].Priority {
			return jobs[i].Priority > jobs[j].Priority
		}
		return jobs[i].ID < jobs[j].ID
	})
	plan := WorkPlan{Algorithm: "priority-greedy", DistanceModel: "great-circle-from-resource", Manifests: make([]WorkManifest, len(resources)), Unassigned: []UnassignedWork{}}
	skills := make([]map[string]bool, len(resources))
	for i, r := range resources {
		plan.Manifests[i] = WorkManifest{ResourceID: r.ID, JobIDs: []string{}, RemainingCapacity: r.Capacity}
		skills[i] = map[string]bool{}
		for _, skill := range r.Skills {
			skills[i][skill] = true
		}
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return WorkPlan{}, err
		}
		best, bestCost, eligible := -1, math.Inf(1), false
		for i, r := range resources {
			qualified := true
			for _, skill := range job.RequiredSkills {
				if !skills[i][skill] {
					qualified = false
					break
				}
			}
			if !qualified {
				continue
			}
			vehicle := Vehicle{ID: r.ID, Properties: r.Properties}
			shipment := Shipment{ID: job.ID, Lat: job.Location.Lat, Lon: job.Location.Lon, Properties: job.Properties}
			cost := haversineMeters(r.Location.Lat, r.Location.Lon, job.Location.Lat, job.Location.Lon)
			for _, c := range constraints {
				if c == nil {
					return WorkPlan{}, fmt.Errorf("work plan: nil constraint")
				}
				if !c.Accept(vehicle, shipment) {
					qualified = false
					break
				}
				delta := c.Cost(vehicle, shipment)
				if !finiteNonnegative(delta) {
					return WorkPlan{}, fmt.Errorf("work plan: invalid constraint cost")
				}
				cost += delta
			}
			if !qualified {
				continue
			}
			if !finiteNonnegative(cost) {
				return WorkPlan{}, fmt.Errorf("work plan: cost overflow")
			}
			eligible = true
			m := &plan.Manifests[i]
			if job.Demand > m.RemainingCapacity || (r.MaxJobs > 0 && len(m.JobIDs) >= r.MaxJobs) {
				continue
			}
			if cost < bestCost {
				best, bestCost = i, cost
			}
		}
		if best < 0 {
			reason := "no_eligible_resource"
			if eligible {
				reason = "capacity_exceeded"
			}
			plan.Unassigned = append(plan.Unassigned, UnassignedWork{JobID: job.ID, Reason: reason})
			continue
		}
		m := &plan.Manifests[best]
		m.JobIDs = append(m.JobIDs, job.ID)
		m.Load += job.Demand
		m.RemainingCapacity = resources[best].Capacity - m.Load
	}
	if err := ctx.Err(); err != nil {
		return WorkPlan{}, err
	}
	return plan, nil
}

func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
