package policy

import "context"

// Classifier is the pluggable verdict backend behind `mode: classify` rules.
// Implementations judge ONE event pre-execution: no session context, no side
// effects. The engine never calls this, so it stays I/O-free. PEPs do, own
// the deadline, and MUST fail closed: any error or blown deadline is a deny
// with reason, mirroring the serverCheck escalation posture.
//
// The backend today is internal/classifier (heuristic, no model).
type Classifier interface {
	Classify(ctx context.Context, ev Event) (ClassifyVerdict, error)
}

// ClassifyVerdict is a classifier's pre-execution judgment of one event.
type ClassifyVerdict struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"` // required when denied
}
