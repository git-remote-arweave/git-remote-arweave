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
	// Verify wallet identity matches the remote URL owner.
	walletAddr := ar.Address()
	if walletAddr != "" && owner != "" && walletAddr != owner {
		return nil, fmt.Errorf("ops: wallet address %q does not match remote owner %q — check ARWEAVE_WALLET / arweave.wallet config", walletAddr, owner)
	}

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
		if errors.As(err, &mfe) && hasPending && (mfe.TxID == res.manifestTxID || mfe.TxID == res.parentTxID) {
			// The latest manifest in GraphQL is either our pending manifest
			// or its parent (both Turbo bundles not yet settled).
			// We can proceed using the pending state — no need to parse
			// the manifest body since effectiveState uses pending refs.
			rs = &RemoteState{manifestTxID: mfe.TxID}
		} else if errors.As(err, &mfe) && arweave.IsTransient(mfe.Err) {
			return nil, fmt.Errorf("ops: manifest %s temporarily unavailable (gateway error), try again later", mfe.TxID)
		} else {
			return nil, err
		}
	}

	// 3. Conflict detection: verify our known parent matches on-chain.
	//    When our local state is ahead of GraphQL (previous push delivered
	//    but not yet indexed), checkConflict returns an updated RemoteState
	//    based on the local manifest fetched from the gateway.
	rs, err = checkConflict(ctx, ar, rs, res, state)
	if err != nil {
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

	// 7a. Encryption (private repos).
	var ec *encryptionContext
	visibility := ""
	keymapTx := ""
	epoch := 0
	if cfg.IsPrivate() {
		visibility = manifest.VisibilityPrivate
		ec, err = initEncryption(state)
		if err != nil {
			return nil, fmt.Errorf("ops: init encryption: %w", err)
		}
		epoch = ec.epoch
		packData, err = ec.encryptData(packData)
		if err != nil {
			return nil, fmt.Errorf("ops: encrypt pack: %w", err)
		}
	}

	// 7b. Private→public conversion: upload open keymap so historical
	// encrypted packs remain accessible to anyone.
	if !cfg.IsPrivate() {
		openKM, err := buildOpenKeyMap(state)
		if err != nil {
			return nil, fmt.Errorf("ops: build open keymap: %w", err)
		}
		if openKM != nil {
			keymapTx, err = uploadOpenKeyMap(ctx, uploader, openKM, repoName)
			if err != nil {
				return nil, fmt.Errorf("ops: upload open keymap: %w", err)
			}
		}
	}

	// 8. Upload pack.
	baseSHA, tipSHA := tips[0].String(), tips[len(tips)-1].String()
	if len(bases) > 0 {
		baseSHA = bases[0].String()
	}
	packTxID, err := uploader.Upload(ctx, packData, manifest.PackTags(repoName, baseSHA, tipSHA, visibility))
	if err != nil {
		return nil, fmt.Errorf("ops: upload pack: %w", err)
	}

	// 8a. Upload keymap if needed (private repos).
	if ec != nil {
		if ec.changed {
			keymapTx, err = buildAndUploadKeyMap(ctx, uploader, state, repoName, ar.Owner(), ar.RSAPublicKey())
			if err != nil {
				return nil, fmt.Errorf("ops: upload keymap: %w", err)
			}
		} else {
			keymapTx = ec.keymapTx
		}
	}

	// 9. Build and upload manifest.
	parentTx := effectiveParentTx(rs, res)
	allPacks := append(effectivePacks, manifest.PackEntry{
		TX:        packTxID,
		Base:      baseSHA,
		Tip:       tipSHA,
		Size:      int64(len(packData)),
		Epoch:     epoch,
		Encrypted: ec != nil,
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
	m.KeyMap = keymapTx

	manifestData, err := m.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ops: marshal manifest: %w", err)
	}

	// 9a. Encrypt manifest body (private repos).
	if ec != nil {
		manifestData, err = ec.encryptData(manifestData)
		if err != nil {
			return nil, fmt.Errorf("ops: encrypt manifest: %w", err)
		}
	}

	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(repoName, parentTx, visibility, keymapTx, ec != nil))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	// 10. Save pending state.
	pending := &localstate.PendingState{
		PackTxID:     packTxID,
		ManifestTxID: manifestTxID,
		ParentTxID:   parentTx,
		Refs:         newRefs,
		Packs:        allPacks,
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
	// Clear local state — start fresh. Reset last-manifest so that
	// checkConflict accepts whatever remote state exists after the
	// force push replaces the manifest chain.
	_ = state.ClearPending()
	_ = state.SaveLastManifestTxID("")

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
	packTxID, err := uploader.Upload(ctx, packData, manifest.PackTags(repoName, "", tipSHA, ""))
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

	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(repoName, "", "", "", false))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	pending := &localstate.PendingState{
		PackTxID:     packTxID,
		ManifestTxID: manifestTxID,
		Refs:         input.RefUpdates,
		Packs:        m.Packs,
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
// When the local last-manifest is ahead of GraphQL (our previous push is
// delivered but not yet indexed), checkConflict fetches the manifest body
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
		lastManifest, err := state.LoadLastManifestTxID()
		if err != nil {
			return nil, fmt.Errorf("ops: load last manifest for conflict check: %w", err)
		}
		if lastManifest == "" {
			return rs, nil
		}
		if lastManifest == rs.manifestTxID {
			return rs, nil
		}
		// Local is ahead of GraphQL — verify ancestry by fetching our
		// last manifest from the gateway and checking its parent chain.
		return resolveAheadOfGraphQL(ctx, ar, rs, lastManifest)

	case pendingInMempool, pendingReUploaded:
		if res.parentTxID != rs.manifestTxID {
			return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from pending parent %q", rs.manifestTxID, res.parentTxID)
		}
	}
	return rs, nil
}

