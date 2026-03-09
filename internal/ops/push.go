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
		return forcePush(ctx, ar, uploader, repo, state, cfg, repoName, input)
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

	// 4a. Fork detection: if pushing to a new repo and we have source
	// packs from a previous fetch, include them so the fork reuses
	// existing Arweave data instead of re-uploading.
	var sourcePacks []manifest.PackEntry
	var forkedFrom string
	if rs.m == nil && len(effectivePacks) == 0 {
		sp, err := state.LoadSourcePacks()
		if err != nil {
			return nil, fmt.Errorf("ops: load source packs: %w", err)
		}
		if len(sp) > 0 {
			sourcePacks = sp
			effectivePacks = sp
			forkedFrom, _ = state.LoadSourceManifest()
		}
	}

	// 5. Apply ref updates.
	newRefs := mergeRefs(effectiveRefs, input.RefUpdates)

	// 6. Determine tips and bases for pack generation.
	tips, bases := computePackRange(input.RefUpdates, effectiveRefs)
	// For forks, add source pack tips as bases so we only generate the delta.
	if len(sourcePacks) > 0 {
		for _, pe := range sourcePacks {
			if pe.Tip != "" {
				bases = append(bases, plumbing.NewHash(pe.Tip))
			}
		}
	}
	// 6a. Private fork: import epoch keys from the original keymap so that
	// encryptAndUpload reuses the same symmetric keys for source packs.
	if len(sourcePacks) > 0 && cfg.IsPrivate() {
		sourceKeymap, _ := state.LoadSourceKeymap()
		if sourceKeymap != "" {
			if err := importForkEpochKeys(ctx, ar, state, sourceKeymap); err != nil {
				return nil, fmt.Errorf("ops: import fork epoch keys: %w", err)
			}
		}
	}

	if len(tips) == 0 {
		// Ref-only update (e.g., delete). No new objects to upload.
		return uploadManifestOnly(ctx, uploader, state, repoName, rs, res, newRefs, effectivePacks, forkedFrom)
	}

	// 7. Generate packfile.
	packData, err := pack.Generate(repo, bases, tips)
	if err != nil {
		return nil, fmt.Errorf("ops: generate pack: %w", err)
	}
	if packData == nil && !cfg.IsPrivate() {
		// No new objects, public repo. Upload manifest only.
		result, err := uploadManifestOnly(ctx, uploader, state, repoName, rs, res, newRefs, effectivePacks, forkedFrom)
		if err != nil {
			return nil, err
		}
		if len(sourcePacks) > 0 {
			state.ClearSourceState()
		}
		return result, nil
	}
	// For private repos packData may be nil (e.g., pure fork) but we still
	// need encryptAndUpload to handle encryption, keymap, and encrypted manifest.

	// 7b. Private→public conversion: upload open keymap so historical
	// encrypted packs remain accessible to anyone.
	var openKeymapTx string
	if !cfg.IsPrivate() {
		openKM, err := buildOpenKeyMap(state)
		if err != nil {
			return nil, fmt.Errorf("ops: build open keymap: %w", err)
		}
		if openKM != nil {
			openKeymapTx, err = uploadOpenKeyMap(ctx, uploader, openKM, repoName)
			if err != nil {
				return nil, fmt.Errorf("ops: upload open keymap: %w", err)
			}
		}
	}

	// 8. Encrypt, upload pack+keymap+manifest, save pending.
	baseSHA, tipSHA := tips[0].String(), tips[len(tips)-1].String()
	if len(bases) > 0 {
		baseSHA = bases[0].String()
	}
	parentTx := effectiveParentTx(rs, res)

	result, err := encryptAndUpload(ctx, ar, uploader, state, cfg, repoName, &uploadParams{
		packData:      packData,
		refs:          newRefs,
		existingPacks: effectivePacks,
		baseSHA:       baseSHA,
		tipSHA:        tipSHA,
		parentTx:      parentTx,
		forkedFrom:    forkedFrom,
		openKeymapTx:  openKeymapTx,
		extensions:    extensions(rs),
	})
	if err != nil {
		return nil, err
	}

	// Clean up source state after successful fork push.
	if len(sourcePacks) > 0 {
		state.ClearSourceState()
	}

	return result, nil
}

// forcePush creates a new genesis manifest with a full packfile,
// ignoring any existing remote state. Old manifests and packs
// remain on Arweave but are superseded by the new genesis.
func forcePush(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	repo *git.Repository,
	state *localstate.State,
	cfg *config.Config,
	repoName string,
	input *PushInput,
) (*PushResult, error) {
	// Clear per-remote state — start fresh.
	_ = state.ClearPending()
	_ = state.SaveLastManifestTxID("")

	// Load source packs before clearing — fork force push reuses them.
	sourcePacks, _ := state.LoadSourcePacks()
	forkedFrom, _ := state.LoadSourceManifest()

	// Collect tips.
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

	// Use source pack tips as bases so we only upload the delta.
	var bases []plumbing.Hash
	for _, pe := range sourcePacks {
		if pe.Tip != "" {
			bases = append(bases, plumbing.NewHash(pe.Tip))
		}
	}

	// Private fork: import epoch keys from the original keymap.
	if len(sourcePacks) > 0 && cfg.IsPrivate() {
		sourceKeymap, _ := state.LoadSourceKeymap()
		if sourceKeymap != "" {
			if err := importForkEpochKeys(ctx, ar, state, sourceKeymap); err != nil {
				return nil, fmt.Errorf("ops: import fork epoch keys: %w", err)
			}
		}
	}

	packData, err := pack.Generate(repo, bases, tips)
	if err != nil {
		return nil, fmt.Errorf("ops: generate pack: %w", err)
	}

	tipSHA := tips[0].String()
	baseSHA := ""
	if len(bases) > 0 {
		baseSHA = bases[0].String()
	}

	result, err := encryptAndUpload(ctx, ar, uploader, state, cfg, repoName, &uploadParams{
		packData:      packData,
		refs:          input.RefUpdates,
		existingPacks: sourcePacks,
		baseSHA:       baseSHA,
		tipSHA:        tipSHA,
		forkedFrom:    forkedFrom,
	})
	if err != nil {
		return nil, err
	}

	state.ClearSourceState()
	return result, nil
}

