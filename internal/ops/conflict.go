package ops

import (
	"context"
	"fmt"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
)

// checkConflict verifies that the local parent expectation matches on-chain state.
// When the local last-manifest is ahead of GraphQL (previous push delivered
// but not yet indexed), checkConflict fetches the manifest body
// from the gateway and walks the parent chain to verify ancestry. If the
// on-chain manifest is an ancestor of our local manifest, it returns the
// local manifest as the effective remote state (no conflict).
func checkConflict(
	ctx context.Context,
	ar *arweave.Client,
	rs *RemoteState,
	res *pendingResolution,
	state *localstate.State,
) (*RemoteState, error) {
	// New repo — no conflict possible.
	if rs.m == nil {
		return rs, nil
	}

	switch res.outcome {
	case pendingConfirmed, noPending:
		lastManifest, lastParent, err := state.LoadLastManifest()
		if err != nil {
			return nil, fmt.Errorf("ops: load last manifest for conflict check: %w", err)
		}
		if lastManifest == "" {
			return rs, nil
		}
		if lastManifest == rs.manifestTxID {
			return rs, nil
		}
		// Manifests differ — check both directions (local ahead or
		// remote ahead) before declaring a conflict.
		return resolveManifestMismatch(ctx, ar, rs, lastManifest, lastParent)

	case pendingInMempool, pendingReUploaded:
		if res.parentTxID != rs.manifestTxID {
			return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from pending parent %q", rs.manifestTxID, res.parentTxID)
		}
	}
	return rs, nil
}

// resolveManifestMismatch handles the case where last-manifest differs from
// the on-chain manifest. Two scenarios:
//
//  1. Local ahead: our last push is delivered but GraphQL hasn't indexed it yet.
//     Walk from lastManifest → parents looking for rs.manifestTxID.
//  2. Remote ahead: someone (or another machine) pushed after us, or our
//     interrupted push actually succeeded. Walk from rs.manifestTxID → parents
//     looking for lastManifest.
//
// In both cases, if ancestry is confirmed, there is no conflict.
// lastParent is the Parent-Tx stored alongside lastManifest in local state,
// used as a fast path when the manifest body can't be parsed (encrypted).
func resolveManifestMismatch(
	ctx context.Context,
	ar *arweave.Client,
	rs *RemoteState,
	lastManifest, lastParent string,
) (*RemoteState, error) {
	if ar == nil {
		return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from local %q; run git fetch first", rs.manifestTxID, lastManifest)
	}

	// Case 1: local ahead — check if rs.manifestTxID is an ancestor of lastManifest.
	// Fast path: if the stored parent matches, no need to fetch/parse the body.
	if lastParent == rs.manifestTxID {
		// Our local manifest directly descends from the on-chain manifest.
		// We can't parse the encrypted body, but we know ancestry holds.
		// Re-use the on-chain manifest as effective remote state since
		// the push will build on top of it.
		return rs, nil
	}
	// Slow path: fetch and parse manifest bodies (works for unencrypted repos).
	localM, err := walkAncestry(ctx, ar, lastManifest, rs.manifestTxID)
	if err == nil {
		return &RemoteState{manifestTxID: lastManifest, m: localM}, nil
	}

	// Case 2: remote ahead — walk from rs.manifestTxID toward lastManifest.
	// rs.m is already loaded. Check if lastManifest is in its ancestry.
	if rs.m != nil && isAncestor(ctx, ar, rs.m, rs.manifestTxID, lastManifest) {
		// Our last-manifest is an ancestor of the remote manifest.
		// Remote is ahead — accept it and update local state.
		return rs, nil
	}

	return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from local %q; run git fetch first", rs.manifestTxID, lastManifest)
}

// walkAncestry fetches manifests from startTxID → parents looking for
// targetTxID. Returns the manifest at startTxID on success.
func walkAncestry(ctx context.Context, ar *arweave.Client, startTxID, targetTxID string) (*manifest.Manifest, error) {
	cur := startTxID
	var startM *manifest.Manifest
	for i := 0; i < 10; i++ {
		data, err := ar.Fetch(ctx, cur)
		if err != nil {
			return nil, err
		}
		m, err := manifest.Parse(data)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			startM = m
		}
		if cur == targetTxID || m.Parent == targetTxID {
			return startM, nil
		}
		if m.Parent == "" {
			break
		}
		cur = m.Parent
	}
	return nil, fmt.Errorf("target %q not found in ancestry of %q", targetTxID, startTxID)
}

// isAncestor checks whether targetTxID appears in the parent chain of
// the manifest at startTxID. startM is the already-parsed manifest for
// startTxID (avoids re-fetching).
func isAncestor(ctx context.Context, ar *arweave.Client, startM *manifest.Manifest, startTxID, targetTxID string) bool {
	if startTxID == targetTxID {
		return true
	}
	cur := startM.Parent
	for i := 0; i < 10; i++ {
		if cur == "" {
			return false
		}
		if cur == targetTxID {
			return true
		}
		data, err := ar.Fetch(ctx, cur)
		if err != nil {
			return false
		}
		m, err := manifest.Parse(data)
		if err != nil {
			return false
		}
		cur = m.Parent
	}
	return false
}