// resolveAheadOfGraphQL handles the case where our local last-manifest is
// not yet visible in GraphQL. It fetches the manifest from the gateway
// and walks the parent chain to verify that the on-chain manifest (from
// GraphQL) is an ancestor. If so, returns the local manifest as effective
// remote state so the new push chains from it correctly.
func resolveAheadOfGraphQL(
	ctx context.Context,
	ar *arweave.Client,
	rs *RemoteState,
	lastManifest string,
) (*RemoteState, error) {
	if ar == nil {
		return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from local %q; run git fetch first", rs.manifestTxID, lastManifest)
	}

	// Walk from lastManifest toward rs.manifestTxID via parent links.
	cur := lastManifest
	var latestM *manifest.Manifest
	for i := 0; i < 10; i++ { // bound the walk to avoid infinite loops
		data, err := ar.Fetch(ctx, cur)
		if err != nil {
			// Can't fetch — treat as real conflict.
			return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from local %q (fetch failed: %v)", rs.manifestTxID, lastManifest, err)
		}
		m, err := manifest.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from local %q (parse failed: %v)", rs.manifestTxID, lastManifest, err)
		}
		if i == 0 {
			latestM = m
		}
		if cur == rs.manifestTxID || m.Parent == rs.manifestTxID {
			// On-chain manifest is an ancestor — no conflict.
			// Use our latest manifest as effective remote state.
			return &RemoteState{manifestTxID: lastManifest, m: latestM}, nil
		}
		if m.Parent == "" {
			break // reached genesis without finding rs.manifestTxID
		}
		cur = m.Parent
	}

	return nil, fmt.Errorf("ops: conflict detected — remote manifest %q differs from local %q; run git fetch first", rs.manifestTxID, lastManifest)
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
	// When rs.m is nil but we have pending packs (e.g., on-chain manifest
	// unfetchable), use the pending pack list which includes the full history.
	var packs []manifest.PackEntry
	if rs.m != nil {
		packs = make([]manifest.PackEntry, len(rs.m.Packs))
		copy(packs, rs.m.Packs)
		if hasPending {
			packs = append(packs, manifest.PackEntry{TX: res.packTxID})
		}
	} else if hasPending && len(res.packs) > 0 {
		packs = make([]manifest.PackEntry, len(res.packs))
		copy(packs, res.packs)
	} else if hasPending {
		packs = []manifest.PackEntry{{TX: res.packTxID}}
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

	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(repoName, parentTx, "", "", false))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	pending := &localstate.PendingState{
		ManifestTxID: manifestTxID,
		ParentTxID:   parentTx,
		Refs:         newRefs,
		Packs:        packs,
		UploadedAt:   time.Now(),
		Guaranteed:   uploader.Guaranteed(),
	}
	if err := state.SavePending(pending, nil); err != nil {
		return nil, fmt.Errorf("ops: save pending: %w", err)
	}

	return &PushResult{ManifestTxID: manifestTxID, BytesUploaded: len(manifestData)}, nil
}
