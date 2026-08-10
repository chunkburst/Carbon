package backup

import (
	"context"
	"errors"
	"time"
)

// RetentionPolicy applies only to the local manifest index. A snapshot is
// retained whenever it is either among the newest KeepLast snapshots or newer
// than KeepDays; using the union avoids surprising deletion during a busy day.
type RetentionPolicy struct {
	KeepLast int
	KeepDays int
}

// PruneResult describes local retention work.
type PruneResult struct {
	Retained      int `json:"retained"`
	Pruned        int `json:"pruned"`
	ObjectsPruned int `json:"objectsPruned"`
}

// PruneLocal removes local manifests that have aged out of the policy and then
// reclaims only their file objects that no retained manifest references. It
// never constructs a remote client or calls a remote BlobStore. Listing
// validates every recognized manifest before any deletion, so malformed metadata
// fails closed and preserves all local recovery points.
func (r *Repository) PruneLocal(ctx context.Context, policy RetentionPolicy) (PruneResult, error) {
	return r.PruneLocalAt(ctx, policy, r.now().UTC())
}

// PruneLocalAt is PruneLocal with an injected clock for deterministic hosts and tests.
func (r *Repository) PruneLocalAt(ctx context.Context, policy RetentionPolicy, now time.Time) (PruneResult, error) {
	if err := ctx.Err(); err != nil {
		return PruneResult{}, err
	}
	if policy.KeepLast < 1 || policy.KeepLast > maxLocalRetention {
		return PruneResult{}, errors.New("backup local retention keepLast is invalid")
	}
	if policy.KeepDays < 1 || policy.KeepDays > maxLocalRetention {
		return PruneResult{}, errors.New("backup local retention keepDays is invalid")
	}
	local, ok := r.store.(*LocalBlobStore)
	if !ok {
		return PruneResult{}, errors.New("backup local retention requires a local store")
	}
	items, err := r.List(ctx)
	if err != nil {
		return PruneResult{}, err
	}
	cutoff := now.UTC().AddDate(0, 0, -policy.KeepDays)
	result := PruneResult{}
	prune := make([]SnapshotInfo, 0)
	reachable := make(map[string]struct{})
	for index, item := range items {
		if err := ctx.Err(); err != nil {
			return PruneResult{}, err
		}
		keepByCount := index < policy.KeepLast
		keepByAge := !item.Manifest.CreatedAt.Before(cutoff)
		if keepByCount || keepByAge {
			result.Retained++
			for _, file := range item.Manifest.Files {
				reachable[ObjectKey(file.SHA256)] = struct{}{}
			}
			continue
		}
		prune = append(prune, item)
	}
	// Remove indexes first. A failed deletion leaves the objects intact, which
	// is safe. Object deletion happens only after all manifest reachability was
	// validated and after every retained reference has been collected.
	for _, item := range prune {
		if err := local.Delete(ctx, ManifestKey(item.Snapshot.ID)); err != nil {
			return PruneResult{}, err
		}
		result.Pruned++
	}
	candidates := make(map[string]struct{})
	for _, item := range prune {
		for _, file := range item.Manifest.Files {
			key := ObjectKey(file.SHA256)
			if _, retained := reachable[key]; !retained {
				candidates[key] = struct{}{}
			}
		}
	}
	for key := range candidates {
		if err := ctx.Err(); err != nil {
			return PruneResult{}, err
		}
		if err := local.Delete(ctx, key); err != nil {
			return PruneResult{}, err
		}
		result.ObjectsPruned++
	}
	return result, nil
}
