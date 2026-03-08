package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
	"git-remote-arweave/internal/pack"
)

// PushInput describes a set of ref updates to push.
type PushInput struct {
	// RefUpdates maps ref names to their new commit SHAs.
	// A SHA of all-zeros means "delete this ref".
	RefUpdates map[string]string
	// Force creates a new genesis manifest with a full packfile,
	// ignoring any existing remote state.
	Force bool
}

// Push uploads new objects and updates the remote ref manifest.
// It handles pending state, conflict detection, pack generation,
// and manifest creation.
func Push(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	repo *git.Repository,
	state *localstate.State,
	cfg *config.Config,
	owner, repoName string,
	input *PushInput,
) (*PushResult, error) {
	if input.Force {
		return forcePush(ctx, uploader, repo, state, repoName, input)
	}

	// 1. Resolve any pending push.
	res, err := resolvePending(ctx, ar, uploader, state, cfg.DropTimeout, repoName)
	if err != nil {
		return nil, err
	}

	// 2. Load remote state.
	rs, err := LoadRemoteState(ctx, ar, owner, repoName)
	if err != nil {
		var mfe *ManifestFetchError
		hasPending := res.outcome == pendingInMempool || res.outcome == pendingReUploaded
		if errors.As(err, &mfe) && hasPending && mfe.TxID == res.manifestTxID {
			// The latest manifest in GraphQL is our own pending manifest
			// whose body isn't fetchable yet (Turbo bundle not settled).
			// We can proceed using the pending state — no need to parse
			// the manifest body since effectiveState uses pending refs.
			rs = &RemoteState{manifestTxID: mfe.TxID}
		} else {
			return nil, err
		}
	}

	// 3. Conflict detection: verify our known parent matches on-chain.
	if err := checkConflict(rs, res, state); err != nil {
		return nil, err
	}

	// 4. Compute effective state (refs and packs) from remote + pending.
	effectiveRefs, effectivePacks := effectiveState(rs, res)

	// 5. Apply ref updates.
	newRefs := mergeRefs(effectiveRefs, input.RefUpdates)

	// 6. Determine tips and bases for pack generation.
	tips, bases := computePackRange(input.RefUpdates, effectiveRefs)
	if len(tips) == 0 {
		// Ref-only update (e.g., delete). No new objects to upload.
		return uploadManifestOnly(ctx, uploader, state, repoName, rs, res, newRefs, effectivePacks)
	}

	// 7. Generate packfile.
	packData, err := pack.Generate(repo, bases, tips)
	if err != nil {
		return nil, fmt.Errorf("ops: generate pack: %w", err)
	}

	// 8. Upload pack.
	baseSHA, tipSHA := tips[0].String(), tips[len(tips)-1].String()
	if len(bases) > 0 {
		baseSHA = bases[0].String()
	}
	packTxID, err := uploader.Upload(ctx, packData, manifest.PackTags(repoName, baseSHA, tipSHA))
	if err != nil {
		return nil, fmt.Errorf("ops: upload pack: %w", err)
	}

	// 9. Build and upload manifest.
	parentTx := effectiveParentTx(rs, res)
	allPacks := append(effectivePacks, manifest.PackEntry{
		TX:   packTxID,
		Base: baseSHA,
		Tip:  tipSHA,
		Size: int64(len(packData)),
	})

	var m *manifest.Manifest
	if parentTx == "" {
		m = manifest.NewGenesis()
		m.Refs = newRefs
		m.Packs = allPacks
	} else {
		ext := extensions(rs)
		m = manifest.New(newRefs, allPacks, parentTx, ext)
	}

	manifestData, err := m.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ops: marshal manifest: %w", err)
	}

	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(repoName, parentTx))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	// 10. Save pending state.
	pending := &localstate.PendingState{
		PackTxID:     packTxID,
		ManifestTxID: manifestTxID,
		ParentTxID:   parentTx,
		Refs:         newRefs,
		PackBase:     baseSHA,
		PackTip:      tipSHA,
		UploadedAt:   time.Now(),
		Guaranteed:   uploader.Guaranteed(),
	}
	if err := state.SavePending(pending, packData); err != nil {
		return nil, fmt.Errorf("ops: save pending: %w", err)
	}

	return &PushResult{PackTxID: packTxID, ManifestTxID: manifestTxID, BytesUploaded: len(packData) + len(manifestData)}, nil
}