// uploadParams describes what to upload after pack generation.
type uploadParams struct {
	packData      []byte
	refs          map[string]string
	existingPacks []manifest.PackEntry               // packs from previous manifests
	baseSHA       string                              // "" for genesis/force
	tipSHA        string
	parentTx      string                              // "" for genesis/force
	forkedFrom    string                              // source manifest tx for forks
	openKeymapTx  string                              // private→public conversion keymap
	extensions    map[string]json.RawMessage
}

// encryptAndUpload handles the common tail of both normal and force push:
// encrypt pack → upload pack → upload keymap → build manifest → encrypt
// manifest → upload manifest → save genesis → save pending.
func encryptAndUpload(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	state *localstate.State,
	cfg *config.Config,
	repoName string,
	p *uploadParams,
) (*PushResult, error) {
	packData := p.packData

	// 1. Encryption (private repos).
	var ec *encryptionContext
	visibility := ""
	keymapTx := p.openKeymapTx // may be set by private→public conversion
	epoch := 0
	if cfg.IsPrivate() {
		visibility = manifest.VisibilityPrivate
		if _, err := state.AddReader(ar.Address(), ar.Owner()); err != nil {
			return nil, fmt.Errorf("ops: ensure owner in readers: %w", err)
		}
		var err error
		ec, err = initEncryption(state)
		if err != nil {
			return nil, fmt.Errorf("ops: init encryption: %w", err)
		}
		epoch = ec.epoch
		if packData != nil {
			packData, err = ec.encryptData(packData)
			if err != nil {
				return nil, fmt.Errorf("ops: encrypt pack: %w", err)
			}
		}
	}

	// 2. Upload pack (skipped for manifest-only pushes like pure forks).
	var packTxID string
	if packData != nil {
		var err error
		packTxID, err = uploader.Upload(ctx, packData, manifest.PackTags(repoName, p.baseSHA, p.tipSHA, visibility))
		if err != nil {
			return nil, fmt.Errorf("ops: upload pack: %w", err)
		}
	}

	// 3. Upload keymap if needed (private repos).
	if ec != nil {
		var err error
		if ec.changed {
			keymapTx, err = buildAndUploadKeyMap(ctx, uploader, state, repoName, ar.Owner(), ar.RSAPublicKey())
			if err != nil {
				return nil, fmt.Errorf("ops: upload keymap: %w", err)
			}
		} else {
			keymapTx = ec.keymapTx
		}
	}

	// 4. Build manifest.
	allPacks := make([]manifest.PackEntry, len(p.existingPacks))
	copy(allPacks, p.existingPacks)
	if packTxID != "" {
		allPacks = append(allPacks, manifest.PackEntry{
			TX:        packTxID,
			Base:      p.baseSHA,
			Tip:       p.tipSHA,
			Size:      int64(len(packData)),
			Epoch:     epoch,
			Encrypted: ec != nil,
		})
	}

	var m *manifest.Manifest
	if p.parentTx == "" {
		m = manifest.NewGenesis()
		m.Refs = p.refs
		m.Packs = allPacks
	} else {
		m = manifest.New(p.refs, allPacks, p.parentTx, p.extensions)
	}
	m.KeyMap = keymapTx

	manifestData, err := m.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ops: marshal manifest: %w", err)
	}

	// 5. Encrypt manifest body (private repos).
	if ec != nil {
		manifestData, err = ec.encryptData(manifestData)
		if err != nil {
			return nil, fmt.Errorf("ops: encrypt manifest: %w", err)
		}
	}

	// 6. Upload manifest.
	genesisTx, _ := state.LoadGenesisManifest()
	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(manifest.RefsTagsOpts{
		RepoName:   repoName,
		ParentTx:   p.parentTx,
		Visibility: visibility,
		KeyMapTx:   keymapTx,
		ForkedFrom: p.forkedFrom,
		GenesisTx:  genesisTx,
		Encrypted:  ec != nil,
	}))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	// 7. Save genesis tx-id on genesis push.
	if p.parentTx == "" {
		_ = state.SaveGenesisManifest(manifestTxID)
	}

	// 8. Save pending state.
	pending := &localstate.PendingState{
		PackTxID:     packTxID,
		ManifestTxID: manifestTxID,
		ParentTxID:   p.parentTx,
		Refs:         p.refs,
		Packs:        allPacks,
		PackBase:     p.baseSHA,
		PackTip:      p.tipSHA,
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
	forkedFrom string,
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

	genesisTx, _ := state.LoadGenesisManifest()
	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(manifest.RefsTagsOpts{
		RepoName:   repoName,
		ParentTx:   parentTx,
		ForkedFrom: forkedFrom,
		GenesisTx:  genesisTx,
	}))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	if parentTx == "" {
		_ = state.SaveGenesisManifest(manifestTxID)
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
