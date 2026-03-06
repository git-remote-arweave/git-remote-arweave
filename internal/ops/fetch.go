package ops

import (
	"context"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/pack"
)

// ListRefs returns the ref list from a previously loaded remote state.
// Returns an empty map if the repository does not exist yet.
func ListRefs(rs *RemoteState) map[string]string {
	if rs.m == nil {
		return map[string]string{}
	}
	return rs.m.Refs
}

// Fetch downloads and applies any new packs from the remote.
// It updates local remote-tracking refs and the applied-packs set.
// Returns the current remote refs.
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

	// Update remote-tracking refs.
	for name, sha := range rs.m.Refs {
		ref := plumbing.NewHashReference(plumbing.ReferenceName(name), plumbing.NewHash(sha))
		if err := repo.Storer.SetReference(ref); err != nil {
			return nil, fmt.Errorf("ops: set ref %q: %w", name, err)
		}
	}

	// Record the latest manifest for conflict detection on next push.
	if err := state.SaveLastManifestTxID(rs.manifestTxID); err != nil {
		return nil, fmt.Errorf("ops: save last manifest: %w", err)
	}

	return &FetchResult{Refs: rs.m.Refs}, nil
}