// forcePush creates a new genesis manifest with a full packfile,
// ignoring any existing remote state. Old manifests and packs
// remain on Arweave but are superseded by the new genesis.
func forcePush(
	ctx context.Context,
	uploader arweave.Uploader,
	repo *git.Repository,
	state *localstate.State,
	repoName string,
	input *PushInput,
) (*PushResult, error) {
	// Clear local state — start fresh.
	_ = state.ClearPending()

	// Collect tips (no bases — full pack).
	var tips []plumbing.Hash
	zeroHash := plumbing.ZeroHash.String()
	for _, sha := range input.RefUpdates {
		if sha != zeroHash {
			tips = append(tips, plumbing.NewHash(sha))
		}
	}
	if len(tips) == 0 {
		return nil, fmt.Errorf("ops: force push with no refs")
	}

	packData, err := pack.Generate(repo, nil, tips)
	if err != nil {
		return nil, fmt.Errorf("ops: generate pack: %w", err)
	}

	tipSHA := tips[0].String()
	packTxID, err := uploader.Upload(ctx, packData, manifest.PackTags(repoName, "", tipSHA))
	if err != nil {
		return nil, fmt.Errorf("ops: upload pack: %w", err)
	}

	m := manifest.NewGenesis()
	m.Refs = input.RefUpdates
	m.Packs = []manifest.PackEntry{{
		TX:   packTxID,
		Base: "",
		Tip:  tipSHA,
		Size: int64(len(packData)),
	}}

	manifestData, err := m.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ops: marshal manifest: %w", err)
	}

	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(repoName, ""))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	pending := &localstate.PendingState{
		PackTxID:     packTxID,
		ManifestTxID: manifestTxID,
		Refs:         input.RefUpdates,
		PackTip:      tipSHA,
		UploadedAt:   time.Now(),
		Guaranteed:   uploader.Guaranteed(),
	}
	if err := state.SavePending(pending, packData); err != nil {
		return nil, fmt.Errorf("ops: save pending: %w", err)
	}

	return &PushResult{PackTxID: packTxID, ManifestTxID: manifestTxID, BytesUploaded: len(packData) + len(manifestData)}, nil
}

// checkConflict verifies that the local parent expectation matches on-chain state.
func checkConflict(rs *RemoteState, res *pendingResolution, state *localstate.State) error {
	// New repo — no conflict possible.
	if rs.m == nil {
		return nil
	}

	// If we have a pending push in mempool, its parent should match on-chain.
	// If confirmed, the on-chain state was just updated by us.
	// If re-uploaded, the parent is the same as the original pending.
	switch res.outcome {
	case pendingConfirmed, noPending:
		// Check that our locally recorded parent matches on-chain.
		lastManifest, err := state.LoadLastManifestTxID()
		if err != nil {
			return fmt.Errorf("ops: load last manifest for conflict check: %w", err)
		}
		// If we have no local record, we're likely fetching for the first time
		// from this machine. Accept the remote state.
		if lastManifest == "" {
			return nil
		}
		if lastManifest != rs.manifestTxID {
			return fmt.Errorf("ops: conflict detected — remote manifest %q differs from local %q; run git fetch first", rs.manifestTxID, lastManifest)
		}
	case pendingInMempool, pendingReUploaded:
		// The pending push's parent should match the on-chain manifest.
		if res.parentTxID != rs.manifestTxID {
			return fmt.Errorf("ops: conflict detected — remote manifest %q differs from pending parent %q", rs.manifestTxID, res.parentTxID)
		}
	}
	return nil
}

