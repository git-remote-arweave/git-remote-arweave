package ops

import (
	"context"
	"fmt"

	git "github.com/go-git/go-git/v5"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/pack"
)

// ListRefs returns the ref list from a previously loaded remote state.
// If pending is non-nil, its refs are overlaid on top of the remote refs
// so that git sees the expected state while transactions are in mempool.
// Returns an empty map if the repository does not exist yet.
func ListRefs(rs *RemoteState, pending *localstate.PendingState) map[string]string {
	refs := map[string]string{}
	if rs.m != nil {
		for k, v := range rs.m.Refs {
			refs[k] = v
		}
	}
	if pending != nil {
		for k, v := range pending.Refs {
			refs[k] = v
		}
	}
	return refs
}

// Fetch downloads and applies any new packs from the remote.
// It does NOT update refs — git manages ref updates based on the list output.
func Fetch(
	ctx context.Context,
	ar *arweave.Client,
	repo *git.Repository,
	state *localstate.State,
	rs *RemoteState,
) (*FetchResult, error) {
	if rs.m == nil {
		return &FetchResult{Refs: map[string]string{}}, nil
	}

	// Determine which packs are new.
	applied, err := state.AppliedSet()
	if err != nil {
		return nil, fmt.Errorf("ops: load applied set: %w", err)
	}

	for _, pe := range rs.m.Packs {
		if applied[pe.TX] {
			continue
		}

		data, err := ar.Fetch(ctx, pe.TX)
		if err != nil {
			return nil, fmt.Errorf("ops: fetch pack %q: %w", pe.TX, err)
		}
		if err := pack.Apply(repo, data); err != nil {
			return nil, fmt.Errorf("ops: apply pack %q: %w", pe.TX, err)
		}
		if err := state.MarkApplied(pe.TX); err != nil {
			return nil, fmt.Errorf("ops: mark applied %q: %w", pe.TX, err)
		}
	}

	// Do NOT update refs here — git manages ref updates based on the
	// list output. Setting refs from the helper causes "initial ref
	// transaction called with existing refs" crashes during clone.

	// Record the latest manifest for conflict detection on next push.
	if err := state.SaveLastManifestTxID(rs.manifestTxID); err != nil {
		return nil, fmt.Errorf("ops: save last manifest: %w", err)
	}

	return &FetchResult{Refs: rs.m.Refs}, nil
}
