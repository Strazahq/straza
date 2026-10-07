package store

import "context"

// PolicyRepo manages PolicySet documents.
type PolicyRepo interface {
	Create(ctx context.Context, p PolicySet) (PolicySet, error)
	GetByID(ctx context.Context, id string) (PolicySet, error)
	GetByName(ctx context.Context, name string) (PolicySet, error)
	List(ctx context.Context) ([]PolicySet, error)
	// ListMeta is List without hauling yaml_source (rows carry an empty
	// YAMLSource): the list endpoint reads metadata every call while YAML
	// bytes move only on a summary-cache miss or a single-set fetch.
	ListMeta(ctx context.Context) ([]PolicySet, error)
	Update(ctx context.Context, p PolicySet) (PolicySet, error)
	Delete(ctx context.Context, id string) error
}

// SnapshotRepo manages compiled policy snapshots.
type SnapshotRepo interface {
	Create(ctx context.Context, s Snapshot) (Snapshot, error)
	GetByID(ctx context.Context, id string) (Snapshot, error)
	GetActive(ctx context.Context) (Snapshot, error)
	SetActive(ctx context.Context, id string) error
	// SetActiveFrom activates id only while from is still the active
	// snapshot. It returns ErrConflict when another snapshot became active
	// first, or none is, and ErrNotFound for an unknown id; either way the
	// active snapshot is left as it was. A publish built on from uses it so
	// that it never overwrites a publish that landed in between.
	SetActiveFrom(ctx context.Context, id, from string) error
	List(ctx context.Context) ([]Snapshot, error)
}