// effectiveState returns the refs and packs to use as a base for the new manifest.
// When a push is still in mempool, we use the pending push's refs snapshot
// (which already includes the on-chain refs + the pending push's updates).
func effectiveState(rs *RemoteState, res *pendingResolution) (map[string]string, []manifest.PackEntry) {
	hasPending := res.outcome == pendingInMempool || res.outcome == pendingReUploaded

	// Refs: use pending refs if available (they are a superset of on-chain refs).
	var refs map[string]string
	if hasPending && res.refs != nil {
		refs = make(map[string]string, len(res.refs))
		for k, v := range res.refs {
			refs[k] = v
		}
	} else if rs.m != nil {
		refs = make(map[string]string, len(rs.m.Refs))
		for k, v := range rs.m.Refs {
			refs[k] = v
		}
	} else {
		refs = map[string]string{}
	}

	// Packs: on-chain packs + pending pack.
	var packs []manifest.PackEntry
	if rs.m != nil {
		packs = make([]manifest.PackEntry, len(rs.m.Packs))
		copy(packs, rs.m.Packs)
	}
	if hasPending {
		packs = append(packs, manifest.PackEntry{TX: res.packTxID})
	}

	return refs, packs
}

// effectiveParentTx returns the parent tx to use for the new manifest.
func effectiveParentTx(rs *RemoteState, res *pendingResolution) string {
	// If there's a pending push in mempool, the new manifest chains from it
	// (we don't have its tx-id yet since it hasn't confirmed). In this case
	// we chain from the on-chain manifest (same parent as the pending).
	// The pending manifest will confirm and become the actual parent; if it
	// doesn't confirm and gets re-uploaded, both will share the same parent.
	return rs.manifestTxID
}

// mergeRefs applies updates to the base refs.
func mergeRefs(base, updates map[string]string) map[string]string {
	merged := make(map[string]string, len(base))
	for k, v := range base {
		merged[k] = v
	}
	zeroHash := plumbing.ZeroHash.String()
	for k, v := range updates {
		if v == zeroHash {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	return merged
}

// computePackRange determines tips and bases for pack generation.
func computePackRange(updates, currentRefs map[string]string) (tips, bases []plumbing.Hash) {
	zeroHash := plumbing.ZeroHash.String()
	for _, sha := range updates {
		if sha != zeroHash {
			tips = append(tips, plumbing.NewHash(sha))
		}
	}
	for _, sha := range currentRefs {
		bases = append(bases, plumbing.NewHash(sha))
	}
	return tips, bases
}

// extensions returns the extensions map from the remote state, or nil.
func extensions(rs *RemoteState) map[string]json.RawMessage {
	if rs.m != nil {
		return rs.m.Extensions
	}
	return nil
}

// uploadManifestOnly handles ref-only updates (no new pack data).
func uploadManifestOnly(
	ctx context.Context,
	uploader arweave.Uploader,
	state *localstate.State,
	repoName string,
	rs *RemoteState,
	res *pendingResolution,
	newRefs map[string]string,
	packs []manifest.PackEntry,
) (*PushResult, error) {
	parentTx := effectiveParentTx(rs, res)

	var m *manifest.Manifest
	if parentTx == "" {
		m = manifest.NewGenesis()
		m.Refs = newRefs
		m.Packs = packs
	} else {
		m = manifest.New(newRefs, packs, parentTx, extensions(rs))
	}

	manifestData, err := m.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ops: marshal manifest: %w", err)
	}

	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(repoName, parentTx))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	pending := &localstate.PendingState{
		ManifestTxID: manifestTxID,
		ParentTxID:   parentTx,
		Refs:         newRefs,
		UploadedAt:   time.Now(),
		Guaranteed:   uploader.Guaranteed(),
	}
	if err := state.SavePending(pending, nil); err != nil {
		return nil, fmt.Errorf("ops: save pending: %w", err)
	}

	return &PushResult{ManifestTxID: manifestTxID, BytesUploaded: len(manifestData)}, nil
}
